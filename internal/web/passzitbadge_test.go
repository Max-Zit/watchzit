package web_test

import (
	"strings"
	"testing"
)

// TestPassZitMarks: every console page says it is a MaxZit console and
// carries the badge texts; a device page with tools keeps the device meta,
// the data-open-* attributes and gets a badge place next to Open with.
func TestPassZitMarks(t *testing.T) {
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "pve", "address", "10.0.0.5", "kind", "server")
	if b := c.get("/").body; !strings.Contains(b, `<meta name="maxzit-console" content="watchzit">`) || !strings.Contains(b, `id="pz-texts"`) || !strings.Contains(b, `data-locked="PassZit locked"`) {
		t.Fatal("console marks missing")
	}
	b := c.get("/devices/1").body
	for _, want := range []string{`<meta name="passzit-device" content="10.0.0.5">`, `data-open-tool="ssh"`, `data-pz-badge`} {
		if !strings.Contains(b, want) {
			t.Fatalf("device page lacks %s", want)
		}
	}
}
