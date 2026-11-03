package web_test

import (
	"strings"
	"testing"
)

// TestCustomFields: administrators define fields, operators fill them in on
// the device page, values are checked, and the device search finds them.
func TestCustomFields(t *testing.T) {
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "core-sw", "address", "10.0.0.1")
	c.post("/devices/new", "name", "edge", "address", "10.0.0.2")
	c.post("/settings/fields", "action", "add", "name", "Asset number", "kind", "text")
	c.post("/settings/fields", "action", "add", "name", "Warranty end", "kind", "date")
	if r := c.post("/settings/fields", "action", "add", "name", "asset NUMBER", "kind", "text"); !strings.Contains(c.get("/settings/fields").body, "A field with this name exists") && !strings.Contains(r.body, "exists") {
		t.Fatal("a duplicate field name was accepted")
	}
	page := c.get("/devices/1").body
	if !strings.Contains(page, `name="f1"`) || !strings.Contains(page, `type="date"`) {
		t.Fatal("no field form on the device page")
	}
	c.post("/devices/1/fields", "f1", "INV-4711", "f2", "31.12.2027")
	if b := c.get("/devices/1").body; strings.Contains(b, "INV-4711") {
		t.Fatal("saved although the date was wrong")
	}
	c.post("/devices/1/fields", "f1", "INV-4711", "f2", "2027-12-31")
	b := c.get("/devices/1").body
	if !strings.Contains(b, "INV-4711") || !strings.Contains(b, "2027-12-31") {
		t.Fatal("values not shown")
	}
	list := c.get("/devices?q=inv-47").body
	if !strings.Contains(list, "core-sw") || strings.Contains(list, `">edge</a>`) {
		t.Fatal("search by a field value")
	}
	c.post("/settings/fields", "action", "delete", "id", "1")
	if strings.Contains(c.get("/devices/1").body, "INV-4711") {
		t.Fatal("values stayed after the field was removed")
	}
}
