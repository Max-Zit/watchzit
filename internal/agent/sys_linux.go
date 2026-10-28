//go:build linux

package agent

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

func protectDir(dir string) error { return os.Chmod(dir, 0o700) } // #nosec G302 -- a directory needs the x bit; root only

func markEnrolled(bool) {}

// cpuState holds the last /proc/stat totals.
type cpuState struct{ idle, total uint64 }

func cpuPercent(st *cpuState) float64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0
	}
	line, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return 0
	}
	var total, idle uint64
	for i, s := range f[1:] {
		v, _ := strconv.ParseUint(s, 10, 64)
		if i >= 8 { // guest time is already in user time
			break
		}
		total += v
		if i == 3 || i == 4 { // idle, iowait
			idle += v
		}
	}
	dt, di := total-st.total, idle-st.idle
	first := st.total == 0
	st.total, st.idle = total, idle
	if first || dt == 0 || total < dt {
		return 0
	}
	return max(0, min(100, float64(dt-di)/float64(dt)*100))
}

func meminfo() map[string]uint64 {
	out := map[string]uint64{}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		n, _ := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
		out[k] = n * 1024
	}
	return out
}

func memory() (total, used, swapTotal, swapUsed uint64) {
	m := meminfo()
	total = m["MemTotal"]
	avail, ok := m["MemAvailable"]
	if !ok {
		avail = m["MemFree"] + m["Buffers"] + m["Cached"]
	}
	used = total - min(avail, total)
	swapTotal = m["SwapTotal"]
	swapUsed = swapTotal - min(m["SwapFree"], swapTotal)
	return
}

func uptimeSeconds() float64 {
	b, _ := os.ReadFile("/proc/uptime")
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

func bootTime() time.Time {
	up := uptimeSeconds()
	if up == 0 {
		return time.Time{}
	}
	return time.Now().Add(-time.Duration(up * float64(time.Second))).UTC().Truncate(time.Second)
}

func load1() *float64 {
	b, _ := os.ReadFile("/proc/loadavg")
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return nil
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return nil
	}
	return &v
}

func procs() int {
	b, _ := os.ReadFile("/proc/loadavg")
	f := strings.Fields(string(b))
	if len(f) < 4 {
		return 0
	}
	_, all, _ := strings.Cut(f[3], "/")
	n, _ := strconv.Atoi(all)
	return n
}

// realFS are the file systems worth a disk check (no tmpfs, overlay …).
var realFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true, "vfat": true,
	"exfat": true, "ntfs": true, "ntfs3": true, "f2fs": true, "jfs": true, "reiserfs": true, "nfs": true, "nfs4": true, "cifs": true, "fuseblk": true}

func disks() []proto.Disk {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	var out []proto.Disk
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || !realFS[f[2]] || seen[f[0]] {
			continue
		}
		mount := strings.ReplaceAll(f[1], `\040`, " ")
		var st syscall.Statfs_t
		if syscall.Statfs(mount, &st) != nil || st.Blocks == 0 {
			continue
		}
		seen[f[0]] = true
		bs := uint64(st.Bsize) // #nosec G115 -- block size is positive
		total := st.Blocks * bs
		out = append(out, proto.Disk{Mount: mount, FS: f[2], Total: total, Used: total - st.Bfree*bs})
	}
	return out
}

func readSys(p string) string {
	b, err := os.ReadFile(p) // #nosec G304 G703 -- /sys/class/net paths built from interface names
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func netCounters() ([]netCounter, error) {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	vlans := map[string][]int{} // VLAN interfaces (eth0.10) on their parents
	if v, err := os.ReadFile("/proc/net/vlan/config"); err == nil {
		vlans = parseProcVLAN(string(v))
	}
	var out []netCounter
	for _, line := range strings.Split(string(b), "\n")[2:] {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if len(f) < 16 || name == "lo" {
			continue
		}
		base := filepath.Join("/sys/class/net", name)
		idx, _ := strconv.Atoi(readSys(filepath.Join(base, "ifindex")))
		c := netCounter{Index: idx, Name: name, MAC: readSys(filepath.Join(base, "address")), Up: readSys(filepath.Join(base, "operstate")) == "up"}
		c.VLANs = vlans[name]
		if c.MAC == "00:00:00:00:00:00" {
			c.MAC = ""
		}
		if sp, err := strconv.ParseInt(readSys(filepath.Join(base, "speed")), 10, 64); err == nil && sp > 0 {
			c.Speed = sp * 1_000_000
		}
		c.Alias = readSys(filepath.Join(base, "ifalias"))
		c.InBytes, _ = strconv.ParseUint(f[0], 10, 64)
		c.InErr, _ = strconv.ParseUint(f[2], 10, 64)
		c.OutBytes, _ = strconv.ParseUint(f[8], 10, 64)
		c.OutErr, _ = strconv.ParseUint(f[10], 10, 64)
		out = append(out, c)
	}
	return out, nil
}

var unitRE = func(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.@:", r)) {
			return false
		}
	}
	return s != "" && len(s) < 200
}

func serviceStates(ctx context.Context, names []string) []proto.Service {
	var out []proto.Service
	for _, n := range names {
		if !unitRE(n) {
			continue
		}
		unit := n
		if !strings.Contains(unit, ".") {
			unit += ".service"
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		b, err := exec.CommandContext(cctx, "systemctl", "show", "-p", "ActiveState,LoadState,Description", "--", unit).Output() // #nosec G204 -- unit name checked above
		cancel()
		s := proto.Service{Name: n, State: "missing"}
		if err == nil {
			props := map[string]string{}
			for _, l := range strings.Split(string(b), "\n") {
				if k, v, ok := strings.Cut(l, "="); ok {
					props[k] = v
				}
			}
			if props["LoadState"] != "not-found" {
				s.State, s.Display = props["ActiveState"], props["Description"]
				s.Running = s.State == "active"
			}
		}
		out = append(out, s)
	}
	return out
}

func osRelease() map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			out[k] = strings.Trim(v, `"`)
		}
	}
	return out
}

func basicOS() string {
	if n := osRelease()["PRETTY_NAME"]; n != "" {
		return n
	}
	return "Linux"
}

func platformInventory(ctx context.Context, inv *proto.Inventory) {
	inv.OSVersion = osRelease()["VERSION_ID"]
	var u syscall.Utsname
	if syscall.Uname(&u) == nil {
		inv.Kernel = charsToString(u.Release[:])
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "model name" {
				inv.CPUModel = strings.TrimSpace(v)
				break
			}
		}
	}
	inv.Vendor = readSys("/sys/class/dmi/id/sys_vendor")
	inv.Model = readSys("/sys/class/dmi/id/product_name")
	inv.Serial = readSys("/sys/class/dmi/id/product_serial")
	// enabled services the console can offer to watch
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	b, err := exec.CommandContext(cctx, "systemctl", "list-unit-files", "--type=service", "--no-legend", "--no-pager").Output()
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || strings.Contains(f[0], "@") {
			continue
		}
		if f[1] == "enabled" {
			inv.Services = append(inv.Services, proto.ServiceInfo{Name: strings.TrimSuffix(f[0], ".service"), Auto: true})
		}
	}
}

func charsToString[T int8 | uint8](ca []T) string {
	b := make([]byte, 0, len(ca))
	for _, c := range ca {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}
