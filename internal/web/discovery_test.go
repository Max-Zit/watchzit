package web_test

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/discovery"
	"github.com/max-zit/watchzit/internal/store"
)

// TestDiscovery: a range is scanned, the hosts that answered and are not
// devices are listed with their names and kinds, added as devices with a
// check, or hidden; a weekly range is due again after a week.
func TestDiscovery(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "known", "address", "10.9.0.1", "ping", "1")
	e.srv.Prober = &discovery.Prober{
		Ping: func(_ context.Context, a netip.Addr) (time.Duration, bool) {
			last := a.As4()[3]
			return time.Millisecond, last == 1 || last == 5 || last == 9
		},
		Dial: func(_ context.Context, a netip.Addr, port int) bool {
			last := a.As4()[3]
			return last == 5 && port == 8291 || last == 7 && port == 3389 || last == 9 && port == 22
		},
		Identify: func(_ context.Context, a netip.Addr) (string, string, string, int64) {
			if a.As4()[3] == 5 {
				return "core-rtr", "RouterOS CCR2004", "MikroTik", 0
			}
			return "", "", "", 0
		},
		Lookup: func(_ context.Context, a netip.Addr) string {
			if a.As4()[3] == 9 {
				return "web01.lab.example."
			}
			return ""
		},
	}
	for _, bad := range []string{"10.0.0.0/16", "nope", "fd00::/120"} {
		if r := c.post("/discovery/ranges", "cidr", bad); !strings.Contains(c.get(r.loc).body, "flash err") {
			t.Fatalf("range %s accepted", bad)
		}
	}
	if r := c.post("/discovery/ranges", "cidr", "10.9.0.0/28", "every", "week", "now", "1"); r.code != 303 {
		t.Fatalf("add: %d", r.code)
	}
	deadline := time.Now().Add(10 * time.Second)
	var hosts []*store.DiscoveredHost
	for time.Now().Before(deadline) {
		if hosts, _ = e.st.DiscoveredHosts(ctx, false); len(hosts) == 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(hosts) != 3 {
		t.Fatalf("hosts: %+v", hosts)
	}
	byAddr := map[string]*store.DiscoveredHost{}
	for _, h := range hosts {
		byAddr[h.Address.String()] = h
	}
	if h := byAddr["10.9.0.5"]; h == nil || h.Name != "core-rtr" || h.Kind != "router" || h.Vendor != "MikroTik" {
		t.Fatalf("router: %+v", h)
	}
	if h := byAddr["10.9.0.7"]; h == nil || h.Ping || h.Kind != "pc" {
		t.Fatalf("pc without ping: %+v", h)
	}
	if h := byAddr["10.9.0.9"]; h == nil || h.Name != "web01.lab.example" || h.Kind != "server" {
		t.Fatalf("server: %+v", h)
	}
	r := c.get("/discovery")
	for _, want := range []string{"10.9.0.0/28", "core-rtr", "web01.lab.example", "8291"} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
	c.post("/discovery/hosts", "do", "dismiss", "addr", "10.9.0.7")
	if r := c.post("/discovery/hosts", "do", "add", "addr", "10.9.0.5", "addr", "10.9.0.9", "group", "0"); r.code != 303 || r.loc != "/devices" {
		t.Fatalf("add hosts: %d %s", r.code, r.loc)
	}
	devs, _ := e.st.Devices(ctx, store.DeviceFilter{})
	if len(devs) != 3 {
		t.Fatalf("devices: %d", len(devs))
	}
	for _, d := range devs {
		if d.Name == "core-rtr" {
			if cs, _ := e.st.DeviceChecks(ctx, d.ID); len(cs) != 1 || cs[0].Type != store.CheckICMP || d.Kind != "router" {
				t.Fatalf("router device: %+v %+v", d, cs)
			}
		}
	}
	if hosts, _ := e.st.DiscoveredHosts(ctx, false); len(hosts) != 0 {
		t.Fatalf("added or hidden hosts still listed: %d", len(hosts))
	}
	rs, _ := e.st.DiscoveryRanges(ctx)
	if len(rs) != 1 || rs[0].Due(e.now.Add(24*time.Hour)) || !rs[0].Due(e.now.Add(7*24*time.Hour)) {
		t.Fatalf("schedule: %+v", rs)
	}
}
