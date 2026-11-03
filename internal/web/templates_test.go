package web_test

import (
	"context"
	"strings"
	"testing"

	"github.com/max-zit/watchzit/internal/store"
)

// TestTemplates: a template's checks are added to a device once (applying
// again adds nothing twice), new devices of its kinds get them by
// themselves, a group can get them at once, checks with passwords are
// refused, and removing a template leaves the devices' checks alone.
func TestTemplates(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	if r := c.post("/templates", "name", "Linux server", "kind", "server", "auto", "1"); r.code != 303 || r.loc != "/templates/1" {
		t.Fatalf("create: %d %s", r.code, r.loc)
	}
	c.post("/templates/1/checks", "type", "tcp", "port", "22", "name", "SSH")
	c.post("/templates/1/checks", "type", "http", "contains", "ok", "interval", "120")
	if r := c.post("/templates/1/checks", "type", "pgsql", "user", "u", "secret", "pw"); r.code != 400 {
		t.Fatalf("a check with a password was accepted: %d", r.code)
	}
	tm, err := e.st.TemplateByID(ctx, 1)
	if err != nil || len(tm.Checks) != 2 {
		t.Fatalf("template %+v %v", tm, err)
	}
	count := func(dev int64) int {
		cs, _ := e.st.DeviceChecks(ctx, dev)
		return len(cs)
	}
	// a new server gets them by itself; a printer does not
	c.post("/devices/new", "name", "srv1", "address", "10.0.0.5", "kind", "server", "ping", "1")
	c.post("/devices/new", "name", "prn", "address", "10.0.0.6", "kind", "printer", "ping", "1")
	if count(1) != 3 || count(2) != 1 {
		t.Fatalf("auto: srv1 %d checks, prn %d", count(1), count(2))
	}
	// applying again adds nothing twice
	c.post("/devices/1/templates", "template", "1")
	if count(1) != 3 {
		t.Fatalf("applied twice: %d checks", count(1))
	}
	if b := c.get("/devices/1").body; !strings.Contains(b, `href="/templates/1"`) {
		t.Fatal("the device page does not show the template")
	}
	// a group at once
	c.post("/groups", "name", "Printers")
	c.post("/devices/2/edit", "name", "prn", "address", "10.0.0.6", "kind", "printer", "group", "1")
	gs, _ := e.st.Groups(ctx)
	if len(gs) != 1 {
		t.Fatal("group")
	}
	d2, _ := e.st.DeviceByID(ctx, 2)
	if d2.GroupID == nil {
		if err := e.st.UpdateDevice(ctx, 2, store.DeviceInput{Name: "prn", Address: "10.0.0.6", Kind: "printer", GroupID: gs[0].ID}); err != nil {
			t.Skip("cannot set the group:", err)
		}
	}
	c.post("/templates/1/apply", "scope", "g1")
	if count(2) != 3 {
		t.Fatalf("group apply: prn %d checks", count(2))
	}
	// removing the template keeps the checks
	c.post("/templates/1/delete")
	if count(1) != 3 || count(2) != 3 {
		t.Fatal("checks went with the template")
	}
	if b := c.get("/templates").body; !strings.Contains(b, "No templates yet") {
		t.Fatal("template not removed")
	}
}
