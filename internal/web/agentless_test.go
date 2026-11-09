package web_test

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestAgentlessChecks: Linux (SSH) and Windows (WinRM) checks are set up
// from the device page; a saved host key stays when the form leaves it
// empty and goes only when asked; the agent's disk trigger watches them.
func TestAgentlessChecks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "web01", "address", "10.0.0.5")
	c.post("/devices/new", "name", "dc01", "address", "10.0.0.6")

	if r := c.post("/devices/1/checks", "type", "ssh_host", "user", "monitor", "secret", "pw", "host_key", "MD5:aa"); r.code != http.StatusBadRequest || !strings.Contains(r.body, "SHA256") {
		t.Fatalf("bad host key: %d", r.code)
	}
	key := "-----BEGIN OPENSSH PRIVATE KEY-----\r\nb3BlbnNzaC1rZXktdjEAAAAA\r\n-----END OPENSSH PRIVATE KEY-----"
	if r := c.post("/devices/1/checks", "type", "ssh_host", "user", "monitor", "secret_key", key); r.code != http.StatusSeeOther {
		t.Fatalf("add ssh: %d %s", r.code, r.body)
	}
	if r := c.post("/devices/2/checks", "type", "winrm", "user", `LAB\monitor`, "secret", "pw", "insecure", "1"); r.code != http.StatusSeeOther {
		t.Fatalf("add winrm: %d %s", r.code, r.body)
	}
	if r := c.post("/devices/2/checks", "type", "winrm", "user", `a\b\c`, "secret", "pw"); r.code != http.StatusBadRequest {
		t.Fatalf("bad user accepted: %d", r.code)
	}
	ssh, _ := e.st.DeviceChecks(ctx, 1)
	win, _ := e.st.DeviceChecks(ctx, 2)
	if len(ssh) != 1 || ssh[0].Name != "Linux (SSH)" || ssh[0].Params.Port != 22 || !strings.HasPrefix(ssh[0].Params.Secret, "v1:") {
		t.Fatalf("ssh check: %+v", ssh)
	}
	if sec, err := e.st.CheckSecret(ssh[0]); err != nil || !strings.HasPrefix(sec, "-----BEGIN") || strings.Contains(sec, "\r") {
		t.Fatalf("the key was not saved as the secret: %v", err)
	}
	if len(win) != 1 || win[0].Name != "Windows (WinRM)" || win[0].Params.Port != 5985 || win[0].Params.Insecure {
		t.Fatalf("winrm check: %+v", win)
	}

	// the poller saved the key on first use
	fp := "SHA256:" + strings.Repeat("x", 43)
	p := ssh[0].Params
	p.HostKey = fp
	if err := e.st.UpdateCheckParams(ctx, ssh[0].ID, p); err != nil {
		t.Fatal(err)
	}
	id := strconv.FormatInt(ssh[0].ID, 10)
	if r := c.get("/devices/1"); !strings.Contains(r.body, fp) {
		t.Fatal("the device page does not show the host key")
	}
	c.post("/checks/"+id, "name", "web01", "user", "monitor", "port", "22", "host_key", "", "interval", "60", "timeout_ms", "5000")
	if cs, _ := e.st.DeviceChecks(ctx, 1); cs[0].Params.HostKey != fp || !strings.HasPrefix(cs[0].Params.Secret, "v1:") {
		t.Fatalf("an empty field dropped the key or the secret: %+v", cs[0].Params)
	}
	c.post("/checks/"+id, "name", "web01", "user", "monitor", "port", "22", "host_key", "", "new_key", "1", "interval", "60", "timeout_ms", "5000")
	if cs, _ := e.st.DeviceChecks(ctx, 1); cs[0].Params.HostKey != "" {
		t.Fatal("the key was not forgotten")
	}

	ts, _ := e.st.Triggers(ctx)
	var disk bool
	for _, tr := range ts {
		if tr.Name == "Disk almost full" {
			disk = slices.Contains(tr.CheckTypes, "ssh_host") && slices.Contains(tr.CheckTypes, "winrm")
		}
	}
	if !disk {
		t.Error("the disk trigger does not watch machines without an agent")
	}
}
