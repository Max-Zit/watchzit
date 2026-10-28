package launch

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestOpenLinkRoundTrip(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	u, err := OpenURL(key, Payload{Tool: ToolSSH, Name: "SSH", Host: "10.0.0.1", Port: 22, User: "admin", Device: "core-router"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, "watchzit://open?") {
		t.Fatalf("link %s", u)
	}
	l, err := Parse(u)
	if err != nil || l.Action != "open" || l.ServerID() != KeyID(pub) {
		t.Fatalf("parse: %v %+v", err, l)
	}
	p, err := l.Verify(pub, now.Add(10*time.Second))
	if err != nil || p.Host != "10.0.0.1" || p.User != "admin" || p.Port != 22 || p.Nonce == "" {
		t.Fatalf("verify: %v %+v", err, p)
	}
	// expired
	if _, err := l.Verify(pub, now.Add(2*time.Minute)); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired link accepted: %v", err)
	}
	// another server's key
	other, _, _ := ed25519.GenerateKey(nil)
	if _, err := l.Verify(other, now); err == nil {
		t.Fatal("foreign key accepted")
	}
	// a changed payload breaks the signature
	var m map[string]any
	_ = json.Unmarshal(l.Raw, &m)
	m["h"] = "evil.example"
	l.Raw, _ = json.Marshal(m)
	if _, err := l.Verify(pub, now); err == nil {
		t.Fatal("tampered link accepted")
	}
}

func TestPairLink(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	l, err := Parse(PairURL(pub, "monitor.example.com"))
	if err != nil || l.Action != "pair" || !l.Key.Equal(pub) || l.Name != "monitor.example.com" {
		t.Fatalf("pair: %v %+v", err, l)
	}
	if _, err := Parse("watchzit://pair?k=abc&n=x"); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := Parse("https://example.com/"); err == nil {
		t.Fatal("other scheme accepted")
	}
	if len(Fingerprint(pub)) != 79 {
		t.Fatalf("fingerprint %q", Fingerprint(pub))
	}
}

func TestPayloadRefusesInjection(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	bad := []Payload{
		{Tool: ToolSSH, Host: "-oProxyCommand=calc", Port: 22},
		{Tool: ToolSSH, Host: "10.0.0.1 && calc", Port: 22},
		{Tool: ToolSSH, Host: "10.0.0.1", Port: 22, User: "-oProxyCommand=calc"},
		{Tool: ToolSSH, Host: "10.0.0.1", Port: 22, User: `a" b`},
		{Tool: ToolSSH, Host: "10.0.0.1", Port: 0},
		{Tool: ToolSSH, Host: "10.0.0.1", Port: 22, Program: `C:\x.exe`},
		{Tool: ToolCustom, Host: "10.0.0.1", Port: 22, Program: `calc`},
		{Tool: ToolCustom, Host: "10.0.0.1", Port: 22, Program: `C:\a\..\b.exe`},
		{Tool: ToolCustom, Host: "10.0.0.1", Port: 22, Program: `C:\a\b.exe`, Args: []string{"{password}"}},
		{Tool: "cmd", Host: "10.0.0.1", Port: 22},
	}
	for _, p := range bad {
		if _, err := OpenURL(key, p, now); err == nil {
			t.Errorf("accepted %+v", p)
		}
	}
	good := []Payload{
		{Tool: ToolRDP, Host: "fe80::1%12", Port: 3389, User: `CORP\admin`},
		{Tool: ToolWinBox, Host: "router-1.lan", Port: 8291, User: "admin"},
		{Tool: ToolCustom, Host: "[2001:db8::1]", Port: 443, Program: `%ProgramFiles%\App\app.exe`, Args: []string{"--host={host}", "{port}"}},
	}
	for _, p := range good {
		if _, err := OpenURL(key, p, now); err != nil {
			t.Errorf("refused %+v: %v", p, err)
		}
	}
}

func TestVerifyRechecksFields(t *testing.T) {
	// a server bug that signs a bad host must still be refused by the launcher
	pub, key, _ := ed25519.GenerateKey(nil)
	raw, _ := json.Marshal(Payload{V: 1, Server: KeyID(pub), Tool: ToolSSH, Host: "-oX=y", Port: 22, Expires: time.Now().Add(time.Minute).Unix(), Nonce: "n"})
	sig := ed25519.Sign(key, raw)
	u := "watchzit://open?" + url.Values{"d": {base64.RawURLEncoding.EncodeToString(raw)}, "s": {base64.RawURLEncoding.EncodeToString(sig)}}.Encode()
	l, err := Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Verify(pub, time.Now()); err == nil {
		t.Fatal("bad host accepted")
	}
}

func TestArgs(t *testing.T) {
	a, err := SplitArgs(`-ssh "{user}@{host}" -P {port} "-title={name} x"`)
	if err != nil || len(a) != 5 || a[1] != "{user}@{host}" || a[4] != "-title={name} x" {
		t.Fatalf("split: %v %q", err, a)
	}
	if JoinArgs(a) != `-ssh {user}@{host} -P {port} "-title={name} x"` {
		t.Fatalf("join: %s", JoinArgs(a))
	}
	p := Payload{Args: a, Host: "10.0.0.1", Port: 2222, Device: "sw1"}
	got := p.Expand()
	if strings.Join(got, "|") != "-ssh|-P|2222|-title=sw1 x" {
		t.Fatalf("expand without user: %q", got)
	}
	p.User = "root"
	if got := p.Expand(); got[1] != "root@10.0.0.1" {
		t.Fatalf("expand: %q", got)
	}
	if _, err := SplitArgs(`"open`); err == nil {
		t.Fatal("open quote accepted")
	}
	if _, err := SplitArgs(`{host} {secret}`); err == nil {
		t.Fatal("unknown placeholder accepted")
	}
}
