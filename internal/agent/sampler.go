package agent

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

// netCounter is a raw interface counter reading of the platform code.
type netCounter struct {
	Index             int
	Name, Alias, MAC  string
	Speed             int64
	Up                bool
	InBytes, OutBytes uint64
	InErr, OutErr     uint64
	VLANs             []int // tagged VLANs on the port
}

// Sampler takes the reports; it keeps the previous counters to turn them
// into rates.
type Sampler struct {
	mu     sync.Mutex
	cpu    cpuState
	prev   map[int]netCounter
	prevAt time.Time
	vx     []proto.VXLAN // VXLANs, read every five minutes
	vxAt   time.Time
}

// NewSampler prepares a sampler (the first CPU value comes on the second report).
func NewSampler() *Sampler {
	s := &Sampler{prev: map[int]netCounter{}}
	cpuPercent(&s.cpu)
	return s
}

func rate(cur, old uint64, dt float64, mul float64) *float64 {
	if cur < old || dt <= 0 {
		return nil // counter reset (driver reload)
	}
	v := float64(cur-old) / dt * mul
	return &v
}

// Sample reads the machine now.
func (s *Sampler) Sample(ctx context.Context, services []string) *proto.Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	r := &proto.Report{Time: now.UTC(), CPU: cpuPercent(&s.cpu), Uptime: uptimeSeconds(), Load1: load1(), Procs: procs()}
	r.MemTotal, r.MemUsed, r.SwapTotal, r.SwapUsed = memory()
	if bt := bootTime(); !bt.IsZero() {
		r.Boot = &bt
	}
	r.Disks = disks()
	ncs, err := netCounters()
	if err != nil {
		r.Errors = append(r.Errors, "network: "+err.Error())
	}
	dt := now.Sub(s.prevAt).Seconds()
	cur := map[int]netCounter{}
	for _, c := range ncs {
		cur[c.Index] = c
		n := proto.Net{Index: c.Index, Name: c.Name, Alias: c.Alias, MAC: c.MAC, Speed: c.Speed, Up: c.Up, VLANs: c.VLANs}
		if old, ok := s.prev[c.Index]; ok && !s.prevAt.IsZero() {
			n.InBps = rate(c.InBytes, old.InBytes, dt, 8)
			n.OutBps = rate(c.OutBytes, old.OutBytes, dt, 8)
			n.InErr = rate(c.InErr, old.InErr, dt, 1)
			n.OutErr = rate(c.OutErr, old.OutErr, dt, 1)
		}
		r.Nets = append(r.Nets, n)
	}
	sort.Slice(r.Nets, func(i, j int) bool { return r.Nets[i].Index < r.Nets[j].Index })
	s.prev, s.prevAt = cur, now
	if len(services) > 0 {
		r.Services = serviceStates(ctx, services)
	}
	if now.Sub(s.vxAt) >= 5*time.Minute {
		s.vx, s.vxAt = vxlans(ctx), now
	}
	r.VXLANs = s.vx
	return r
}

// CollectInventory describes the machine.
func CollectInventory(ctx context.Context) *proto.Inventory {
	host, _ := os.Hostname()
	inv := &proto.Inventory{Hostname: host, OS: osName(), Arch: runtime.GOARCH, CPUs: runtime.NumCPU()}
	inv.MemTotal, _, _, _ = memory()
	platformInventory(ctx, inv)
	inv.Addresses = localAddresses()
	return inv
}

// parseProcVLAN reads Linux's /proc/net/vlan/config ("eth0.10 | 10 | eth0"
// after two header lines): the VLANs on each parent interface.
func parseProcVLAN(s string) map[string][]int {
	out := map[string][]int{}
	for _, line := range strings.Split(s, "\n") {
		f := strings.Split(line, "|")
		if len(f) != 3 {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSpace(f[1]))
		parent := strings.TrimSpace(f[2])
		if err != nil || id < 1 || id > 4094 || parent == "" {
			continue
		}
		out[parent] = append(out[parent], id)
	}
	for p := range out {
		sort.Ints(out[p])
	}
	return out
}

// ifNameRE is a Linux interface name (at most 15 characters, not an option).
var ifNameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:@-]{0,14}$`)

// parseIPVXLAN reads "ip -d -j link show type vxlan".
func parseIPVXLAN(b []byte) []proto.VXLAN {
	var links []struct {
		Name     string `json:"ifname"`
		LinkInfo struct {
			Kind string `json:"info_kind"`
			Data struct {
				ID     int    `json:"id"`
				Remote string `json:"remote"`
				Local  string `json:"local"`
				Port   int    `json:"port"`
			} `json:"info_data"`
		} `json:"linkinfo"`
	}
	if json.Unmarshal(b, &links) != nil {
		return nil
	}
	var out []proto.VXLAN
	for _, l := range links {
		d := l.LinkInfo.Data
		if l.LinkInfo.Kind != "vxlan" || d.ID < 1 {
			continue
		}
		x := proto.VXLAN{VNI: d.ID, Name: l.Name, Local: d.Local, Port: d.Port}
		if d.Remote != "" {
			x.Remotes = []string{d.Remote}
		}
		out = append(out, x)
	}
	return out
}

// parseFDBRemotes reads the remote VTEPs ("dst") of "bridge -j fdb show".
func parseFDBRemotes(b []byte) []string {
	var rows []struct {
		Dst string `json:"dst"`
	}
	if json.Unmarshal(b, &rows) != nil {
		return nil
	}
	var out []string
	for _, r := range rows {
		if r.Dst != "" {
			out = mergeRemotes(out, []string{r.Dst})
		}
	}
	return out
}

func mergeRemotes(list, more []string) []string {
	for _, m := range more {
		dup := false
		for _, x := range list {
			dup = dup || x == m
		}
		if !dup && len(list) < 64 {
			list = append(list, m)
		}
	}
	return list
}
