package web_test

import (
	"bytes"
	"context"
	"html"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// uploadPicture posts a file to the picture library's upload form.
func (c *client) uploadPicture(name string, data []byte) resp {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", name)
	_, _ = fw.Write(data)
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "http://wz.test/pictures/upload", &body)
	r.RemoteAddr = c.ip + ":50000"
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Origin", "http://wz.test")
	for k, v := range c.cookies {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	c.e.h.ServeHTTP(w, r)
	return resp{code: w.Code, loc: w.Header().Get("Location"), body: w.Body.String(), hdr: w.Header()}
}

// TestPictureLibrary: upload, ports and description, copies of built-in
// and own pictures, deleting.
func TestPictureLibrary(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")

	r := c.uploadPicture("switch.png", pngOf(400, 40))
	if r.code != http.StatusSeeOther || !strings.HasPrefix(r.loc, "/pictures/") {
		t.Fatalf("upload: %d %s", r.code, r.loc)
	}
	own := r.loc
	if r := c.get(own); r.code != http.StatusOK || !strings.Contains(r.body, "data-piceditor") {
		t.Fatalf("editor: %d", r.code)
	}
	r = c.post(own, "name", "My switch", "vendor", "Acme", "type", "switch", "u", "1", "fiber", "1", "ports", "[[10,20],[30,20],[50,20.04]]")
	if r.code != http.StatusSeeOther || r.loc != own {
		t.Fatalf("save: %d %s", r.code, r.loc)
	}
	as, _ := e.st.Assets(ctx, "icon")
	if len(as) != 1 || as[0].Name != "My switch" || as[0].Meta.Vendor != "Acme" || as[0].Meta.Type != "switch" || as[0].Meta.U != 1 ||
		len(as[0].Meta.Ports) != 3 || as[0].Meta.Ports[2][1] != 20 || as[0].Meta.Fiber != 1 {
		t.Fatalf("saved: %+v", as)
	}
	// refused: a port outside the picture, more SFP ports than ports, a bad type is dropped
	for _, bad := range [][]string{{"ports", "[[500,5]]"}, {"ports", "[[1,1]]", "fiber", "2"}, {"ports", "not json"}, {"u", "20"}} {
		kv := append([]string{"name", "My switch"}, bad...)
		c.post(own, kv...)
	}
	as, _ = e.st.Assets(ctx, "icon")
	if len(as[0].Meta.Ports) != 3 || as[0].Meta.U != 1 {
		t.Fatalf("bad description saved: %+v", as[0].Meta)
	}
	if r := c.get("/pictures"); !strings.Contains(r.body, "My switch") || !strings.Contains(r.body, "Acme") {
		t.Fatalf("list: %d %s", r.code, r.body)
	}
	// a built-in picture copied with its ports; an own picture cloned
	r = c.post("/pictures/copy", "name", "mikrotik-crs326-24g-2splusrm")
	if r.code != http.StatusSeeOther || !strings.HasPrefix(r.loc, "/pictures/") {
		t.Fatalf("copy: %d %s", r.code, r.loc)
	}
	if r := c.post("/pictures/copy", "name", "../index"); r.code != http.StatusNotFound {
		t.Fatalf("bad built-in name: %d", r.code)
	}
	if r := c.post(own + "/clone"); r.code != http.StatusSeeOther {
		t.Fatalf("clone: %d", r.code)
	}
	as, _ = e.st.Assets(ctx, "icon")
	byName := map[string]int{}
	for i, a := range as {
		byName[a.Name] = i
	}
	cp, ok1 := byName["MikroTik CRS326-24G-2S+RM"]
	cl, ok2 := byName["My switch (copy)"]
	if !ok1 || !ok2 || len(as[cp].Meta.Ports) != 26 || as[cp].Meta.Vendor != "MikroTik" || as[cp].Meta.Type != "switch" || as[cp].Meta.U != 1 ||
		as[cp].Mime != "image/svg+xml" || len(as[cl].Meta.Ports) != 3 {
		t.Fatalf("copies: %+v", as)
	}
	// the pencil of a built-in picture opens it without storing anything;
	// saved unchanged, nothing is stored; saved with a change, one changed
	// version stands for it on the maps and opens from then on
	before, _ := e.st.Assets(ctx, "icon")
	r = c.get("/pictures/builtin?lib=mikrotik-hex-rb750gr3")
	if r.code != http.StatusOK || !strings.Contains(r.body, "data-piceditor") || !strings.Contains(r.body, `action="/pictures/builtin?lib=mikrotik-hex-rb750gr3"`) {
		t.Fatalf("built-in editor: %d", r.code)
	}
	form := editorForm(r.body)
	if r := c.post("/pictures/builtin?lib=mikrotik-hex-rb750gr3", form...); r.code != http.StatusSeeOther || r.loc != "/pictures" {
		t.Fatalf("unchanged save: %d %s %q", r.code, r.loc, form)
	}
	if after, _ := e.st.Assets(ctx, "icon"); len(after) != len(before) {
		t.Fatalf("opening and saving unchanged stored a picture: %d → %d", len(before), len(after))
	}
	form[len(form)-1] = "[[1,1]]" // ports
	r = c.post("/pictures/builtin?lib=mikrotik-hex-rb750gr3", form...)
	changed := r.loc
	if r.code != http.StatusSeeOther || !strings.HasPrefix(changed, "/pictures/") || changed == "/pictures" {
		t.Fatalf("changed save: %d %s", r.code, changed)
	}
	if r := c.get("/pictures/builtin?lib=mikrotik-hex-rb750gr3"); r.loc != changed {
		t.Fatalf("the changed version does not open: %s", r.loc)
	}
	if r := c.get(changed); !strings.Contains(r.body, "notice-ok") {
		t.Fatal("changed built-in picture not marked")
	}
	r = c.post(changed + "/clone")
	as, _ = e.st.Assets(ctx, "icon")
	repl := 0
	for _, a := range as {
		if a.Meta.Replaces == "mikrotik-hex-rb750gr3" {
			repl++
			if len(a.Meta.Ports) != 1 {
				t.Fatalf("changed picture lost its ports: %+v", a.Meta)
			}
		}
	}
	if repl != 1 {
		t.Fatalf("%d pictures stand for the built-in one", repl)
	}
	if r := c.get("/pictures"); !strings.Contains(r.body, `data-replaced="mikrotik-hex-rb750gr3"`) {
		t.Fatal("list does not know the changed built-in picture")
	}
	// deleting
	if r := c.post(own + "/delete"); r.code != http.StatusSeeOther || r.loc != "/pictures" {
		t.Fatalf("delete: %d %s", r.code, r.loc)
	}
	if r := c.get(own); r.code != http.StatusNotFound {
		t.Fatalf("deleted picture still there: %d", r.code)
	}
}

var fieldRE = regexp.MustCompile(`name="(name|vendor|u|show|fiber)" value="([^"]*)"`)
var typeRE = regexp.MustCompile(`option value="(\w+)" selected`)
var portsRE = regexp.MustCompile(`name="ports" value="([^"]*)"`)

// editorForm reads the fields of the picture editor as the browser sends
// them; the ports come last.
func editorForm(body string) []string {
	if i := strings.Index(body, "data-piceditor"); i >= 0 {
		body = body[i:]
	}
	if i := strings.Index(body, "</form>"); i >= 0 {
		body = body[:i]
	}
	var kv []string
	for _, m := range fieldRE.FindAllStringSubmatch(body, -1) {
		kv = append(kv, m[1], html.UnescapeString(m[2]))
	}
	if m := typeRE.FindStringSubmatch(body); m != nil {
		kv = append(kv, "type", m[1])
	}
	if m := portsRE.FindStringSubmatch(body); m != nil {
		kv = append(kv, "ports", html.UnescapeString(m[1]))
	}
	return kv
}
