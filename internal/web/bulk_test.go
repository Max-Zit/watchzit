package web_test

import (
	"context"
	"strings"
	"testing"
)

// TestDevicesBulk: ticked devices change group, type, dependency and
// monitoring at once; a dependency loop is refused; only administrators
// delete in bulk.
func TestDevicesBulk(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	for _, n := range []string{"r1", "s1", "s2"} {
		c.post("/devices/new", "name", n, "address", "10.0.0."+string(rune('1'+len(n))), "ping", "1")
	}
	c.post("/groups", "name", "Branch")
	c.post("/devices/bulk", "action", "group", "group", "1", "id", "2", "id", "3")
	c.post("/devices/bulk", "action", "kind", "kind", "switch", "id", "2", "id", "3")
	c.post("/devices/bulk", "action", "parent", "parent", "1", "id", "2", "id", "3")
	for _, id := range []int64{2, 3} {
		d, _ := e.st.DeviceByID(ctx, id)
		if d.GroupID == nil || *d.GroupID != 1 || d.Kind != "switch" || d.Parent != 1 {
			t.Fatalf("device %d: %+v", id, d)
		}
	}
	// r1 depending on s1 would be a loop
	c.post("/devices/bulk", "action", "parent", "parent", "2", "id", "1")
	if d, _ := e.st.DeviceByID(ctx, 1); d.Parent != 0 {
		t.Fatal("loop accepted")
	}
	c.post("/devices/bulk", "action", "pause", "id", "3")
	if d, _ := e.st.DeviceByID(ctx, 3); !d.Paused || d.Status != "paused" {
		t.Fatalf("pause: %+v", d)
	}
	if !strings.Contains(c.get("/devices").body, `data-checkall="id"`) {
		t.Fatal("no ticks in the list")
	}
	c.post("/devices/bulk", "action", "delete", "id", "3")
	if _, err := e.st.DeviceByID(ctx, 3); err == nil {
		t.Fatal("not deleted")
	}
}
