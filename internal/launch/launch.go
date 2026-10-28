// Package launch is the link between the console and the WatchZit
// Launcher, the small Windows program that opens devices with local tools
// (PuTTY, Remote Desktop, WinBox, VNC, own programs).
//
// The browser cannot start programs, so the console hands the browser a
// watchzit:// link which Windows passes to the launcher:
//
//	watchzit://pair?k=<public key>&n=<server name>   (once per computer)
//	watchzit://open?d=<payload>&s=<signature>        (every click)
//
// The payload is JSON signed with the server's ed25519 key; it names the
// tool, host, port and user (never a password), expires after a minute and
// carries a nonce, so a link works once. The launcher accepts links only from
// servers the user paired, checks every field against a strict character set
// and starts programs with an argument list (no shell).
package launch

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Scheme is the URL scheme the launcher registers.
const Scheme = "watchzit"

// Built-in tools; the launcher knows how to start them.
const (
	ToolSSH    = "ssh"
	ToolRDP    = "rdp"
	ToolWinBox = "winbox"
	ToolVNC    = "vnc"
	ToolTelnet = "telnet"
	ToolCustom = "custom"
)

// Builtins lists the built-in tools in display order.
var Builtins = []string{ToolSSH, ToolRDP, ToolWinBox, ToolVNC, ToolTelnet}

// MaxAge is how long an open link is valid.
const MaxAge = 60 * time.Second

// Payload is what an open link carries.
type Payload struct {
	V       int      `json:"v"`
	Server  string   `json:"k"`           // server id (KeyID of its public key)
	Tool    string   `json:"t"`           // built-in tool or "custom"
	Name    string   `json:"n"`           // tool name, for messages
	Program string   `json:"p,omitempty"` // custom: full path of the program
	Args    []string `json:"a,omitempty"` // custom: argument templates
	Host    string   `json:"h"`
	Port    int      `json:"o"`
	User    string   `json:"u,omitempty"`
	Device  string   `json:"d,omitempty"` // device name, for {name} and titles
	Expires int64    `json:"e"`
	Nonce   string   `json:"x"`
}

// KeyID names a public key: the first 16 hex digits of its SHA-256.
func KeyID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

// Fingerprint is the full SHA-256 of a public key, grouped for reading.
func Fingerprint(pub ed25519.PublicKey) string {
	h := hex.EncodeToString(func() []byte { s := sha256.Sum256(pub); return s[:] }())
	var parts []string
	for i := 0; i < len(h); i += 4 {
		parts = append(parts, h[i:i+4])
	}
	return strings.ToUpper(strings.Join(parts, " "))
}

var b64 = base64.RawURLEncoding

// PairURL is the link that makes a launcher trust a server.
func PairURL(pub ed25519.PublicKey, name string) string {
	q := url.Values{"k": {b64.EncodeToString(pub)}, "n": {name}}
	return Scheme + "://pair?" + q.Encode()
}

// OpenURL signs a payload into an open link (the server fills Expires,
// Nonce and Server).
func OpenURL(key ed25519.PrivateKey, p Payload, now time.Time) (string, error) {
	p.V = 1
	p.Server = KeyID(key.Public().(ed25519.PublicKey))
	p.Expires = now.Add(MaxAge).Unix()
	n := make([]byte, 12)
	if _, err := rand.Read(n); err != nil {
		return "", err
	}
	p.Nonce = b64.EncodeToString(n)
	if err := p.Check(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(key, raw)
	return Scheme + "://open?" + url.Values{"d": {b64.EncodeToString(raw)}, "s": {b64.EncodeToString(sig)}}.Encode(), nil
}

// Link is a parsed watchzit:// link.
type Link struct {
	Action string // "pair" or "open"
	// pair
	Key  ed25519.PublicKey
	Name string
	// open
	Raw []byte
	Sig []byte
}

// Parse reads a watchzit:// link without trusting it.
func Parse(s string) (*Link, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme != Scheme {
		return nil, errors.New("not a WatchZit link")
	}
	q := u.Query()
	switch u.Host {
	case "pair":
		k, err := b64.DecodeString(q.Get("k"))
		if err != nil || len(k) != ed25519.PublicKeySize {
			return nil, errors.New("the pairing link is damaged")
		}
		name := q.Get("n")
		if !nameRE.MatchString(name) {
			return nil, errors.New("the pairing link is damaged")
		}
		return &Link{Action: "pair", Key: k, Name: name}, nil
	case "open":
		raw, err1 := b64.DecodeString(q.Get("d"))
		sig, err2 := b64.DecodeString(q.Get("s"))
		if err1 != nil || err2 != nil || len(raw) == 0 || len(raw) > 8192 || len(sig) != ed25519.SignatureSize {
			return nil, errors.New("the link is damaged")
		}
		return &Link{Action: "open", Raw: raw, Sig: sig}, nil
	}
	return nil, errors.New("unknown WatchZit link")
}

// ServerID tells which server an open link claims to come from (before the
// signature is checked, to find its key).
func (l *Link) ServerID() string {
	var p struct {
		Server string `json:"k"`
	}
	_ = json.Unmarshal(l.Raw, &p)
	return p.Server
}

// Verify checks an open link against the server's key and the clock and
// returns its payload.
func (l *Link) Verify(pub ed25519.PublicKey, now time.Time) (*Payload, error) {
	if !ed25519.Verify(pub, l.Raw, l.Sig) {
		return nil, errors.New("the link is not signed by this server")
	}
	var p Payload
	if err := json.Unmarshal(l.Raw, &p); err != nil {
		return nil, errors.New("the link is damaged")
	}
	if p.V != 1 || p.Server != KeyID(pub) {
		return nil, errors.New("the link is not for this launcher")
	}
	exp := time.Unix(p.Expires, 0)
	if now.After(exp.Add(30 * time.Second)) { // a little clock difference is fine
		return nil, errors.New("the link has expired; click it again in the console")
	}
	if exp.Sub(now) > MaxAge+5*time.Minute {
		return nil, errors.New("the link is valid for too long")
	}
	if err := p.Check(); err != nil {
		return nil, err
	}
	return &p, nil
}

var (
	// hostRE: names, IPv4 and IPv6 (letters, digits, dots, dashes, colons,
	// brackets, percent for zones); nothing a shell or a program option
	// could read as something else.
	hostRE = regexp.MustCompile(`^[A-Za-z0-9\[][A-Za-z0-9.:\-\[\]%]{0,252}$`)
	userRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@\\\-]{0,63}$`)
	nameRE = regexp.MustCompile(`^[\p{L}\p{N} ._\-()]{1,100}$`)
	// a program: an absolute Windows path to an .exe, or a bare file name
	progRE = regexp.MustCompile(`(?i)^([a-z]:\\|%[a-z_()]+%\\)[^<>"|?*\r\n]{1,250}\.exe$`)
	// an argument template: printable, no quotes or line breaks
	argRE   = regexp.MustCompile(`^[^"\r\n\x00]{0,200}$`)
	tokenRE = regexp.MustCompile(`\{[a-z]+\}`)
)

// ValidHost reports whether a host can go into a link.
func ValidHost(h string) bool { return hostRE.MatchString(h) }

// ValidUser reports whether a user name can go into a link ("" is fine).
func ValidUser(u string) bool { return u == "" || userRE.MatchString(u) }

// CheckProgram validates the program path of a custom tool.
func CheckProgram(p string) error {
	if !progRE.MatchString(p) || strings.Contains(p, `..`) {
		return errors.New("the program must be a full path to an .exe file, for example C:\\Program Files\\App\\app.exe")
	}
	return nil
}

// CheckArgs validates the argument templates of a custom tool.
func CheckArgs(args []string) error {
	if len(args) > 20 {
		return errors.New("at most 20 arguments")
	}
	for _, a := range args {
		if !argRE.MatchString(a) {
			return errors.New("an argument may not contain quotes or line breaks")
		}
		for _, t := range tokenRE.FindAllString(a, -1) {
			if t != "{host}" && t != "{port}" && t != "{user}" && t != "{name}" {
				return fmt.Errorf("unknown placeholder %s (use {host}, {port}, {user}, {name})", t)
			}
		}
	}
	return nil
}

// Check validates every field of a payload.
func (p *Payload) Check() error {
	if !ValidHost(p.Host) {
		return errors.New("the device address cannot be used in a link")
	}
	if !ValidUser(p.User) {
		return errors.New("the user name cannot be used in a link")
	}
	if p.Port < 1 || p.Port > 65535 {
		return errors.New("the port must be 1–65535")
	}
	if p.Device != "" && !nameRE.MatchString(p.Device) {
		p.Device = ""
	}
	switch p.Tool {
	case ToolSSH, ToolRDP, ToolWinBox, ToolVNC, ToolTelnet:
		if p.Program != "" || len(p.Args) > 0 {
			return errors.New("a built-in tool takes no program")
		}
	case ToolCustom:
		if err := CheckProgram(p.Program); err != nil {
			return err
		}
		if err := CheckArgs(p.Args); err != nil {
			return err
		}
	default:
		return errors.New("unknown tool")
	}
	return nil
}

// Expand fills the placeholders of a custom tool's arguments. Each template
// stays one argument; an argument that names an empty user is dropped.
func (p *Payload) Expand() []string {
	var out []string
	for _, a := range p.Args {
		if strings.Contains(a, "{user}") && p.User == "" {
			continue
		}
		a = strings.NewReplacer("{host}", p.Host, "{port}", strconv.Itoa(p.Port), "{user}", p.User, "{name}", p.Device).Replace(a)
		out = append(out, a)
	}
	return out
}

// SplitArgs reads an argument template line as the console's form gives it:
// arguments separated by spaces, an argument in double quotes may contain
// spaces ("{host}:{port}" "-title={name}").
func SplitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQ, have := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQ, have = !inQ, true
		case (r == ' ' || r == '\t') && !inQ:
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(r)
			have = true
		}
	}
	if inQ {
		return nil, errors.New("a quote is not closed")
	}
	if have {
		out = append(out, cur.String())
	}
	return out, CheckArgs(out)
}

// JoinArgs writes argument templates back as one line for the form.
func JoinArgs(args []string) string {
	var b []string
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " \t") {
			a = `"` + a + `"`
		}
		b = append(b, a)
	}
	return strings.Join(b, " ")
}

// KeyFile is the server's launcher signing key in the data directory.
const KeyFile = "launcher.key"

// LoadOrCreateKey reads the server's signing key (an ed25519 seed in hex),
// creating it on first use.
func LoadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- a file in the server's data directory
	if err == nil {
		seed, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("%s is damaged", path)
		}
		return ed25519.NewKeyFromSeed(seed), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key.Seed())+"\n"), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}
