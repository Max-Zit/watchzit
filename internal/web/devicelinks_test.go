package web_test

import (
	"strings"
	"testing"
)

// TestDeviceLinks: links an administrator sets under Tools appear on every
// device page with the device filled in; other schemes are refused.
func TestDeviceLinks(t *testing.T) {
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "file server", "address", "10.0.0.7")
	c.post("/settings/device-links", "link_name", "RMMZit", "link_url", "https://rmm.example.com:8443/devices?q={name}", "link_name", "BackupZit", "link_url", "https://bz.example.com/agents?a={address}&d={id}")
	b := c.get("/devices/1").body
	if !strings.Contains(b, `href="https://rmm.example.com:8443/devices?q=file&#43;server"`) || !strings.Contains(b, `href="https://bz.example.com/agents?a=10.0.0.7&amp;d=1"`) {
		t.Fatal("links not on the device page")
	}
	c.post("/settings/device-links", "link_name", "Evil", "link_url", "javascript:alert(1)//{name}")
	if b := c.get("/devices/1").body; strings.Contains(b, "javascript:") || !strings.Contains(b, "rmm.example.com") {
		t.Fatal("a javascript link was saved or the old links were lost")
	}
	if b := c.get("/settings/tools").body; !strings.Contains(b, `value="https://rmm.example.com:8443/devices?q={name}"`) {
		t.Fatal("the tools page does not show the saved links")
	}
}
