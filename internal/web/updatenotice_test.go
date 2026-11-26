package web_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/update"
)

// release writes a signed release manifest of version into dir.
func release(t *testing.T, dir string, priv ed25519.PrivateKey, version string) {
	t.Helper()
	raw, _ := json.Marshal(update.Manifest{Product: "watchzit", Version: version, Released: time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC), Notes: "Notes of " + version})
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json.sig"), update.Sign(raw, priv), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateNotice(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	old := update.TrustedKeys
	update.TrustedKeys = []string{base64.StdEncoding.EncodeToString(pub)}
	t.Cleanup(func() { update.TrustedKeys = old })
	dir := t.TempDir()
	t.Setenv("WATCHZIT_UPDATE_SOURCE", "file://"+filepath.ToSlash(dir))
	helperOK := true
	e.srv.HelperAvailable = func() bool { return helperOK }
	e.srv.Version = "1.4.1"

	admin, secret := e.setupAdmin("192.168.1.10")
	admin.post("/users", "username", "watcher", "password", "watcher password 1", "role", "viewer")
	viewer := e.client("192.168.1.30")
	viewer.post("/login", "username", "watcher", "password", "watcher password 1")
	e.enroll(viewer)
	viewer.post("/account/password", "current", "watcher password 1", "password", "new watcher pass 1", "password2", "new watcher pass 1")

	const notice = `class="flash upd"`
	// no newer release: no notice
	release(t, dir, priv, "1.4.1")
	e.srv.CheckUpdate(ctx)
	if r := admin.get("/overview"); strings.Contains(r.body, notice) {
		t.Fatal("notice without a newer release")
	}

	release(t, dir, priv, "1.5.0")
	e.srv.CheckUpdate(ctx)
	r := admin.get("/devices")
	if !strings.Contains(r.body, notice) || !strings.Contains(r.body, "WatchZit 1.5.0 is available (installed: 1.4.1).") ||
		!strings.Contains(r.body, `href="https://github.com/Max-Zit/watchzit/releases/tag/v1.5.0"`) || !strings.Contains(r.body, `<a class="btn primary sm" href="/settings/version">`) {
		t.Fatalf("admin notice: %s", r.body)
	}
	if r := admin.get("/settings/version"); strings.Contains(r.body, notice) {
		t.Fatal("notice on the Updates page")
	}
	if r := viewer.get("/devices"); r.code != http.StatusOK || strings.Contains(r.body, notice) {
		t.Fatalf("viewer saw the notice: %d", r.code)
	}
	if r := viewer.post("/settings/version/hide-notice", "back", "/devices"); r.code != http.StatusForbidden {
		t.Fatalf("viewer hid the notice: %d", r.code)
	}

	// × returns to the same page and hides it in every browser of that user
	if r := admin.post("/settings/version/hide-notice", "back", "/devices?q=x"); r.code != http.StatusSeeOther || r.loc != "/devices?q=x" {
		t.Fatalf("hide: %d %s", r.code, r.loc)
	}
	if r := admin.get("/devices"); strings.Contains(r.body, notice) {
		t.Fatal("notice still shown after ×")
	}
	e.now = e.now.Add(31 * time.Second)
	other := e.client("192.168.1.11")
	other.post("/login", "username", "admin", "password", "admin password 123")
	if r := other.post("/login/2fa", "code", e.code(secret), "next", "/devices"); r.loc != "/devices" {
		t.Fatalf("second browser: %d %s", r.code, r.loc)
	}
	if r := other.get("/devices"); r.code != http.StatusOK || strings.Contains(r.body, notice) {
		t.Fatalf("notice in another browser after ×: %d", r.code)
	}
	// no open redirect
	for _, back := range []string{"//evil.example/", "https://evil.example/", "/\\evil.example"} {
		if r := admin.post("/settings/version/hide-notice", "back", back); r.loc != "/" {
			t.Fatalf("back %q went to %q", back, r.loc)
		}
	}

	// back after five days (signed in again: the session has expired by then)
	e.now = e.now.Add(5*24*time.Hour + time.Minute)
	admin = e.client("192.168.1.12")
	admin.post("/login", "username", "admin", "password", "admin password 123")
	if r := admin.post("/login/2fa", "code", e.code(secret), "next", "/devices"); r.loc != "/devices" {
		t.Fatalf("sign-in after five days: %d %s", r.code, r.loc)
	}
	if r := admin.get("/devices"); !strings.Contains(r.body, notice) {
		t.Fatal("notice not back after five days")
	}
	// a newer version shows it again at once
	admin.post("/settings/version/hide-notice", "back", "/")
	release(t, dir, priv, "1.5.1")
	e.srv.CheckUpdate(ctx)
	if r := admin.get("/devices"); !strings.Contains(r.body, "WatchZit 1.5.1 is available") {
		t.Fatal("notice not back for a newer version")
	}

	// without the helper: no Install button, how to install it instead
	helperOK = false
	r = admin.get("/devices")
	if strings.Contains(r.body, `<a class="btn primary sm" href="/settings/version">`) || !strings.Contains(r.body, "Install it on the server with install.sh or the release package.") {
		t.Fatalf("notice without the helper: %s", r.body)
	}
	helperOK = true

	// Install asks the source again: a newer version by now is shown, not installed
	release(t, dir, priv, "1.5.2")
	if r := admin.post("/settings/version/install", "version", "1.5.1"); r.code != http.StatusSeeOther || r.loc != "/settings/version" {
		t.Fatalf("install: %d %s", r.code, r.loc)
	}
	if r := admin.get("/settings/version"); !strings.Contains(r.body, "The update source now offers WatchZit 1.5.2.") || !strings.Contains(r.body, "Notes of 1.5.2") {
		t.Fatalf("install of an outdated version: %s", r.body)
	}
}
