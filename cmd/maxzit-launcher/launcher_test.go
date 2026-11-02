package main

import (
	"crypto/ed25519"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/launch"
)

type fakeUI struct {
	answer bool
	asked  []string
	told   []string
}

func (f *fakeUI) ask(_, text string) bool { f.asked = append(f.asked, text); return f.answer }
func (f *fakeUI) info(_, text string)     { f.told = append(f.told, text) }
func (f *fakeUI) fail(_, text string)     { f.told = append(f.told, text) }

type ran struct {
	prog string
	args []string
}

func newLauncher(t *testing.T, progs map[string]string) (*launcher, *fakeUI, *[]ran) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	ui := &fakeUI{answer: true}
	var runs []ran
	l := &launcher{cfg: cfg, ui: ui, now: time.Now, temp: dir + "/tmp",
		find: func(tool string) string {
			if c, ok := strings.CutPrefix(tool, "custom:"); ok {
				return progs["custom"] + c
			}
			return progs[tool]
		},
		run: func(p string, a []string) error { runs = append(runs, ran{p, a}); return nil },
		log: func(string, ...any) {}}
	return l, ui, &runs
}

func TestPairAndOpen(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	l, ui, runs := newLauncher(t, map[string]string{"ssh": `C:\Windows\System32\OpenSSH\ssh.exe`})
	link, _ := launch.OpenURL(key, launch.Payload{Tool: launch.ToolSSH, Name: "SSH", Host: "10.0.0.1", Port: 2222, User: "admin"}, time.Now())

	// not paired yet
	if err := l.handle(link); err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Fatalf("unpaired: %v", err)
	}
	// refusing the pairing keeps the server unknown
	ui.answer = false
	if err := l.handle(launch.PairURL(pub, "monitor.example.com")); err != nil || len(l.cfg.Servers) != 0 {
		t.Fatalf("refused pairing: %v %d", err, len(l.cfg.Servers))
	}
	ui.answer = true
	if err := l.handle(launch.PairURL(pub, "monitor.example.com")); err != nil || len(l.cfg.Servers) != 1 {
		t.Fatalf("pairing: %v", err)
	}
	if !strings.Contains(ui.asked[1], launch.Fingerprint(pub)) {
		t.Fatal("the pairing question does not show the fingerprint")
	}
	// the pairing is saved
	again, _ := loadConfig(strings.TrimSuffix(l.cfg.path, "launcher.json"))
	if len(again.Servers) != 1 {
		t.Fatal("pairing not saved")
	}

	if err := l.handle(link); err != nil {
		t.Fatal(err)
	}
	if len(*runs) != 1 || strings.Join((*runs)[0].args, " ") != "-p 2222 -l admin 10.0.0.1" {
		t.Fatalf("ssh: %+v", *runs)
	}
	// a link works once
	if err := l.handle(link); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("reused link: %v", err)
	}
	// another server's link
	_, other, _ := ed25519.GenerateKey(nil)
	foreign, _ := launch.OpenURL(other, launch.Payload{Tool: launch.ToolSSH, Host: "10.0.0.1", Port: 22}, time.Now())
	if err := l.handle(foreign); err == nil {
		t.Fatal("foreign link accepted")
	}
	// an old link
	old, _ := launch.OpenURL(key, launch.Payload{Tool: launch.ToolSSH, Host: "10.0.0.1", Port: 22}, time.Now().Add(-5*time.Minute))
	if err := l.handle(old); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("old link: %v", err)
	}
}

func TestToolCommands(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	pub := key.Public().(ed25519.PublicKey)
	l, ui, runs := newLauncher(t, map[string]string{"putty": `C:\PuTTY\putty.exe`, "mstsc": `C:\Windows\System32\mstsc.exe`,
		"winbox": `C:\Users\x\Desktop\winbox64.exe`, "vnc": `C:\TightVNC\tvnviewer.exe`, "custom": ""})
	_ = l.pair(&launch.Link{Key: pub, Name: "srv"})
	open := func(p launch.Payload) error {
		u, err := launch.OpenURL(key, p, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return l.handle(u)
	}
	check := func(want string) {
		t.Helper()
		r := (*runs)[len(*runs)-1]
		if got := r.prog + " " + strings.Join(r.args, " "); got != want {
			t.Fatalf("got  %q\nwant %q", got, want)
		}
	}
	_ = open(launch.Payload{Tool: launch.ToolSSH, Host: "sw1", Port: 22, User: "root"})
	check(`C:\PuTTY\putty.exe -ssh -P 22 -l root sw1`)
	_ = open(launch.Payload{Tool: launch.ToolTelnet, Host: "sw1", Port: 23})
	check(`C:\PuTTY\putty.exe -telnet -P 23 sw1`)
	_ = open(launch.Payload{Tool: launch.ToolWinBox, Host: "10.0.0.1", Port: 8291, User: "admin"})
	check(`C:\Users\x\Desktop\winbox64.exe 10.0.0.1 admin `)
	_ = open(launch.Payload{Tool: launch.ToolWinBox, Host: "10.0.0.1", Port: 8292})
	check(`C:\Users\x\Desktop\winbox64.exe 10.0.0.1:8292`)
	_ = open(launch.Payload{Tool: launch.ToolVNC, Host: "pc1", Port: 5901})
	check(`C:\TightVNC\tvnviewer.exe pc1::5901`)
	if err := open(launch.Payload{Tool: launch.ToolRDP, Host: "srv1", Port: 3390, User: `CORP\admin`}); err != nil {
		t.Fatal(err)
	}
	r := (*runs)[len(*runs)-1]
	b, err := os.ReadFile(r.args[0])
	if err != nil || !strings.Contains(string(b), "full address:s:srv1:3390") || !strings.Contains(string(b), `username:s:CORP\admin`) {
		t.Fatalf("rdp file: %v %q", err, b)
	}

	// a custom program: asked once, then remembered; refused means not started
	n := len(*runs)
	ui.answer = false
	_ = open(launch.Payload{Tool: launch.ToolCustom, Name: "App", Program: `C:\App\app.exe`, Args: []string{"--host={host}", "-u", "{user}"}, Host: "h1", Port: 9})
	if len(*runs) != n {
		t.Fatal("a refused program was started")
	}
	ui.answer = true
	_ = open(launch.Payload{Tool: launch.ToolCustom, Name: "App", Program: `C:\App\app.exe`, Args: []string{"--host={host}", "{port}"}, Host: "h1", Port: 9})
	check(`C:\App\app.exe --host=h1 9`)
	asked := len(ui.asked)
	_ = open(launch.Payload{Tool: launch.ToolCustom, Name: "App", Program: `C:\App\app.exe`, Args: []string{"{host}"}, Host: "h2", Port: 9})
	if len(ui.asked) != asked {
		t.Fatal("asked again for an allowed program")
	}
	check(`C:\App\app.exe h2`)

	// a missing program is reported, not started
	l.find = func(string) string { return "" }
	if err := open(launch.Payload{Tool: launch.ToolWinBox, Host: "10.0.0.1", Port: 8291}); err == nil || !strings.Contains(err.Error(), "WinBox was not found") {
		t.Fatalf("missing: %v", err)
	}
}
