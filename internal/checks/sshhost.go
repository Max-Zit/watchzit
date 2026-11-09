package checks

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/max-zit/watchzit/internal/store"
)

// ---- Linux over SSH (no agent)

// sshScript is the one command a Linux check runs: it only reads files
// under /proc and the output of df and uname. Sections are split by @@.
const sshScript = `LC_ALL=C; export LC_ALL; cat /proc/uptime; echo @@; cat /proc/loadavg; echo @@; cat /proc/meminfo; echo @@; ` +
	`head -n 1 /proc/stat; sleep 1; head -n 1 /proc/stat; echo @@; df -P -k 2>/dev/null; echo @@; uname -sr; ` +
	`grep '^PRETTY_NAME=' /etc/os-release 2>/dev/null`

// SSHHost reads processor, memory, swap, load, the fullest file system and
// the uptime of a Linux machine over SSH, with a password or a private key.
// The host key is checked against the saved fingerprint; with none saved,
// t.TrustNewKey accepts the first one (Result.HostKey has it to save).
func SSHHost(ctx context.Context, p store.CheckParams, t Target) Result {
	out, fp, err := sshRun(ctx, p, t, sshScript)
	if err != nil {
		var hk *hostKeyError
		if errors.As(err, &hk) {
			return Result{Text: hk.Error()}
		}
		return fail("%s", shortErr(err))
	}
	r, err := parseLinux(out)
	if err != nil {
		return fail("%v", err)
	}
	if p.HostKey == "" {
		r.HostKey = fp
	}
	return r
}

type hostKeyError struct {
	fp      string
	changed bool
}

func (e *hostKeyError) Error() string {
	if e.changed {
		return "the host key changed to " + e.fp + "; if the machine was reinstalled, clear the saved key in the check"
	}
	return "enter the host key of the machine in the check: " + e.fp
}

// sshAuth is the password, or the private key when the secret is one.
func sshAuth(secret string) ([]ssh.AuthMethod, error) {
	if strings.Contains(secret, "PRIVATE KEY-----") {
		signer, err := ssh.ParsePrivateKey([]byte(secret))
		if err != nil {
			return nil, errors.New("the private key cannot be read (keys with a passphrase are not supported)")
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	if secret == "" {
		return nil, errors.New("the check has no password or key")
	}
	answer := func(_, _ string, questions []string, _ []bool) ([]string, error) {
		a := make([]string, len(questions))
		for i := range a {
			a[i] = secret
		}
		return a, nil
	}
	return []ssh.AuthMethod{ssh.Password(secret), ssh.KeyboardInteractive(answer)}, nil
}

func sshRun(ctx context.Context, p store.CheckParams, t Target, cmd string) (string, string, error) {
	auth, err := sshAuth(t.Secret)
	if err != nil {
		return "", "", err
	}
	var seen string
	cfg := &ssh.ClientConfig{User: p.User, Auth: auth, Timeout: 15 * time.Second,
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seen = ssh.FingerprintSHA256(key)
			switch {
			case p.HostKey == "" && t.TrustNewKey:
				return nil
			case p.HostKey == "":
				return &hostKeyError{fp: seen}
			case seen != p.HostKey:
				return &hostKeyError{fp: seen, changed: true}
			}
			return nil
		}}
	host := t.Host
	if t.Addr.IsValid() {
		host = t.Addr.String()
	}
	addr := hostPort(host, p.Port)
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", "", err
	}
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	defer stop()
	if dl, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(dl)
	}
	sc, chans, reqs, err := ssh.NewClientConn(nc, addr, cfg)
	if err != nil {
		_ = nc.Close()
		var hk *hostKeyError
		if errors.As(err, &hk) {
			return "", seen, hk
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			return "", seen, errors.New("sign-in refused: check the user and the password or key")
		}
		return "", seen, err
	}
	conn := ssh.NewClient(sc, chans, reqs)
	defer conn.Close()
	sess, err := conn.NewSession()
	if err != nil {
		return "", seen, err
	}
	defer sess.Close()
	var stdout limitBuf
	stdout.max = 256 << 10
	sess.Stdout = &stdout
	if err := sess.Run(cmd); err != nil {
		var ee *ssh.ExitError
		if !errors.As(err, &ee) || stdout.Len() == 0 { // grep without os-release exits 1
			return "", seen, err
		}
	}
	return stdout.String(), seen, nil
}

// limitBuf keeps at most max bytes.
type limitBuf struct {
	bytes.Buffer
	max int
}

func (b *limitBuf) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room < len(p) {
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// parseLinux reads the output of sshScript.
func parseLinux(out string) (Result, error) {
	sec := strings.Split(out, "@@\n")
	if len(sec) < 6 {
		return Result{}, errors.New("the machine did not answer like Linux (/proc is missing)")
	}
	r := Result{Up: true, Values: map[string]float64{}}
	if f := strings.Fields(sec[0]); len(f) > 0 {
		if v, err := strconv.ParseFloat(f[0], 64); err == nil {
			r.Values["uptime_s"] = math.Round(v)
		}
	}
	if f := strings.Fields(sec[1]); len(f) > 0 {
		if v, err := strconv.ParseFloat(f[0], 64); err == nil {
			r.Values["load1"] = v
		}
	}
	mem := map[string]float64{}
	for _, l := range strings.Split(sec[2], "\n") {
		k, v, ok := strings.Cut(l, ":")
		if f := strings.Fields(v); ok && len(f) > 0 {
			if n, err := strconv.ParseFloat(f[0], 64); err == nil {
				mem[k] = n * 1024
			}
		}
	}
	if total := mem["MemTotal"]; total > 0 {
		avail, ok := mem["MemAvailable"]
		if !ok {
			avail = mem["MemFree"] + mem["Buffers"] + mem["Cached"]
		}
		r.Values["mem_pct"] = pct(total-avail, total)
		r.Values["mem_used_b"] = total - avail
	}
	if st := mem["SwapTotal"]; st > 0 {
		r.Values["swap_pct"] = pct(st-mem["SwapFree"], st)
	} else if _, ok := mem["SwapTotal"]; ok {
		r.Values["swap_pct"] = 0
	}
	if cpu, ok := cpuBusy(sec[3]); ok {
		r.Values["cpu_pct"] = cpu
	}
	fs, used := fullestFS(sec[4])
	if fs != "" {
		r.Values["used_pct"] = used
	}
	osName := linuxName(sec[5])
	r.Text = fmt.Sprintf("%s, CPU %.0f%%, memory %.0f%%, load %.2f", osName, r.Values["cpu_pct"], r.Values["mem_pct"], r.Values["load1"])
	if fs != "" {
		r.Text += fmt.Sprintf(", fullest %s %.0f%%", fs, used)
	}
	return r, nil
}

func pct(part, whole float64) float64 { return math.Round(part/whole*10000) / 100 }

// cpuBusy compares two "cpu" lines of /proc/stat taken a second apart.
func cpuBusy(s string) (float64, bool) {
	var rows [][]float64
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		f := strings.Fields(l)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var v []float64
		for _, x := range f[1:] {
			n, err := strconv.ParseFloat(x, 64)
			if err != nil {
				return 0, false
			}
			v = append(v, n)
		}
		rows = append(rows, v)
	}
	if len(rows) != 2 || len(rows[0]) != len(rows[1]) {
		return 0, false
	}
	var total, idle float64
	for i := range rows[0] {
		d := rows[1][i] - rows[0][i]
		if i >= 8 { // guest time is already counted in user time
			continue
		}
		total += d
		if i == 3 || i == 4 { // idle, iowait
			idle += d
		}
	}
	if total <= 0 {
		return 0, true
	}
	return math.Max(0, pct(total-idle, total)), true
}

// fullestFS is the local file system with the highest use in df -P -k
// output; memory, pseudo and network file systems are left out.
func fullestFS(s string) (string, float64) {
	skipDev := map[string]bool{"tmpfs": true, "devtmpfs": true, "udev": true, "overlay": true, "shm": true, "none": true, "squashfs": true, "efivarfs": true, "cgroup": true, "cgroup2": true, "rootfs": false}
	best, bestPct := "", -1.0
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 6 || f[0] == "Filesystem" {
			continue
		}
		dev, mnt := f[0], strings.Join(f[5:], " ")
		if skipDev[dev] || strings.HasPrefix(dev, "/dev/loop") || strings.Contains(dev, ":") || strings.HasPrefix(dev, "//") {
			continue
		}
		if mnt == "/dev" || mnt == "/proc" || mnt == "/sys" || mnt == "/run" || strings.HasPrefix(mnt, "/dev/") || strings.HasPrefix(mnt, "/proc/") ||
			strings.HasPrefix(mnt, "/sys/") || strings.HasPrefix(mnt, "/run/") || strings.HasPrefix(mnt, "/snap/") {
			continue
		}
		size, err1 := strconv.ParseFloat(f[1], 64)
		used, err2 := strconv.ParseFloat(f[2], 64)
		avail, err3 := strconv.ParseFloat(f[3], 64)
		if err1 != nil || err2 != nil || err3 != nil || size <= 0 || used+avail <= 0 {
			continue
		}
		p := pct(used, used+avail) // as df counts it: the reserved blocks are not free
		if p > bestPct {
			best, bestPct = mnt, p
		}
	}
	if best == "" {
		return "", 0
	}
	return best, bestPct
}

func linuxName(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for _, l := range lines {
		if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
			if v = strings.Trim(v, `"'`); v != "" {
				return truncate(v, 60)
			}
		}
	}
	if len(lines) > 0 && lines[0] != "" {
		return truncate(lines[0], 60)
	}
	return "Linux"
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
