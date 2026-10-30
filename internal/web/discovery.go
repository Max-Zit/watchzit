package web

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/discovery"
	"github.com/max-zit/watchzit/internal/probe"
	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/snmpmon"
	"github.com/max-zit/watchzit/internal/store"
)

// Network discovery: ranges scanned now or every day or week; the hosts
// found are offered as new devices.

// scans are the ranges being scanned.
type scans struct {
	mu      sync.Mutex
	running map[int64]time.Time
}

func (sc *scans) start(id int64, now time.Time) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.running == nil {
		sc.running = map[int64]time.Time{}
	}
	if _, busy := sc.running[id]; busy {
		return false
	}
	sc.running[id] = now
	return true
}

func (sc *scans) done(id int64) {
	sc.mu.Lock()
	delete(sc.running, id)
	sc.mu.Unlock()
}

func (sc *scans) busy() map[int64]bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	out := map[int64]bool{}
	for id := range sc.running {
		out[id] = true
	}
	return out
}

// prober is the real network prober; tests set Server.Prober.
func (s *Server) prober(ctx context.Context) *discovery.Prober {
	if s.Prober != nil {
		return s.Prober
	}
	profiles, _ := s.store.SNMPProfiles(ctx)
	return &discovery.Prober{
		Ping: func(ctx context.Context, a netip.Addr) (time.Duration, bool) {
			r, err := probe.Ping(ctx, a, 1, time.Second)
			if err != nil || r.Received == 0 {
				return 0, false
			}
			rtt, _ := r.AvgRTT()
			return rtt, true
		},
		Dial: discovery.DialTCP,
		Identify: func(ctx context.Context, a netip.Addr) (string, string, string, int64) {
			for _, pr := range profiles {
				cfg, err := s.store.SNMPConfig(pr)
				if err != nil {
					continue
				}
				cfg.Timeout, cfg.Retries = 1500*time.Millisecond, 0
				c, err := snmp.Dial(netip.AddrPortFrom(a, uint16(pr.Port)), cfg) // #nosec G115 -- port 1–65535
				if err != nil {
					continue
				}
				f, err := snmpmon.Facts(ctx, c)
				_ = c.Close()
				if err == nil {
					return f.SysName, f.SysDescr, f.Vendor, pr.ID
				}
			}
			return "", "", "", 0
		},
		Lookup: discovery.LookupName,
	}
}

// scanRange scans one range and stores what it found.
func (s *Server) scanRange(ctx context.Context, r *store.DiscoveryRange) error {
	if !s.scans.start(r.ID, s.now()) {
		return nil
	}
	defer s.scans.done(r.ID)
	addrs, err := discovery.Range(r.CIDR)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	found := s.prober(ctx).Scan(ctx, addrs)
	var hosts []store.DiscoveredHost
	for _, h := range found {
		var ports []int32
		for _, p := range h.Ports {
			ports = append(ports, int32(p)) // #nosec G115 -- port numbers
		}
		hosts = append(hosts, store.DiscoveredHost{Address: h.Address, Name: h.Name(), DNSName: h.DNSName, SysName: h.SysName, SysDescr: h.SysDescr,
			Vendor: h.Vendor, Kind: h.Kind(), Ports: ports, Ping: h.Ping, SNMP: h.SNMP})
	}
	return s.store.SaveDiscovery(ctx, r.ID, hosts, s.now())
}

// RunDiscovery scans the scheduled ranges when they are due (checked every
// ten minutes).
func (s *Server) RunDiscovery(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.ScanDue(ctx)
		}
	}
}

// ScanDue scans every range that is due, one after another.
func (s *Server) ScanDue(ctx context.Context) {
	rs, err := s.store.DiscoveryRanges(ctx)
	if err != nil {
		s.log.Error("discovery", "err", err)
		return
	}
	for _, r := range rs {
		if r.Due(s.now()) {
			if err := s.scanRange(ctx, r); err != nil {
				s.log.Error("discovery", "range", r.CIDR, "err", err)
			}
		}
	}
}

type discoveryData struct {
	Ranges    []*store.DiscoveryRange
	Hosts     []*store.DiscoveredHost
	Dismissed []*store.DiscoveredHost
	Busy      map[int64]bool
	Profiles  map[int64]string
	Groups    []store.Group
}

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := &discoveryData{Busy: s.scans.busy(), Profiles: map[int64]string{}}
	var err error
	if d.Ranges, err = s.store.DiscoveryRanges(ctx); err == nil {
		d.Hosts, err = s.store.DiscoveredHosts(ctx, false)
	}
	if err == nil {
		d.Dismissed, err = s.store.DiscoveredHosts(ctx, true)
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	ps, _ := s.store.SNMPProfiles(ctx)
	for _, p := range ps {
		d.Profiles[p.ID] = p.Name
	}
	d.Groups, _ = s.store.Groups(ctx)
	p := &Page{Title: "Discovery", Nav: "devices", Data: d}
	if len(d.Busy) > 0 {
		p.Refresh = 15
	}
	s.render(w, r, http.StatusOK, "discovery", p)
}

func (s *Server) handleDiscoveryRangeAdd(w http.ResponseWriter, r *http.Request) {
	const back = "/discovery"
	cidr := strings.TrimSpace(r.FormValue("cidr"))
	addrs, err := discovery.Range(cidr)
	if err != nil {
		s.redirectErr(w, r, back, sentence(err.Error()))
		return
	}
	if len(addrs) == 1 {
		cidr = addrs[0].String()
	} else if p, err := netip.ParsePrefix(cidr); err == nil {
		cidr = p.Masked().String()
	}
	id, err := s.store.CreateDiscoveryRange(r.Context(), cidr, r.FormValue("every"))
	if err != nil {
		s.redirectErr(w, r, back, storeErr(err))
		return
	}
	s.audit(r, "discovery.range", "discovery:"+strconv.FormatInt(id, 10), "range %s added (%q)", cidr, r.FormValue("every"))
	if r.FormValue("now") == "1" {
		s.startScan(id)
		s.redirect(w, r, back, "The scan runs; this page shows the hosts it finds.")
		return
	}
	s.redirect(w, r, back, "Saved.")
}

// startScan scans a range in the background.
func (s *Server) startScan(id int64) {
	go func() { // #nosec G118 -- the scan outlives the request; own timeout
		ctx := context.Background()
		rs, err := s.store.DiscoveryRanges(ctx)
		if err != nil {
			return
		}
		for _, x := range rs {
			if x.ID == id {
				if err := s.scanRange(ctx, x); err != nil {
					s.log.Error("discovery", "range", x.CIDR, "err", err)
				}
			}
		}
	}()
}

func (s *Server) handleDiscoveryRange(w http.ResponseWriter, r *http.Request) {
	const back = "/discovery"
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	switch r.FormValue("do") {
	case "scan":
		s.startScan(id)
		s.audit(r, "discovery.scan", "discovery:"+strconv.FormatInt(id, 10), "scan started")
		s.redirect(w, r, back, "The scan runs; this page shows the hosts it finds.")
	case "every":
		if err := s.store.SetDiscoveryRange(r.Context(), id, r.FormValue("every")); err != nil {
			s.redirectErr(w, r, back, storeErr(err))
			return
		}
		s.redirect(w, r, back, "Saved.")
	case "delete":
		if err := s.store.DeleteDiscoveryRange(r.Context(), id); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.audit(r, "discovery.range.delete", "discovery:"+strconv.FormatInt(id, 10), "range deleted")
		s.redirect(w, r, back, "Deleted.")
	default:
		s.redirectErr(w, r, back, "Unknown action.")
	}
}

// handleDiscoveryHosts adds the chosen hosts as devices (with a ping check
// and, when one answered, the SNMP profile), or hides them.
func (s *Server) handleDiscoveryHosts(w http.ResponseWriter, r *http.Request) {
	const back = "/discovery"
	ctx := r.Context()
	_ = r.ParseForm()
	addrs := r.Form["addr"]
	if len(addrs) == 0 {
		s.redirectErr(w, r, back, "Choose at least one host.")
		return
	}
	switch r.FormValue("do") {
	case "dismiss", "restore":
		if err := s.store.DismissHosts(ctx, addrs, r.FormValue("do") == "dismiss"); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.redirect(w, r, back, "Saved.")
		return
	}
	hosts, err := s.store.DiscoveredHosts(ctx, false)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	group, _ := strconv.ParseInt(r.FormValue("group"), 10, 64)
	want := map[string]bool{}
	for _, a := range addrs {
		want[a] = true
	}
	added, failed := 0, []string{}
	for _, h := range hosts {
		if !want[h.Address.String()] {
			continue
		}
		in := store.DeviceInput{Name: h.Name, Address: h.Address.String(), Kind: h.Kind, GroupID: group, SNMP: h.SNMP}
		if in.Name == "" {
			in.Name = h.Address.String()
		}
		id, err := s.store.CreateDevice(ctx, in)
		if err == store.ErrConflict {
			in.Name += " (" + h.Address.String() + ")"
			id, err = s.store.CreateDevice(ctx, in)
		}
		if err != nil {
			failed = append(failed, h.Address.String()+": "+translate(s.lang(r, userFrom(r)), storeErr(err)))
			continue
		}
		if h.Ping {
			_, _ = s.store.CreateCheck(ctx, id, store.CheckInput{Type: store.CheckICMP})
		} else if len(h.Ports) > 0 {
			_, _ = s.store.CreateCheck(ctx, id, store.CheckInput{Type: store.CheckTCP, Params: store.CheckParams{Port: int(h.Ports[0])}})
		}
		added++
		s.audit(r, "device.create", "device:"+strconv.FormatInt(id, 10), "added %s (%s) from discovery", in.Name, in.Address)
	}
	s.poller.Reload()
	if len(failed) > 0 {
		s.redirectErr(w, r, back, strings.Join(failed, "; "))
		return
	}
	s.redirect(w, r, "/devices", translate(s.lang(r, userFrom(r)), "%d devices added. Devices that answered SNMP can get more checks on their page (SNMP discovery).", added))
}
