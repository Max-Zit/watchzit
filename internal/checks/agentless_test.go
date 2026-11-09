package checks

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rc4"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/max-zit/watchzit/internal/store"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The NTLMv2 example of MS-NLMP 4.2.4.
func TestNTLMv2Vectors(t *testing.T) {
	keyNT := ntowfv2("Password", "User", "Domain")
	if want := unhex(t, "0c868a403bfd7a93a3001ef22ef02e3f"); !bytes.Equal(keyNT, want) {
		t.Fatalf("ResponseKeyNT %x", keyNT)
	}
	server := unhex(t, "0123456789abcdef")
	client := unhex(t, "aaaaaaaaaaaaaaaa")
	info := unhex(t, "02000c00"+"44006f006d00610069006e00"+"01000c00"+"530065007200760065007200"+"00000000")
	proof, resp := ntlmv2(keyNT, server, client, make([]byte, 8), info)
	if want := unhex(t, "68cd0ab851e51c96aabc927bebef6a1c"); !bytes.Equal(proof, want) {
		t.Fatalf("NTProofStr %x", proof)
	}
	if !bytes.HasSuffix(resp, append(info, 0, 0, 0, 0)) {
		t.Fatal("the response does not carry the target information")
	}
	base := hmacMD5(keyNT, proof)
	if want := unhex(t, "8de40ccadbc14a82f15cb0ad0de95ca3"); !bytes.Equal(base, want) {
		t.Fatalf("SessionBaseKey %x", base)
	}
	enc := make([]byte, 16)
	rc, _ := rc4.NewCipher(base)
	rc.XORKeyStream(enc, bytes.Repeat([]byte{0x55}, 16))
	if want := unhex(t, "c5dad2544fc9799094ce1ce90bc9d03e"); !bytes.Equal(enc, want) {
		t.Fatalf("EncryptedRandomSessionKey %x", enc)
	}
	c := &ntlmClient{flags: 0xe28a8233}
	c.keys(bytes.Repeat([]byte{0x55}, 16))
	sig, sealed := c.seal(utf16le("Plaintext"))
	if want := unhex(t, "54e50165bf1936dc996020c1811b0f06fb5f"); !bytes.Equal(sealed, want) {
		t.Fatalf("sealed %x", sealed)
	}
	if want := unhex(t, "010000007fb38ec5c55d497600000000"); !bytes.Equal(sig, want) {
		t.Fatalf("signature %x", sig)
	}
}

func TestNTLMParseRejects(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("NTLMSSP\x00"), append([]byte("NTLMSSP\x00\x02\x00\x00\x00"), make([]byte, 40)...)} {
		c := newNTLM("u", "p", "h")
		c.negotiateMessage()
		if _, err := c.authenticateMessage(b, nil, time.Now(), make([]byte, 8), make([]byte, 16)); err == nil {
			t.Errorf("accepted %x", b)
		}
	}
}

func FuzzNTLMChallenge(f *testing.F) {
	f.Add(testChallenge(time.Now()))
	f.Add([]byte("NTLMSSP\x00\x02\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, b []byte) {
		c := newNTLM(`D\u`, "p", "h")
		c.negotiateMessage()
		_, _ = c.authenticateMessage(b, nil, time.Now(), make([]byte, 8), make([]byte, 16))
	})
}

func FuzzWSItems(f *testing.F) {
	f.Add([]byte(winOS))
	f.Add([]byte(`<a><Items><b><c>1</c></b></Items></a>`))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = wsItems(b)
		_ = soapFault(b)
	})
}

func FuzzParseLinux(f *testing.F) {
	f.Add(linuxOut)
	f.Fuzz(func(t *testing.T, s string) { _, _ = parseLinux(s) })
}

// testChallenge is a challenge as Windows sends it, with a timestamp.
func testChallenge(now time.Time) []byte {
	info := writeAV([]avPair{{2, utf16le("LAB")}, {1, utf16le("WIN")}, {avTimestamp, fileTime(now)}})
	b := make([]byte, 56)
	copy(b, "NTLMSSP\x00")
	le.PutUint32(b[8:], 2)
	le.PutUint32(b[20:], ntlmWant)
	copy(b[24:], "srvchal1")
	le.PutUint16(b[40:], uint16(len(info)))
	le.PutUint16(b[42:], uint16(len(info)))
	le.PutUint32(b[44:], 56)
	return append(b, info...)
}

const linuxOut = `12345.67 2345.00
@@
0.42 0.30 0.25 1/234 5678
@@
MemTotal:        2000000 kB
MemFree:          500000 kB
MemAvailable:    1000000 kB
SwapTotal:       1000000 kB
SwapFree:         750000 kB
@@
cpu  100 0 100 800 0 0 0 0 0 0
cpu  130 0 120 840 10 0 0 0 0 0
@@
Filesystem     1024-blocks    Used Available Capacity Mounted on
udev               1000000       0   1000000       0% /dev
tmpfs               200000    1000    199000       1% /run
/dev/sda1         20000000 9000000   9000000      50% /
/dev/sdb1         10000000 8000000   2000000      80% /srv/data
/dev/loop0           50000   50000         0     100% /snap/core/1
nas:/export       90000000 89000000  1000000      99% /mnt/nas
@@
Linux 6.8.0
PRETTY_NAME="Ubuntu 24.04 LTS"
`

func TestParseLinux(t *testing.T) {
	r, err := parseLinux(linuxOut)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"uptime_s": 12346, "load1": 0.42, "mem_pct": 50, "mem_used_b": 1000000 * 1024, "swap_pct": 25, "cpu_pct": 50, "used_pct": 80}
	for k, v := range want {
		if r.Values[k] != v {
			t.Errorf("%s = %v, want %v", k, r.Values[k], v)
		}
	}
	if !r.Up || !strings.Contains(r.Text, "Ubuntu 24.04 LTS") || !strings.Contains(r.Text, "/srv/data 80%") {
		t.Errorf("text %q", r.Text)
	}
	if _, err := parseLinux("sh: cat: not found\n"); err == nil {
		t.Error("not Linux accepted")
	}
}

const winOS = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:w="http://schemas.dmtf.org/wbem/wsman/1/wsman.xsd" xmlns:n="http://schemas.xmlsoap.org/ws/2004/09/enumeration"><s:Body><n:EnumerateResponse><w:Items>` +
	`<w:XmlFragment><Caption>Microsoft Windows Server 2022 Standard</Caption><FreePhysicalMemory>1048576</FreePhysicalMemory><TotalVisibleMemorySize>4194304</TotalVisibleMemorySize>` +
	`<FreeSpaceInPagingFiles>900000</FreeSpaceInPagingFiles><SizeStoredInPagingFiles>1000000</SizeStoredInPagingFiles><LastBootUpTime><Datetime xmlns="http://schemas.dmtf.org/wbem/wscim/1/common">2026-11-08T10:00:00.5+01:00</Datetime></LastBootUpTime></w:XmlFragment>` +
	`</w:Items><w:EndOfSequence/></n:EnumerateResponse></s:Body></s:Envelope>`

func items(xml string, t *testing.T) []map[string]string {
	t.Helper()
	it, err := wsItems([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func wsEnvelope(rows ...string) string {
	return `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:w="http://schemas.dmtf.org/wbem/wsman/1/wsman.xsd"><s:Body><n:EnumerateResponse xmlns:n="http://schemas.xmlsoap.org/ws/2004/09/enumeration"><w:Items>` +
		strings.Join(rows, "") + `</w:Items><w:EndOfSequence/></n:EnumerateResponse></s:Body></s:Envelope>`
}

const (
	winCPU   = `<w:XmlFragment><LoadPercentage>20</LoadPercentage></w:XmlFragment><w:XmlFragment><LoadPercentage>40</LoadPercentage></w:XmlFragment>`
	winDisks = `<w:XmlFragment><DeviceID>C:</DeviceID><FreeSpace>25000000000</FreeSpace><Size>100000000000</Size></w:XmlFragment><w:XmlFragment><DeviceID>D:</DeviceID><FreeSpace>90</FreeSpace><Size>100</Size></w:XmlFragment>`
)

func TestWindowsResult(t *testing.T) {
	now := time.Date(2026, 11, 9, 10, 0, 0, 500000000, time.FixedZone("", 3600))
	r := windowsResult(items(winOS, t), items(wsEnvelope(winCPU), t), items(wsEnvelope(winDisks), t), now)
	want := map[string]float64{"mem_pct": 75, "swap_pct": 10, "cpu_pct": 30, "used_pct": 75, "uptime_s": 86400, "mem_used_b": 3145728 * 1024}
	for k, v := range want {
		if r.Values[k] != v {
			t.Errorf("%s = %v, want %v", k, r.Values[k], v)
		}
	}
	if !strings.HasPrefix(r.Text, "Windows Server 2022 Standard") || !strings.Contains(r.Text, "C: 75%") {
		t.Errorf("text %q", r.Text)
	}
	if msg := soapFault([]byte(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><s:Fault><s:Reason><s:Text xml:lang="en-US">Access is denied. </s:Text></s:Reason></s:Fault></s:Body></s:Envelope>`)); msg != "Access is denied." {
		t.Errorf("fault %q", msg)
	}
}

// fakeWinRM is a WinRM server that checks the NTLMv2 sign-in with the
// password and answers sealed WMI items (over HTTP) or plain ones (HTTPS).
type fakeWinRM struct {
	t        *testing.T
	password string
	queries  []string
	authed   map[string]*ntlmClient // per connection: the server's keys
}

func (f *fakeWinRM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	auth := r.Header.Get("Authorization")
	conn := r.RemoteAddr
	if tok, ok := strings.CutPrefix(auth, "Negotiate "); ok {
		msg, _ := base64.StdEncoding.DecodeString(tok)
		switch {
		case len(msg) > 12 && le.Uint32(msg[8:]) == 1:
			w.Header().Set("WWW-Authenticate", "Negotiate "+base64.StdEncoding.EncodeToString(testChallenge(time.Now())))
			w.WriteHeader(http.StatusUnauthorized)
			return
		case len(msg) > 12 && le.Uint32(msg[8:]) == 3:
			srv, err := f.verify(msg, r.TLS != nil)
			if err != nil {
				f.t.Log("sign-in refused:", err)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			f.authed[conn] = srv
		}
	}
	srv := f.authed[conn]
	if srv == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.TLS == nil {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/encrypted") {
			f.t.Error("a message over HTTP is not encrypted")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w2 := &winrm{ntlm: srv}
		var err error
		if body, err = w2.unwrap(body); err != nil {
			f.t.Error("cannot open the message:", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	q := string(body)
	var answer string
	switch {
	case strings.Contains(q, "Win32_OperatingSystem"):
		answer = winOS
	case strings.Contains(q, "Win32_Processor"):
		answer = wsEnvelope(winCPU)
	case strings.Contains(q, "Win32_LogicalDisk"):
		answer = wsEnvelope(winDisks)
	}
	f.queries = append(f.queries, q)
	if r.TLS != nil {
		w.Header().Set("Content-Type", "application/soap+xml;charset=UTF-8")
		_, _ = io.WriteString(w, answer)
		return
	}
	sig, sealed := srv.seal([]byte(answer))
	var b bytes.Buffer
	fmt.Fprintf(&b, "--Encrypted Boundary\r\n\tContent-Type: application/HTTP-SPNEGO-session-encrypted\r\n\tOriginalContent: type=application/soap+xml;charset=UTF-8;Length=%d\r\n--Encrypted Boundary\r\n\tContent-Type: application/octet-stream\r\n", len(answer))
	n := make([]byte, 4)
	le.PutUint32(n, 16)
	b.Write(n)
	b.Write(sig)
	b.Write(sealed)
	b.WriteString("--Encrypted Boundary--\r\n")
	w.Header().Set("Content-Type", `multipart/encrypted;protocol="application/HTTP-SPNEGO-session-encrypted";boundary="Encrypted Boundary"`)
	_, _ = w.Write(b.Bytes())
}

func (f *fakeWinRM) verify(msg []byte, tlsConn bool) (*ntlmClient, error) {
	field := func(at int) []byte { b, _ := ntlmField(msg, at); return b }
	nt, user, domain, encKey := field(20), field(36), field(28), field(52)
	if len(nt) < 48 {
		return nil, fmt.Errorf("short NT response")
	}
	dec := func(b []byte) string {
		r := make([]rune, len(b)/2)
		for i := range r {
			r[i] = rune(le.Uint16(b[2*i:]))
		}
		return string(r)
	}
	if dec(user) != "monitor" || dec(domain) != "LAB" {
		return nil, fmt.Errorf("user %q domain %q", dec(user), dec(domain))
	}
	keyNT := ntowfv2(f.password, dec(user), dec(domain))
	temp := nt[16:]
	proof := hmacMD5(keyNT, []byte("srvchal1"), temp)
	if !bytes.Equal(proof, nt[:16]) {
		return nil, fmt.Errorf("wrong password")
	}
	pairs, err := parseAV(temp[28:])
	if err != nil {
		return nil, err
	}
	var hasCB bool
	for _, p := range pairs {
		if p.id == avChannel && !bytes.Equal(p.val, make([]byte, 16)) {
			hasCB = true
		}
	}
	if hasCB != tlsConn {
		return nil, fmt.Errorf("channel binding %v over TLS %v", hasCB, tlsConn)
	}
	kx := hmacMD5(keyNT, proof)
	exported := make([]byte, 16)
	rc, _ := rc4.NewCipher(kx)
	rc.XORKeyStream(exported, encKey)
	c := &ntlmClient{flags: le.Uint32(msg[60:])}
	c.keys(exported)
	// the server's view: what the client sends is received, and back
	c.sendSign, c.recvSign = c.recvSign, c.sendSign
	c.sendSeal, c.recvSeal = c.recvSeal, c.sendSeal
	return c, nil
}

func winTarget(t *testing.T, url, password string) (store.CheckParams, Target) {
	t.Helper()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://"))
	n, _ := strconv.Atoi(port)
	return store.CheckParams{User: `LAB\monitor`, Port: n, TLS: strings.HasPrefix(url, "https"), Insecure: true},
		Target{Host: host, Addr: netip.MustParseAddr(host), Secret: password}
}

func TestWinRM(t *testing.T) {
	for _, secure := range []bool{false, true} {
		f := &fakeWinRM{t: t, password: "s3cret!", authed: map[string]*ntlmClient{}}
		var srv *httptest.Server
		if secure {
			srv = httptest.NewTLSServer(f)
		} else {
			srv = httptest.NewServer(f)
		}
		p, tg := winTarget(t, srv.URL, "s3cret!")
		r := WinRM(context.Background(), p, tg)
		if !r.Up || r.Values["cpu_pct"] != 30 || r.Values["used_pct"] != 75 || len(f.queries) != 3 {
			t.Errorf("https %v: %+v, %d queries", secure, r, len(f.queries))
		}
		_, tg = winTarget(t, srv.URL, "wrong")
		if r := WinRM(context.Background(), p, tg); r.Up || !strings.Contains(r.Text, "sign-in refused") {
			t.Errorf("https %v, wrong password: %+v", secure, r)
		}
		srv.Close()
	}
}

// sshServer runs a server that answers the check's command with linuxOut.
func sshServer(t *testing.T, password string, key ed25519.PrivateKey) (string, ssh.PublicKey, func()) {
	t.Helper()
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if c.User() == "monitor" && string(pw) == password {
			return nil, nil
		}
		return nil, fmt.Errorf("no")
	}, PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		pub, _ := ssh.NewPublicKey(key.Public())
		if bytes.Equal(k.Marshal(), pub.Marshal()) {
			return nil, nil
		}
		return nil, fmt.Errorf("no")
	}}
	_, hk, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(hk)
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nch := range chans {
					ch, creqs, _ := nch.Accept()
					go func() {
						for req := range creqs {
							if req.Type == "exec" {
								_ = req.Reply(true, nil)
								_, _ = io.WriteString(ch, linuxOut)
								_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
								_ = ch.Close()
								return
							}
							_ = req.Reply(false, nil)
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String(), signer.PublicKey(), func() { _ = ln.Close() }
}

func TestSSHHost(t *testing.T) {
	_, userKey, _ := ed25519.GenerateKey(rand.Reader)
	addr, hostKey, stop := sshServer(t, "pw", userKey)
	defer stop()
	host, port, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(port)
	p := store.CheckParams{User: "monitor", Port: n}
	tg := Target{Host: host, Addr: netip.MustParseAddr(host), Secret: "pw"}
	fp := ssh.FingerprintSHA256(hostKey)

	// a collector does not accept an unknown key: it shows it
	if r := Run(context.Background(), store.CheckSSH, p, tg, time.Second); r.Up || !strings.Contains(r.Text, fp) {
		t.Fatalf("unknown key: %+v", r)
	}
	tg.TrustNewKey = true
	r := Run(context.Background(), store.CheckSSH, p, tg, time.Second)
	if !r.Up || r.HostKey != fp || r.Values["used_pct"] != 80 {
		t.Fatalf("first use: %+v", r)
	}
	p.HostKey = fp
	if r := Run(context.Background(), store.CheckSSH, p, tg, time.Second); !r.Up || r.HostKey != "" {
		t.Fatalf("saved key: %+v", r)
	}
	p.HostKey = "SHA256:" + strings.Repeat("A", 43)
	if r := Run(context.Background(), store.CheckSSH, p, tg, time.Second); r.Up || !strings.Contains(r.Text, "host key changed") {
		t.Fatalf("changed key: %+v", r)
	}
	p.HostKey = fp
	tg.Secret = "wrong"
	if r := Run(context.Background(), store.CheckSSH, p, tg, time.Second); r.Up || !strings.Contains(r.Text, "sign-in refused") {
		t.Fatalf("wrong password: %+v", r)
	}
	blk, err := ssh.MarshalPrivateKey(userKey, "")
	if err != nil {
		t.Fatal(err)
	}
	tg.Secret = string(ssh.MarshalAuthorizedKey(mustPub(t, userKey))) // a public key is not a secret
	if r := Run(context.Background(), store.CheckSSH, p, tg, time.Second); r.Up {
		t.Fatal("a public key signed in")
	}
	var b bytes.Buffer
	_ = pem.Encode(&b, blk)
	tg.Secret = b.String()
	if r := Run(context.Background(), store.CheckSSH, p, tg, time.Second); !r.Up {
		t.Fatalf("private key: %+v", r)
	}
}

func mustPub(t *testing.T, k ed25519.PrivateKey) ssh.PublicKey {
	p, err := ssh.NewPublicKey(k.Public())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
