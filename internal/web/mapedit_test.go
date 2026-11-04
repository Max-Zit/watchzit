package web_test

import (
	"strings"
	"testing"
)

// TestEditFromMap: the map side panel offers editing a device (for those
// who may change it); the form opened from a map saves back to the map.
func TestEditFromMap(t *testing.T) {
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "pve", "address", "10.0.0.6", "kind", "server")
	c.post("/maps", "name", "Net")
	if b := c.get("/maps/1/layout").body; !strings.Contains(b, `"can_edit":true`) || !strings.Contains(b, "Edit device") {
		t.Fatal("the map does not let a writer edit devices")
	}
	if b := c.get("/devices/1/edit?map=1").body; !strings.Contains(b, `action="/devices/1/edit?map=1"`) {
		t.Fatal("the form forgets the map")
	}
	if r := c.post("/devices/1/edit?map=1", "name", "pve2", "address", "10.0.0.6", "kind", "server"); r.code != 303 || r.loc != "/maps/1" {
		t.Fatalf("saved to %d %s", r.code, r.loc)
	}
	if r := c.post("/devices/1/edit?map=99", "name", "pve2", "address", "10.0.0.6", "kind", "server"); r.loc != "/devices/1" {
		t.Fatalf("a missing map: %s", r.loc)
	}
}
