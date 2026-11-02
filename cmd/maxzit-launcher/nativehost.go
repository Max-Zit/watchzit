package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/connect"
)

// The MaxZit Launcher is also the native-messaging host com.maxzit.passzit
// of the PassZit browser extension (it replaces passzit-helper): the
// browser starts it with one message on standard input, it opens the
// connection with the credential from the vault and answers on standard
// output. See internal/connect for what it accepts and how secrets are
// handed to the tools.

// browserStarted tells whether a browser started the launcher as a native
// host: Chromium passes the extension's origin (and --parent-window),
// Firefox the manifest's path and the extension's id.
func browserStarted(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if strings.HasPrefix(args[0], "chrome-extension://") {
		return true
	}
	return len(args) >= 2 && args[1] == connect.FirefoxID
}

// strictJSON refuses unknown fields (a message is exactly a request).
func strictJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

// handleMessage answers one message of the extension; run starts what
// connect.Plan worked out.
func handleMessage(raw []byte, env connect.Env, run func(*connect.Action) error) connect.Response {
	var r connect.Request
	if err := strictJSON(raw, &r); err != nil {
		return connect.Response{Error: "unreadable message"}
	}
	if r.Ping {
		return connect.Response{OK: true, Version: version}
	}
	a, err := connect.Plan(r, env)
	if err != nil {
		return connect.Response{Error: err.Error()}
	}
	if err := run(a); err != nil {
		return connect.Response{Error: err.Error()}
	}
	return a.Response
}

// pairing is a request to let a PassZit extension use the launcher.
type pairing struct {
	Browser string // chromium or firefox
	ID      string
}

// parsePair reads maxzit-launcher://pair?app=passzit&browser=…&id=… (any
// web page can open such a link: the launcher asks before it does anything).
func parsePair(link string) (pairing, error) {
	u, err := url.Parse(link)
	if err != nil || !strings.EqualFold(u.Scheme, "maxzit-launcher") {
		return pairing{}, errors.New("not a launcher link")
	}
	if strings.Trim(u.Host+u.Path, "/") != "pair" {
		return pairing{}, errors.New("unknown launcher link")
	}
	q := u.Query()
	if q.Get("app") != "passzit" {
		return pairing{}, errors.New("only the PassZit extension can be paired")
	}
	p := pairing{Browser: q.Get("browser"), ID: q.Get("id")}
	switch {
	case p.Browser == "chromium" && connect.ValidChromeID(p.ID):
	case p.Browser == "firefox" && p.ID == connect.FirefoxID:
	default:
		return pairing{}, errors.New("the link does not name a PassZit extension")
	}
	return p, nil
}

// chromeIDs are the extension ids an existing Chromium manifest allows.
func chromeIDs(manifest []byte) []string {
	var m struct {
		Origins []string `json:"allowed_origins"`
	}
	_ = json.Unmarshal(manifest, &m)
	var ids []string
	for _, o := range m.Origins {
		id := strings.TrimSuffix(strings.TrimPrefix(o, "chrome-extension://"), "/")
		if connect.ValidChromeID(id) && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// tempPrefix marks the files with secrets the launcher writes for a tool.
const tempPrefix = "pzh-"

// cleanupArgs is what the detached clean-up gets: seconds to wait, the
// temporary files, and "cred:<target>" for a Windows credential.
func cleanupArgs(keep time.Duration, files []string, cred string) []string {
	out := append([]string{"--cleanup", strconv.Itoa(int(keep.Seconds()))}, files...)
	if cred != "" {
		out = append(out, "cred:"+cred)
	}
	return out
}

// cleanup removes temporary secrets after a while: only the launcher's own
// files in the temp folder, and the credentials it named.
func cleanup(args []string, deleteCred func(string)) {
	if len(args) == 0 {
		return
	}
	secs, _ := strconv.Atoi(args[0])
	if secs < 0 || secs > 300 {
		secs = 30
	}
	time.Sleep(time.Duration(secs) * time.Second)
	for _, a := range args[1:] {
		if t, ok := strings.CutPrefix(a, "cred:"); ok {
			if strings.HasPrefix(t, "TERMSRV/") {
				deleteCred(t)
			}
			continue
		}
		if strings.HasPrefix(filepath.Base(a), tempPrefix) && filepath.Dir(a) == filepath.Clean(os.TempDir()) {
			_ = os.Remove(a) // #nosec G703 -- only our own pzh-* files in the temp folder
		}
	}
}
