package web_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

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
