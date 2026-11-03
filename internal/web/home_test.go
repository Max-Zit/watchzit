package web_test

import (
	"strings"
	"testing"
)

// TestStartPage: a user who picks a map as the start page lands on it
// from "/" (sign-in, the logo); the overview stays one click away, and a
// removed map falls back to the overview.
func TestStartPage(t *testing.T) {
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/maps", "name", "Net")
	if b := c.get("/account").body; !strings.Contains(b, `<option value="map:1"`) {
		t.Fatal("the map is not offered as the start page")
	}
	if r := c.post("/account/home", "home", "map:1"); r.code != 303 {
		t.Fatalf("save: %d", r.code)
	}
	if r := c.get("/"); r.code != 302 || r.loc != "/maps/1" {
		t.Fatalf("/ went to %d %s", r.code, r.loc)
	}
	if r := c.get("/overview"); r.code != 200 || !strings.Contains(r.body, `href="/overview"`) {
		t.Fatalf("overview: %d", r.code)
	}
	if b := c.get("/account").body; !strings.Contains(b, `value="map:1" selected`) {
		t.Fatal("the choice is not shown")
	}
	if r := c.post("/account/home", "home", "map:99"); r.code != 303 || c.get("/").code != 200 {
		t.Fatal("a missing map was kept")
	}
	if r := c.post("/account/home", "home", "/etc/x"); r.code == 303 && c.get("/").code != 200 {
		t.Fatal("a bad value was taken")
	}
	c.post("/account/home", "home", "map:1")
	c.post("/maps/1/delete")
	if r := c.get("/"); r.code != 200 {
		t.Fatalf("a removed map: %d %s", r.code, r.loc)
	}
}
