package web_test

import (
	"context"
	"strings"
	"testing"
)

// TestDeviceToolChoice: a device chooses which tools Open with offers — a
// Proxmox server without Remote Desktop, a router with RDP added by hand —
// and an edit without the ticks keeps the choice.
func TestDeviceToolChoice(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "pve", "address", "10.0.0.6", "kind", "server")
	ts, _ := e.st.Tools(ctx)
	id := map[string]string{}
	for _, x := range ts {
		id[x.Builtin] = itoa(int32(x.ID))
	}
	if b := c.get("/devices/1").body; !strings.Contains(b, "/open/"+id["rdp"]) {
		t.Skip("servers do not get Remote Desktop by default here")
	}
	if b := c.get("/devices/1/edit").body; !strings.Contains(b, `name="tool_`+id["rdp"]+`" value="1"`) {
		t.Fatal("no tool ticks in the form")
	}
	// untick RDP, keep SSH
	if r := c.post("/devices/1/edit", "name", "pve", "address", "10.0.0.6", "kind", "server", "tools_set", "1", "tool_"+id["ssh"], "1"); r.code != 303 {
		t.Fatalf("edit: %d", r.code)
	}
	b := c.get("/devices/1").body
	if strings.Contains(b, "/open/"+id["rdp"]) || strings.Contains(b, "/devices/1/rdp") || !strings.Contains(b, "/open/"+id["ssh"]) {
		t.Fatal("RDP still offered or SSH gone")
	}
	if a, _ := e.st.DeviceAccess(ctx, 1); len(a.Shown) != 1 {
		t.Fatalf("stored %+v (only the difference is kept)", a.Shown)
	}
	// an edit without the ticks (another form, the API) keeps the choice
	c.post("/devices/1/edit", "name", "pve", "address", "10.0.0.6", "kind", "server")
	if strings.Contains(c.get("/devices/1").body, "/open/"+id["rdp"]) {
		t.Fatal("the choice was lost")
	}
	// the edit form shows it unticked
	if b := c.get("/devices/1/edit").body; strings.Contains(b, `name="tool_`+id["rdp"]+`" value="1" data-tool-kinds="`) && strings.Contains(b, `name="tool_`+id["rdp"]+`" value="1" data-tool-kinds="pc server" checked`) {
		t.Fatal("RDP ticked in the form")
	}
	// a router gets RDP by hand
	c.post("/devices/new", "name", "gw", "address", "10.0.0.1", "kind", "router", "tools_set", "1", "tool_"+id["rdp"], "1")
	if !strings.Contains(c.get("/devices/2").body, "/open/"+id["rdp"]) {
		t.Fatal("RDP not offered on the router")
	}
}
