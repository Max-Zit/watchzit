package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/max-zit/watchzit/internal/store"
)

// TestMapParents: following the cables from the router, the switch depends
// on the router and the access points on the switch; a power cable does
// not count; a device that already has its dependency is left alone, and
// the ticked ones are set.
func TestMapParents(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	for _, d := range [][3]string{{"core", "10.0.0.1", "router"}, {"sw1", "10.0.0.2", "switch"}, {"ap1", "10.0.0.3", "ap"}, {"ap2", "10.0.0.4", "ap"}, {"ups", "10.0.0.5", "ups"}} {
		c.post("/devices/new", "name", d[0], "address", d[1], "kind", d[2])
	}
	c.post("/maps", "name", "Net")
	link := func(id, a, b int64, kind string) store.MapLink { return store.MapLink{ID: id, A: a, B: b, Kind: kind} }
	r := c.postJSON("/maps/1/layout", store.MapLayout{
		Nodes: []store.MapNode{{ID: -1, Kind: "device", DeviceID: 1}, {ID: -2, Kind: "device", DeviceID: 2, X: 200}, {ID: -3, Kind: "device", DeviceID: 3, X: 400},
			{ID: -4, Kind: "device", DeviceID: 4, X: 600}, {ID: -5, Kind: "device", DeviceID: 5, X: 800}},
		Links: []store.MapLink{link(-1, -1, -2, "ethernet"), link(-2, -2, -3, "ethernet"), link(-3, -2, -4, "fiber"), link(-4, -5, -3, "power")},
	})
	if r.code != 200 {
		t.Fatalf("layout: %d %s", r.code, r.body)
	}
	// ap2 depends on core already (wrong): offered, but not ticked
	c.post("/devices/4/edit", "name", "ap2", "address", "10.0.0.4", "kind", "ap", "parent", "1")
	b := c.get("/maps/1/parents").body
	if !strings.Contains(b, `<option value="1" selected>core</option>`) {
		t.Fatal("the router is not the root")
	}
	for _, want := range []string{`name="set" value="2" checked`, `name="set" value="3" checked`, `name="set" value="4"`} {
		if !strings.Contains(b, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(b, `value="4" checked`) {
		t.Fatal("a device with another dependency was ticked")
	}
	if strings.Contains(b, `value="5" checked`) {
		t.Fatal("the UPS (only a power cable) got a dependency")
	}
	if r := c.post("/maps/1/parents?root=1", "set", "2", "set", "3", "set", "4"); r.code != 303 {
		t.Fatalf("apply: %d", r.code)
	}
	want := map[int64]int64{1: 0, 2: 1, 3: 2, 4: 2, 5: 0}
	for id, p := range want {
		d, _ := e.st.DeviceByID(ctx, id)
		if d.Parent != p {
			t.Fatalf("device %d depends on %d, want %d", id, d.Parent, p)
		}
	}
	if b := c.get("/maps/1/parents").body; !strings.Contains(b, "Nothing to change") {
		t.Fatal("suggested again after applying")
	}
	// another root turns the tree around
	if b := c.get("/maps/1/parents?root=3").body; !strings.Contains(b, `name="set" value="2"`) || !strings.Contains(b, `name="set" value="1"`) {
		t.Fatal("root ap1")
	}
	if r := c.get("/maps/1"); !strings.Contains(r.body, `href="/maps/1/parents"`) {
		t.Fatal("no link on the map")
	}
}
