package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/max-zit/watchzit/internal/store"
)

// upload posts a picture like map.js does.
func (c *client) upload(kind, name string, data []byte) resp {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("kind", kind)
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write(data)
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "http://wz.test/assets", &body)
	r.RemoteAddr = c.ip + ":50000"
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Origin", "http://wz.test")
	for k, v := range c.cookies {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	c.e.h.ServeHTTP(w, r)
	return resp{code: w.Code, body: w.Body.String(), hdr: w.Header()}
}

func pngOf(w, h int) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

// TestMapDesign: pictures, racks, ports on pictures, bends, cable kinds,
// background and their validation.
func TestMapDesign(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "sw1", "address", "10.0.0.2")
	c.post("/devices/new", "name", "srv1", "address", "10.0.0.3")
	c.post("/maps", "name", "DC")

	// pictures: PNG and SVG accepted, anything else refused; SVG sandboxed
	r := c.upload("background", "plan.png", pngOf(300, 200))
	if r.code != http.StatusOK || !strings.Contains(r.body, `"w":300`) {
		t.Fatalf("png: %d %s", r.code, r.body)
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 40"><script>alert(1)</script></svg>`
	r = c.upload("icon", "mine.svg", []byte(svg))
	if r.code != http.StatusOK || !strings.Contains(r.body, `"mime":"image/svg+xml"`) || !strings.Contains(r.body, `"w":120`) {
		t.Fatalf("svg: %d %s", r.code, r.body)
	}
	if r := c.upload("icon", "x.exe", []byte("MZ\x90\x00 not a picture")); r.code != http.StatusBadRequest {
		t.Fatalf("exe accepted: %d", r.code)
	}
	if r := c.upload("other", "a.png", pngOf(2, 2)); r.code != http.StatusBadRequest {
		t.Fatalf("unknown kind accepted: %d", r.code)
	}
	r = c.get("/assets/2")
	if r.code != http.StatusOK || r.hdr.Get("Content-Type") != "image/svg+xml" || !strings.Contains(r.hdr.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("asset headers: %d %v", r.code, r.hdr)
	}

	layout := store.MapLayout{
		Nodes: []store.MapNode{
			{ID: -1, Kind: "rack", Label: "R1", U: 12},
			{ID: -2, Kind: "device", DeviceID: 1, Icon: "lib:switch-24", Parent: -1, U: 10},
			{ID: -3, Kind: "device", DeviceID: 2, Icon: "asset:2", X: 400},
			{ID: -4, Kind: "item", Icon: "lib:patch-24", Parent: -1, U: 12},
			{ID: -5, Kind: "item", Icon: "lib:internet", Label: "ISP", X: -300},
		},
		Links: []store.MapLink{
			{ID: -1, A: -4, B: -2, ASlot: 3, BSlot: 3, Kind: "ethernet"},
			{ID: -2, A: -2, B: -3, ASlot: 25, Kind: "fiber-sm", Route: "ortho", Points: [][2]float64{{200, 0}, {200, 100}}},
			{ID: -3, A: -5, B: -4, Kind: "wan", Route: "diagonal"},
		},
		Background: &store.MapBackground{Asset: 1, X: -500, Y: -300, W: 1600, Opacity: 0.6},
	}
	r = c.postJSON("/maps/1/layout", layout)
	if r.code != http.StatusOK {
		t.Fatalf("save: %d %s", r.code, r.body)
	}
	l, err := e.st.MapLayout(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Nodes) != 5 || len(l.Links) != 3 || l.Background.Asset != 1 || l.Background.W != 1600 {
		t.Fatalf("%+v", l)
	}
	rack := l.Nodes[0].ID
	if l.Nodes[1].Parent != rack || l.Nodes[1].U != 10 || l.Nodes[3].Parent != rack || l.Nodes[1].Icon != "lib:switch-24" {
		t.Fatalf("mounted: %+v", l.Nodes)
	}
	if k := l.Links[1]; k.Kind != "fiber-sm" || k.Route != "ortho" || len(k.Points) != 2 || k.ASlot != 25 {
		t.Fatalf("link: %+v", k)
	}
	// a picture in use cannot be deleted; an unused one can
	if r := c.post("/assets/2/delete"); r.code != http.StatusConflict {
		t.Fatalf("delete used picture: %d", r.code)
	}
	r3 := c.upload("icon", "spare.png", pngOf(4, 4))
	var spare store.Asset
	_ = json.Unmarshal([]byte(r3.body), &spare)
	if r := c.post("/assets/" + strconv.FormatInt(spare.ID, 10) + "/delete"); r.code != http.StatusOK {
		t.Fatalf("delete unused picture: %d %s", r.code, r.body)
	}
	// broken designs are refused
	for name, bad := range map[string]store.MapLayout{
		"mounted without rack": {Nodes: []store.MapNode{{ID: -1, Kind: "item", Icon: "lib:pdu", Parent: -9, U: 1}}},
		"rack too tall":        {Nodes: []store.MapNode{{ID: -1, Kind: "rack", U: 99}}},
		"bad picture":          {Nodes: []store.MapNode{{ID: -1, Kind: "item", Icon: "https://evil.example/x.svg"}}},
		"parent not a rack":    {Nodes: []store.MapNode{{ID: -1, Kind: "item"}, {ID: -2, Kind: "item", Parent: -1, U: 1}}},
		"unknown background":   {Background: &store.MapBackground{Asset: 999, W: 100, Opacity: 1}},
	} {
		if r := c.postJSON("/maps/1/layout", bad); r.code != http.StatusBadRequest {
			t.Errorf("%s: accepted (%d)", name, r.code)
		}
	}
	// the layout JSON tells map.js the picture sizes
	r = c.get("/maps/1/layout")
	if !strings.Contains(r.body, `"assets":{"1":`) || !strings.Contains(r.body, `"cable:fiber-sm":"Fibre, single mode"`) {
		t.Fatalf("layout json: %s", r.body)
	}
	// deleting the rack deletes what is mounted in it (cascade), not the devices
	l.Nodes = []store.MapNode{l.Nodes[2], l.Nodes[4]}
	l.Links = nil
	if r := c.postJSON("/maps/1/layout", l); r.code != http.StatusOK {
		t.Fatalf("remove rack: %d %s", r.code, r.body)
	}
	if d, err := e.st.DeviceByID(ctx, 1); err != nil || d.Name != "sw1" {
		t.Fatal("device gone with its rack", err)
	}
}

// TestKiosk: a display opens through its secret link without signing in,
// shows only its maps and stops working when deleted.
func TestKiosk(t *testing.T) {
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/maps", "name", "Shown")
	c.post("/maps", "name", "Hidden")
	r := c.post("/settings/kiosks", "name", "NOC TV", "map", "1", "rotate", "20", "theme", "dark")
	m := regexp.MustCompile(`/kiosk/([A-Za-z0-9_-]{20,})`).FindStringSubmatch(r.body)
	if r.code != http.StatusOK || m == nil {
		t.Fatalf("create: %d %s", r.code, r.body)
	}
	token := m[1]
	if r := c.post("/settings/kiosks", "name", "none", "rotate", "20"); r.code != http.StatusBadRequest {
		t.Fatalf("kiosk without maps: %d", r.code)
	}
	tv := e.client("192.168.1.50") // no session
	if r := tv.get("/kiosk/" + token); r.code != http.StatusOK || !strings.Contains(r.body, `data-maps="1"`) || !strings.Contains(r.body, "map.js") {
		t.Fatalf("kiosk page: %d %s", r.code, r.body)
	}
	if r := tv.get("/kiosk/" + token + "/layout/1"); r.code != http.StatusOK || !strings.Contains(r.body, `"can_edit":false`) {
		t.Fatalf("kiosk layout: %d %s", r.code, r.body)
	}
	if r := tv.get("/kiosk/" + token + "/live/1"); r.code != http.StatusOK {
		t.Fatalf("kiosk live: %d", r.code)
	}
	if r := tv.get("/kiosk/" + token + "/layout/2"); r.code != http.StatusNotFound {
		t.Fatalf("map not on the display: %d", r.code)
	}
	if r := tv.get("/maps/1/layout"); r.code == http.StatusOK {
		t.Fatal("console open without signing in")
	}
	if r := tv.get("/kiosk/" + strings.Repeat("A", 32)); r.code != http.StatusNotFound {
		t.Fatalf("wrong token: %d", r.code)
	}
	if r := c.get("/settings/kiosks"); !strings.Contains(r.body, "NOC TV") || strings.Contains(r.body, token) {
		t.Fatal("the link must be shown only once")
	}
	if r := c.post("/settings/kiosks/1/delete"); r.code != http.StatusSeeOther {
		t.Fatalf("delete: %d", r.code)
	}
	if r := tv.get("/kiosk/" + token); r.code != http.StatusNotFound {
		t.Fatalf("deleted display still works: %d", r.code)
	}
}

// The picture library: every entry of the index has its drawing, the
// catalog models are there with their vendor, ports and rack units.
func TestDeviceLibrary(t *testing.T) {
	b, err := os.ReadFile("static/devices/index.json")
	if err != nil {
		t.Fatal(err)
	}
	var icons []struct {
		Name, Label, Vendor string
		U                   int
		Ports               [][2]float64
	}
	if err := json.Unmarshal(b, &icons); err != nil {
		t.Fatal(err)
	}
	byName := map[string]int{}
	vendors := map[string]int{}
	for i, ic := range icons {
		if _, err := os.Stat("static/devices/" + ic.Name + ".svg"); err != nil {
			t.Errorf("%s: %v", ic.Name, err)
		}
		byName[ic.Name] = i
		vendors[ic.Vendor]++
	}
	if len(icons) < 500 || vendors["Fortinet"] < 60 || vendors["HPE"] < 80 || vendors["MikroTik"] < 80 || vendors["Dell"] < 80 {
		t.Fatalf("%d pictures, by vendor %v", len(icons), vendors)
	}
	for name, want := range map[string]struct{ ports, u int }{
		"fortinet-fortigate-100f": {28, 1}, "mikrotik-crs354-48g-4splus2qplusrm": {54, 1},
		"hpe-proliant-dl380-gen10": {5, 2}, "dell-poweredge-r940": {5, 3},
	} {
		i, ok := byName[name]
		if !ok {
			t.Fatalf("%s missing", name)
		}
		if ic := icons[i]; len(ic.Ports) != want.ports || ic.U != want.u {
			t.Errorf("%s: %d ports, %dU", name, len(ic.Ports), ic.U)
		}
	}
}
