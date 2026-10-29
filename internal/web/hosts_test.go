package web_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/alert"

	"github.com/max-zit/watchzit/internal/store"
)

// TestHypervisorHosts: a hypervisor host box with its physical server and
// the virtual machines (devices and items) running on it.
func TestHypervisorHosts(t *testing.T) {
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "esx01", "address", "10.0.0.5")
	c.post("/devices/new", "name", "dc01", "address", "10.0.0.6")
	c.post("/maps", "name", "Virtual")

	l := store.MapLayout{Nodes: []store.MapNode{
		{ID: -1, Kind: "host", Label: "esx01", Icon: "lib:hv-esxi", HostDevice: 1, X: 0, Y: 0},
		{ID: -2, Kind: "device", DeviceID: 2, Icon: "lib:vm-windows-server", Parent: -1, U: 1},
		{ID: -3, Kind: "item", Label: "web", Icon: "lib:vm-ubuntu", Parent: -1, U: 2},
	}}
	r := c.postJSON("/maps/1/layout", l)
	if r.code != http.StatusOK {
		t.Fatalf("save: %d %s", r.code, r.body)
	}
	var saved store.MapLayout
	_ = json.Unmarshal([]byte(r.body), &saved)
	if len(saved.Nodes) != 3 || saved.Nodes[0].HostDevice != 1 || saved.Nodes[1].Parent != saved.Nodes[0].ID || saved.Nodes[2].Parent != saved.Nodes[0].ID || saved.Nodes[2].U != 2 {
		t.Fatalf("saved: %+v", saved.Nodes)
	}
	if r := c.get("/maps/1/layout"); !strings.Contains(r.body, `"host_device":1`) || !strings.Contains(r.body, `"kind":"host"`) {
		t.Fatalf("layout: %s", r.body)
	}
	// a hypervisor needs its physical server
	noServer := store.MapLayout{Nodes: []store.MapNode{{ID: -1, Kind: "host", Label: "lonely", Icon: "lib:hv-proxmox"}}}
	if r := c.postJSON("/maps/1/layout", noServer); r.code != http.StatusBadRequest || !strings.Contains(r.body, "physical server") {
		t.Fatalf("hypervisor without server: %d %s", r.code, r.body)
	}
	// the live data knows the physical server's state
	r = c.get("/maps/1/live")
	var live store.MapLive
	_ = json.Unmarshal([]byte(r.body), &live)
	if live.Devices[1] == nil || live.Devices[2] == nil {
		t.Fatalf("live devices: %s", r.body)
	}
	// refused: a submap on a host; a rack is never put on one
	host := saved.Nodes[0].ID
	x := saved
	x.Nodes = append(append([]store.MapNode{}, saved.Nodes...), store.MapNode{ID: -9, Kind: "submap", SubmapID: 1, Parent: host})
	if r := c.postJSON("/maps/1/layout", x); r.code != http.StatusBadRequest {
		t.Fatalf("submap on a host accepted: %d %s", r.code, r.body)
	}
	x.Nodes[3] = store.MapNode{ID: -9, Kind: "rack", U: 10, Parent: host}
	r = c.postJSON("/maps/1/layout", x)
	_ = json.Unmarshal([]byte(r.body), &saved)
	if r.code != http.StatusOK || saved.Nodes[3].Parent != 0 {
		t.Fatalf("rack on a host: %d %s", r.code, r.body)
	}
	// the server deleted: the host stays, without it
	c.post("/devices/1/delete")
	if r := c.get("/maps/1/layout"); strings.Contains(r.body, `"host_device"`) || !strings.Contains(r.body, `"kind":"host"`) {
		t.Fatalf("after deleting the server: %s", r.body)
	}
}

// TestHAGroups: HA groups on a map (saved with their members, refused when
// wrong), hypervisor clusters, and the problem of a group that lost its
// redundancy.
func TestHAGroups(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "fw1", "address", "10.0.0.1", "ping", "1")
	c.post("/devices/new", "name", "fw2", "address", "10.0.0.2", "ping", "1")
	c.post("/devices/new", "name", "esx2", "address", "10.0.0.3")
	c.post("/maps", "name", "Edge")
	l := store.MapLayout{
		Nodes: []store.MapNode{{ID: -1, Kind: "device", DeviceID: 1}, {ID: -2, Kind: "device", DeviceID: 2, X: 100},
			{ID: -3, Kind: "host", Icon: "lib:hv-esxi", HostDevice: 3, HostDevices: []int64{1, 3, 1}, X: 300}},
		Links: []store.MapLink{{A: -1, B: -2, Kind: "ha"}},
		HA:    []store.MapHA{{ID: -1, Name: "fw-ha", Kind: "firewall", Nodes: []int64{-1, -2}}},
	}
	r := c.postJSON("/maps/1/layout", l)
	var saved store.MapLayout
	_ = json.Unmarshal([]byte(r.body), &saved)
	if r.code != http.StatusOK || len(saved.HA) != 1 || saved.HA[0].ID <= 0 || saved.HA[0].Nodes[0] != saved.Nodes[0].ID || saved.Links[0].Kind != "ha" ||
		fmt.Sprint(saved.Nodes[2].HostDevices) != "[1]" {
		t.Fatalf("save: %d %s", r.code, r.body)
	}
	if r := c.get("/maps/1/layout"); !strings.Contains(r.body, `"name":"fw-ha"`) || !strings.Contains(r.body, `"host_devices":[1]`) {
		t.Fatalf("layout: %s", r.body)
	}
	// refused: one member, an unknown kind, a device in two groups, a label
	for _, bad := range [][]store.MapHA{
		{{ID: -1, Kind: "firewall", Nodes: []int64{saved.Nodes[0].ID}}},
		{{ID: -1, Kind: "magic", Nodes: []int64{saved.Nodes[0].ID, saved.Nodes[1].ID}}},
		{{ID: -1, Kind: "firewall", Nodes: []int64{saved.Nodes[0].ID, saved.Nodes[1].ID}}, {ID: -2, Kind: "server", Nodes: []int64{saved.Nodes[0].ID, saved.Nodes[2].ID}}},
	} {
		x := saved
		x.HA = bad
		if r := c.postJSON("/maps/1/layout", x); r.code != http.StatusBadRequest {
			t.Fatalf("bad HA group %+v accepted: %d %s", bad, r.code, r.body)
		}
	}

	// fw2 down, fw1 up: the group lost its redundancy; both up again: resolved
	eng := alert.New(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	eng.SetClock(func() time.Time { return e.now })
	ping := func(dev int64) int64 { cs, _ := e.st.DeviceChecks(ctx, dev); return cs[0].ID }
	result := func(dev int64, status string) {
		if _, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: ping(dev), At: e.now, Status: status, Text: status}}); err != nil {
			t.Fatal(err)
		}
	}
	haOpen := func() *store.Problem {
		ps, _ := e.st.OpenProblems(ctx)
		for _, p := range ps {
			if strings.Contains(p.Key, ":ha") {
				return p
			}
		}
		return nil
	}
	result(1, store.StatusUp)
	result(2, store.StatusDown)
	_ = eng.Evaluate(ctx)
	e.now = e.now.Add(61 * time.Second)
	if err := eng.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	p := haOpen()
	if p == nil || p.DeviceID != 2 || !strings.Contains(p.Detail, "fw-ha: fw2 down, fw1 still up") || p.Title != "HA group without redundancy" {
		t.Fatalf("HA problem: %+v", p)
	}
	result(2, store.StatusUp)
	e.now = e.now.Add(time.Second)
	_ = eng.Evaluate(ctx)
	if haOpen() != nil {
		t.Fatal("HA problem not resolved")
	}
	// the HA role a device reports: on the map and its page
	if err := e.st.SetHAState(ctx, 1, store.HAMaster, "routeros"); err != nil {
		t.Fatal(err)
	}
	_ = e.st.SetHAState(ctx, 2, "nonsense", "x")
	if r := c.get("/maps/1/live"); !strings.Contains(r.body, `"ha":"master"`) || strings.Contains(r.body, "nonsense") {
		t.Fatalf("live HA role: %s", r.body)
	}
	if r := c.get("/devices/1"); !strings.Contains(r.body, "HA role") || !strings.Contains(r.body, "RouterOS VRRP") {
		t.Fatal("device page does not show the HA role")
	}
	if r := c.get("/devices/2"); strings.Contains(r.body, "HA role") {
		t.Fatal("a device without a role shows one")
	}
}
