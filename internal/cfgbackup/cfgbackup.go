// Package cfgbackup reads the configuration of network devices over SSH
// (MikroTik RouterOS, Cisco IOS, FortiGate, or any command), removes the
// lines that change on every read (time stamps), and compares versions.
package cfgbackup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Methods: what to run on the device.
const (
	MikroTik  = "mikrotik"
	Cisco     = "cisco"
	FortiGate = "fortigate"
	Command   = "command"
)

// Methods lists them in the order the console offers them.
var Methods = []string{MikroTik, Cisco, FortiGate, Command}

// CommandOf is the command a method runs (custom: the given one).
func CommandOf(method, custom string) string {
	switch method {
	case MikroTik:
		return "/export terse show-sensitive"
	case Cisco:
		return "show running-config"
	case FortiGate:
		return "show full-configuration"
	}
	return custom
}

// Target is how to reach a device.
type Target struct {
	Address  string // host name or IP
	Port     int
	User     string
	Password string
	Key      string // private key (PEM), instead of or with the password
	HostKey  string // the host key seen before ("" on the first backup)
	Method   string
	Command  string // for Command
	Timeout  time.Duration
}

// Result is a read configuration.
type Result struct {
	Text    string // normalised
	HostKey string // the host key (to remember on the first backup)
}

// ErrHostKey: the device answered with another host key than before.
var ErrHostKey = errors.New("the SSH host key of the device changed; if that was expected (new device or firmware), forget the saved key")

// MaxSize limits a configuration.
const MaxSize = 4 << 20

// Fetch connects and reads the configuration.
func Fetch(ctx context.Context, t Target) (*Result, error) {
	if t.Port == 0 {
		t.Port = 22
	}
	if t.Timeout == 0 {
		t.Timeout = 60 * time.Second
	}
	cmd := CommandOf(t.Method, t.Command)
	if strings.TrimSpace(cmd) == "" {
		return nil, errors.New("no command to run")
	}
	var auths []ssh.AuthMethod
	if t.Key != "" {
		var signer ssh.Signer
		var err error
		if t.Password != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(t.Key), []byte(t.Password))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(t.Key))
		}
		if err != nil {
			return nil, fmt.Errorf("the private key cannot be read: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	if t.Password != "" {
		pw := t.Password
		auths = append(auths, ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			out := make([]string, len(qs))
			for i := range out {
				out[i] = pw
			}
			return out, nil
		}))
	}
	if len(auths) == 0 {
		return nil, errors.New("enter a password or a private key")
	}
	res := &Result{}
	cfg := &ssh.ClientConfig{
		User: t.User, Auth: auths, Timeout: 15 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			k := key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal())
			res.HostKey = k
			if t.HostKey != "" && t.HostKey != k {
				return ErrHostKey
			}
			return nil
		},
	}
	// older network devices still offer only these
	cfg.KeyExchanges = append(ssh.SupportedAlgorithms().KeyExchanges, "diffie-hellman-group14-sha1", "diffie-hellman-group1-sha1")
	cfg.Ciphers = append(ssh.SupportedAlgorithms().Ciphers, "aes128-cbc", "aes256-cbc")
	//lint:ignore SA1019 old Cisco IOS images offer only ssh-dss
	cfg.HostKeyAlgorithms = append(ssh.SupportedAlgorithms().HostKeys, ssh.KeyAlgoRSA, ssh.KeyAlgoDSA) // #nosec G106 -- old device keys; pinned on first use
	ctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(t.Address, strconv.Itoa(t.Port)))
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, t.Address, cfg)
	if err != nil {
		_ = conn.Close()
		if errors.Is(err, ErrHostKey) || strings.Contains(err.Error(), ErrHostKey.Error()) {
			return nil, ErrHostKey
		}
		return nil, err
	}
	client := ssh.NewClient(sc, chans, reqs)
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var out bytes.Buffer
	sess.Stdout = &limited{w: &out, n: MaxSize}
	sess.Stderr = &limited{w: &out, n: 64 << 10}
	if err := sess.Run(cmd); err != nil && out.Len() == 0 {
		return nil, fmt.Errorf("the command failed: %w", err)
	}
	text := Normalize(t.Method, out.String())
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("the device returned an empty configuration")
	}
	res.Text = text
	return res, nil
}

type limited struct {
	w interface{ Write([]byte) (int, error) }
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	l.n -= len(q)
	_, err := l.w.Write(q)
	return len(p), err
}

var (
	reMikroTikHead = regexp.MustCompile(`^# (\d{4}-\d{2}-\d{2} |[a-z]{3}/\d{2}/\d{4} )?\d{2}:\d{2}:\d{2} by RouterOS`)
	reCiscoChange  = regexp.MustCompile(`^! (Last configuration change|NVRAM config last updated|No configuration change since)`)
	reCiscoBytes   = regexp.MustCompile(`^(Building configuration|Current configuration : \d+ bytes)`)
	reFortiVer     = regexp.MustCompile(`^#(conf_file_ver|buildno|global_vdom)=`)
)

// Normalize removes the lines that change on every read and unifies line
// ends, so equal configurations compare equal.
func Normalize(method, s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "")
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, " \t")
		switch {
		case method == MikroTik && reMikroTikHead.MatchString(l):
			continue
		case method == Cisco && (reCiscoChange.MatchString(l) || reCiscoBytes.MatchString(l)):
			continue
		case method == FortiGate && reFortiVer.MatchString(l):
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n")) + "\n"
}

// Hash identifies a configuration.
func Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(h[:12])
}

// Line is one line of a comparison.
type Line struct {
	Op   byte // ' ' same, '+' added, '-' removed, '~' lines left out
	Text string
}

// Diff compares two texts line by line (longest common subsequence;
// texts over 20,000 lines are compared by their changed middle only).
func Diff(a, b string) (lines []Line, added, removed int) {
	x, y := strings.Split(strings.TrimSuffix(a, "\n"), "\n"), strings.Split(strings.TrimSuffix(b, "\n"), "\n")
	// common start and end
	pre := 0
	for pre < len(x) && pre < len(y) && x[pre] == y[pre] {
		pre++
	}
	suf := 0
	for suf < len(x)-pre && suf < len(y)-pre && x[len(x)-1-suf] == y[len(y)-1-suf] {
		suf++
	}
	for _, l := range x[:pre] {
		lines = append(lines, Line{' ', l})
	}
	mx, my := x[pre:len(x)-suf], y[pre:len(y)-suf]
	if len(mx)*len(my) > 25_000_000 { // too big for the table: all removed, all added
		for _, l := range mx {
			lines = append(lines, Line{'-', l})
		}
		for _, l := range my {
			lines = append(lines, Line{'+', l})
		}
	} else {
		n, m := len(mx), len(my)
		lcs := make([][]int32, n+1)
		for i := range lcs {
			lcs[i] = make([]int32, m+1)
		}
		for i := n - 1; i >= 0; i-- {
			for j := m - 1; j >= 0; j-- {
				if mx[i] == my[j] {
					lcs[i][j] = lcs[i+1][j+1] + 1
				} else if lcs[i+1][j] >= lcs[i][j+1] {
					lcs[i][j] = lcs[i+1][j]
				} else {
					lcs[i][j] = lcs[i][j+1]
				}
			}
		}
		i, j := 0, 0
		for i < n || j < m {
			switch {
			case i < n && j < m && mx[i] == my[j]:
				lines = append(lines, Line{' ', mx[i]})
				i++
				j++
			case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
				lines = append(lines, Line{'+', my[j]})
				j++
			default:
				lines = append(lines, Line{'-', mx[i]})
				i++
			}
		}
	}
	for _, l := range x[len(x)-suf:] {
		lines = append(lines, Line{' ', l})
	}
	for _, l := range lines {
		switch l.Op {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	return lines, added, removed
}

// Context keeps the changed lines and n lines around them ('~' between).
func Context(lines []Line, n int) []Line {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.Op != ' ' {
			for k := max(0, i-n); k <= min(len(lines)-1, i+n); k++ {
				keep[k] = true
			}
		}
	}
	var out []Line
	gap := false
	for i, l := range lines {
		if keep[i] {
			out = append(out, l)
			gap = false
		} else if !gap {
			out = append(out, Line{'~', ""})
			gap = true
		}
	}
	return out
}
