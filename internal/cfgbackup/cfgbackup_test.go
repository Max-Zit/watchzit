package cfgbackup

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestNormalizeAndDiff(t *testing.T) {
	a := Normalize(MikroTik, "# 2026-10-30 12:00:00 by RouterOS 7.20\r\n# software id = X\r\n/ip address add address=10.0.0.1/24 interface=ether1\r\n/system identity set name=r1\r\n")
	b := Normalize(MikroTik, "# 2026-10-31 02:00:00 by RouterOS 7.20\n# software id = X\n/ip address add address=10.0.0.1/24 interface=ether1\n/system identity set name=r2\n")
	if strings.Contains(a, "by RouterOS") || Hash(a) == Hash(b) {
		t.Fatalf("normalize: %q", a)
	}
	if Hash(a) != Hash(Normalize(MikroTik, "# 2026-11-01 02:00:00 by RouterOS 7.20\n"+a)) {
		t.Fatal("only the time stamp changed, but the hash did")
	}
	lines, add, del := Diff(a, b)
	if add != 1 || del != 1 || len(lines) != 4 {
		t.Fatalf("diff: %+v %d %d", lines, add, del)
	}
	if c := Normalize(Cisco, "Building configuration...\n\nCurrent configuration : 1234 bytes\n! Last configuration change at 10:00\nhostname sw1\n"); c != "hostname sw1\n" {
		t.Fatalf("cisco: %q", c)
	}
	long := strings.Repeat("x\n", 50)
	_, add, del = Diff(long+"a\n"+long, long+"b\n"+long)
	ctx := Context(func() []Line { l, _, _ := Diff(long+"a\n"+long, long+"b\n"+long); return l }(), 2)
	if add != 1 || del != 1 || len(ctx) != 8 || ctx[0].Op != '~' {
		t.Fatalf("context: %d %+v", len(ctx), ctx)
	}
}

// sshServer answers one command with a text (password "pw").
func sshServer(t *testing.T, out string) (addr string, key ssh.Signer) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if c.User() == "backup" && string(p) == "pw" {
			return nil, nil
		}
		return nil, &ssh.PartialSuccessError{}
	}}
	cfg.AddHostKey(signer)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, rq, _ := nc.Accept()
					go func() {
						for r := range rq {
							if r.Type == "exec" {
								n := binary.BigEndian.Uint32(r.Payload)
								cmd := string(r.Payload[4 : 4+n])
								_ = r.Reply(true, nil)
								if cmd == "/export terse show-sensitive" {
									_, _ = ch.Write([]byte(out))
								}
								_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
								ch.Close()
								return
							}
							_ = r.Reply(false, nil)
						}
					}()
				}
			}(c)
		}
	}()
	return l.Addr().String(), signer
}

func TestFetch(t *testing.T) {
	addr, _ := sshServer(t, "# 2026-10-30 12:00:00 by RouterOS 7.20\r\n/system identity set name=lab\r\n")
	host, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)
	tg := Target{Address: host, Port: p, User: "backup", Password: "pw", Method: MikroTik}
	r, err := Fetch(context.Background(), tg)
	if err != nil || r.Text != "/system identity set name=lab\n" || !strings.HasPrefix(r.HostKey, "ssh-ed25519 ") {
		t.Fatalf("fetch: %+v %v", r, err)
	}
	tg.HostKey = r.HostKey
	if _, err := Fetch(context.Background(), tg); err != nil {
		t.Fatalf("with the pinned key: %v", err)
	}
	tg.HostKey = "ssh-ed25519 AAAAother"
	if _, err := Fetch(context.Background(), tg); err != ErrHostKey {
		t.Fatalf("changed host key: %v", err)
	}
	tg.HostKey, tg.Password = "", "wrong"
	if _, err := Fetch(context.Background(), tg); err == nil {
		t.Fatal("a wrong password worked")
	}
}
