package web_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/max-zit/watchzit/internal/launch"
)

func TestOpenDevicesWithTools(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pub, key, _ := ed25519.GenerateKey(nil)
	e.srv.LauncherKey = key
	e.h = e.srv.Handler()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "core", "address", "10.0.0.1", "kind", "router", "access_user", "admin")
	c.post("/devices/new", "name", "pc1", "address", "10.0.0.20", "kind", "pc")
	ts, _ := e.st.Tools(ctx)
	id := map[string]string{}
	for _, x := range ts {
		id[x.Builtin] = itoa(int32(x.ID))
	}
	// a non-default WinBox port for the router
	if r := c.post("/devices/1/edit", "name", "core", "address", "10.0.0.1", "kind", "router", "access_user", "admin", "port_"+id["winbox"], "8292"); r.code != http.StatusSeeOther {
		t.Fatalf("edit: %d %s", r.code, r.body)
	}
	if r := c.post("/devices/1/edit", "name", "core", "address", "10.0.0.1", "kind", "router", "access_user", "-oProxyCommand=x"); r.code != http.StatusBadRequest {
		t.Fatalf("bad user accepted: %d", r.code)
	}

	// the router offers SSH and WinBox, the PC Remote Desktop
	r := c.get("/devices/1")
	if !strings.Contains(r.body, "/devices/1/open/"+id["ssh"]) || !strings.Contains(r.body, "/devices/1/open/"+id["winbox"]) || strings.Contains(r.body, "/open/"+id["rdp"]) {
		t.Fatal("router tools")
	}
	if r := c.get("/devices/2"); !strings.Contains(r.body, "/devices/2/open/"+id["rdp"]) || !strings.Contains(r.body, "/devices/2/rdp") {
		t.Fatal("pc tools")
	}

	// a click gives a signed link the launcher accepts
	r = c.do(http.MethodGet, "/devices/1/open/"+id["winbox"], nil, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if r.code != http.StatusFound || !strings.HasPrefix(r.loc, "watchzit://open?") {
		t.Fatalf("open: %d %s", r.code, r.loc)
	}
	l, err := launch.Parse(r.loc)
	if err != nil {
		t.Fatal(err)
	}
	p, err := l.Verify(pub, e.now)
	if err != nil || p.Tool != launch.ToolWinBox || p.Host != "10.0.0.1" || p.Port != 8292 || p.User != "admin" || p.Device != "core" {
		t.Fatalf("payload %v %+v", err, p)
	}
	// another site cannot make the browser open a program
	if r := c.do(http.MethodGet, "/devices/1/open/"+id["ssh"], nil, map[string]string{"Sec-Fetch-Site": "cross-site"}); r.code != http.StatusForbidden {
		t.Fatalf("cross-site open: %d", r.code)
	}
	// a switched-off tool is not offered and cannot be opened
	if r := c.post("/settings/tools/"+id["ssh"], "name", "SSH", "port", "22", "kind", "router"); r.code != http.StatusSeeOther {
		t.Fatalf("switch off: %d %s", r.code, r.body)
	}
	if r := c.get("/devices/1/open/" + id["ssh"]); r.code != http.StatusNotFound {
		t.Fatalf("disabled tool: %d", r.code)
	}

	// Remote Desktop file without the launcher
	r = c.get("/devices/2/rdp")
	if r.code != http.StatusOK || !strings.Contains(r.body, "full address:s:10.0.0.20:3389") || !strings.Contains(r.hdr.Get("Content-Disposition"), "pc1.rdp") {
		t.Fatalf("rdp: %d %q", r.code, r.body)
	}

	// own tools: a bad program or placeholder is refused
	if r := c.post("/settings/tools", "name", "Calc", "program", "calc", "port", "1", "enabled", "1"); r.code != http.StatusBadRequest {
		t.Fatalf("bare program accepted: %d", r.code)
	}
	if r := c.post("/settings/tools", "name", "X", "program", `C:\x\x.exe`, "args", "{password}", "port", "1", "enabled", "1"); r.code != http.StatusBadRequest {
		t.Fatalf("unknown placeholder accepted: %d", r.code)
	}
	if r := c.post("/settings/tools", "name", "Browser", "program", `%ProgramFiles%\Mozilla Firefox\firefox.exe`, "args", `"https://{host}:{port}/"`, "port", "443", "kind", "router", "enabled", "1"); r.code != http.StatusSeeOther {
		t.Fatalf("custom tool: %d %s", r.code, r.body)
	}
	ts, _ = e.st.Tools(ctx)
	custom := ts[len(ts)-1]
	r = c.get("/devices/1/open/" + itoa(int32(custom.ID)))
	l, _ = launch.Parse(r.loc)
	if p, err := l.Verify(pub, e.now); err != nil || p.Tool != launch.ToolCustom || strings.Join(p.Expand(), " ") != "https://10.0.0.1:443/" {
		t.Fatalf("custom link: %v %+v", err, p)
	}
	if r := c.post("/settings/tools/" + id["rdp"] + "/delete"); !strings.Contains(c.get(r.loc).body, "only be switched off") {
		t.Fatal("a built-in tool was deleted")
	}

	// the map's side panel gets the tools, kiosks do not
	c.post("/maps", "name", "M")
	c.postJSON("/maps/1/layout", map[string]any{"nodes": []map[string]any{{"id": -1, "kind": "device", "device": 1, "x": 0, "y": 0}}, "links": []any{}})
	if r := c.get("/maps/1/live"); !strings.Contains(r.body, `"tools":[`) || !strings.Contains(r.body, `"name":"Browser"`) {
		t.Fatalf("map live: %s", r.body)
	}

	// the launcher page pairs with this server's key
	r = c.get("/launcher")
	m := regexp.MustCompile(`href="(watchzit://pair\?[^"]+)"`).FindStringSubmatch(r.body)
	if m == nil || !strings.Contains(r.body, launch.Fingerprint(pub)) {
		t.Fatal("launcher page")
	}
	pl, err := launch.Parse(strings.ReplaceAll(m[1], "&amp;", "&"))
	if err != nil || !pl.Key.Equal(pub) {
		t.Fatalf("pair link: %v", err)
	}
}
