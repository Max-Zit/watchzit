package web_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/helper"
)

// TestServerSettings: the parts of Settings → Updates and server that go to
// the root helper — operating system updates, a server restart, SSH (only
// on the appliance) and a release uploaded as files (only when signed).
func TestServerSettings(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.srv.DataDir = dir
	e.srv.HelperAvailable = func() bool { return true }
	c, _ := e.setupAdmin("192.168.1.10")
	request := func() *helper.Request {
		b, err := os.ReadFile(filepath.Join(dir, "helper", helper.RequestFile))
		if err != nil {
			return nil
		}
		var r helper.Request
		_ = json.Unmarshal(b, &r)
		_ = os.Remove(filepath.Join(dir, "helper", helper.RequestFile))
		return &r
	}

	r := c.get("/settings/version")
	for _, want := range []string{"Operating system updates", "Install from files", "Restart the server"} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("page lacks %q", want)
		}
	}
	if q := request(); q == nil || q.Action != helper.ActionOSStatus {
		t.Fatalf("the OS status was not asked for: %+v", q)
	}
	// a fresh status: the page does not ask again
	st, _ := json.Marshal(helper.OSStatus{Checked: time.Now().UTC(), OS: "Debian GNU/Linux 13", Pending: 3})
	_ = os.WriteFile(filepath.Join(dir, "helper", helper.OSStatusFile), st, 0o644)
	for do, action := range map[string]string{"upgrade": helper.ActionOSUpgrade, "reboot": helper.ActionReboot, "auto-on": helper.ActionOSAuto} {
		if r := c.post("/settings/os", "do", do); r.code != http.StatusSeeOther {
			t.Fatalf("%s: %d", do, r.code)
		}
		if q := request(); q == nil || q.Action != action || q.RequestedBy != "admin" || (do == "auto-on" && !q.Enable) {
			t.Fatalf("%s: %+v", do, q)
		}
	}
	// a second request waits for the first one
	c.post("/settings/os", "do", "status")
	if r := c.post("/settings/os", "do", "upgrade"); !strings.Contains(r.loc+r.body, "error") && !strings.Contains(c.get("/settings/version").body, "still busy") {
		t.Fatal("a second request was accepted while one waits")
	}
	request()

	// SSH is changed only on the appliance (this test server is none)
	c.post("/settings/ssh", "enabled", "1", "port", "2222")
	if q := request(); q != nil {
		t.Fatalf("SSH request outside the appliance: %+v", q)
	}
	if r := c.get("/settings/version"); strings.Contains(r.body, `action="/settings/ssh"`) {
		t.Fatal("SSH form outside the appliance")
	}

	// an uploaded release must be signed with the release key
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, content := range map[string]string{"manifest": `{"product":"watchzit","version":"99.0.0","files":[]}`, "signature": "AAAA", "package": "not a package"} {
		fw, _ := mw.CreateFormFile(name, name+".bin")
		_, _ = fw.Write([]byte(content))
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "http://wz.test/settings/version/upload", &body)
	req.RemoteAddr = "192.168.1.10:50000"
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", "http://wz.test")
	for k, v := range c.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	for _, ck := range w.Result().Cookies() {
		c.cookies[ck.Name] = ck.Value
	}
	if w.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d", w.Code)
	}
	if q := request(); q != nil {
		t.Fatalf("an unsigned release was handed to the helper: %+v", q)
	}
	if _, err := os.Stat(filepath.Join(dir, "helper", helper.UploadDir)); err == nil {
		t.Fatal("files of an unsigned release were kept")
	}
	if r := c.get("/settings/version"); !strings.Contains(r.body, "refused") {
		t.Fatal("no message for the refused release")
	}
}
