package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/max-zit/watchzit/internal/connect"
)

func TestBrowserStarted(t *testing.T) {
	for args, want := range map[string]bool{
		"chrome-extension://abcdefghijklmnopabcdefghijklmnop/": true,
		"watchzit://open?x":                  false,
		"maxzit-launcher://pair?app=passzit": false,
		"":                                   false,
	} {
		var a []string
		if args != "" {
			a = []string{args, "--parent-window=0"}
		}
		if got := browserStarted(a); got != want {
			t.Errorf("%q: %v", args, got)
		}
	}
	if !browserStarted([]string{`C:\x\firefox-host.json`, connect.FirefoxID}) {
		t.Error("Firefox start not recognised")
	}
	if browserStarted([]string{`C:\x\firefox-host.json`, "evil@example.com"}) {
		t.Error("another Firefox extension recognised")
	}
}

// TestHandleMessage: a ping answers, unknown fields and option injection are
// refused before anything runs, and a valid request runs what Plan made.
func TestHandleMessage(t *testing.T) {
	ran := 0
	run := func(*connect.Action) error { ran++; return nil }
	env := connect.Env{GOOS: "windows", LookPath: func(names ...string) (string, error) { return `C:\tools\` + names[0], nil }}
	if r := handleMessage([]byte(`{"v":1,"ping":true}`), env, run); !r.OK || r.Version == "" {
		t.Fatalf("ping %+v", r)
	}
	for _, bad := range []string{`{"v":1,"tool":"ssh","host":"h","evil":1}`, `{"v":1,"tool":"ssh","host":"-oProxyCommand=x"}`, `not json`, `{"v":1,"tool":"cmd","host":"h"}`} {
		if r := handleMessage([]byte(bad), env, run); r.OK || r.Error == "" {
			t.Errorf("%s accepted: %+v", bad, r)
		}
	}
	if ran != 0 {
		t.Fatal("a refused request ran")
	}
	if r := handleMessage([]byte(`{"v":1,"tool":"rdp","host":"10.0.0.5","user":"admin","password":"pw"}`), env, run); !r.OK || ran != 1 {
		t.Fatalf("rdp %+v (ran %d)", r, ran)
	}
	fail := func(*connect.Action) error { return errors.New("mstsc.exe could not be started") }
	if r := handleMessage([]byte(`{"v":1,"tool":"rdp","host":"10.0.0.5"}`), env, fail); r.OK || r.Error == "" {
		t.Fatalf("start error not reported: %+v", r)
	}
}

func TestParsePair(t *testing.T) {
	id := "abcdefghijklmnopabcdefghijklmnop"
	if p, err := parsePair("maxzit-launcher://pair?app=passzit&browser=chromium&id=" + id); err != nil || p.Browser != "chromium" || p.ID != id {
		t.Fatalf("chromium: %+v %v", p, err)
	}
	if p, err := parsePair("maxzit-launcher://pair?app=passzit&browser=firefox&id=" + connect.FirefoxID); err != nil || p.Browser != "firefox" {
		t.Fatalf("firefox: %+v %v", p, err)
	}
	for _, bad := range []string{
		"maxzit-launcher://pair?app=other&browser=chromium&id=" + id,
		"maxzit-launcher://pair?app=passzit&browser=chromium&id=ABCDEFGHIJKLMNOPABCDEFGHIJKLMNOP",
		"maxzit-launcher://pair?app=passzit&browser=chromium&id=" + id + "q",
		"maxzit-launcher://pair?app=passzit&browser=firefox&id=evil@example.com",
		"maxzit-launcher://open?app=passzit&browser=chromium&id=" + id,
		"watchzit://pair?app=passzit&browser=chromium&id=" + id,
	} {
		if _, err := parsePair(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestChromeIDs(t *testing.T) {
	a, b := "abcdefghijklmnopabcdefghijklmnop", "ponmlkjihgfedcbaponmlkjihgfedcba"
	m, err := connect.ChromeManifest(`C:\x\maxzit-launcher.exe`, []string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if ids := chromeIDs(m); len(ids) != 2 || ids[0] != a || ids[1] != b {
		t.Fatalf("%v", ids)
	}
	if ids := chromeIDs([]byte(`{"allowed_origins":["chrome-extension://bad/","https://evil.example/"]}`)); len(ids) != 0 {
		t.Fatalf("invalid origins kept: %v", ids)
	}
}

func TestCleanupOnlyOwn(t *testing.T) {
	own, err := os.CreateTemp("", tempPrefix+"*")
	if err != nil {
		t.Fatal(err)
	}
	own.Close()
	other := filepath.Join(t.TempDir(), tempPrefix+"not-in-temp")
	_ = os.WriteFile(other, []byte("x"), 0o600)
	var creds []string
	cleanup([]string{"0", own.Name(), other, "cred:TERMSRV/10.0.0.5", "cred:git:https://github.com"}, func(c string) { creds = append(creds, c) })
	if _, err := os.Stat(own.Name()); err == nil {
		t.Error("own file left")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("a file the launcher did not make was removed")
	}
	if len(creds) != 1 || creds[0] != "TERMSRV/10.0.0.5" {
		t.Errorf("credentials removed: %v", creds)
	}
	if a := cleanupArgs(0, []string{"f"}, "TERMSRV/h"); len(a) != 4 || a[0] != "--cleanup" || a[3] != "cred:TERMSRV/h" {
		t.Errorf("cleanup args %v", a)
	}
}
