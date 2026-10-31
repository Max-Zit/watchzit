package logrecv

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/store"
)

func TestParseSyslog(t *testing.T) {
	for _, c := range []struct {
		in             string
		fac, sev       int
		host, tag, msg string
	}{
		{"<34>Oct 11 22:14:15 mymachine su: 'su root' failed for lonvick on /dev/pts/8", 4, 2, "mymachine", "su", "'su root' failed for lonvick on /dev/pts/8"},
		{"<13>Feb  5 17:32:18 10.0.0.99 sshd[4321]: Accepted publickey for root", 1, 5, "10.0.0.99", "sshd", "Accepted publickey for root"},
		{"<165>1 2003-10-11T22:14:15.003Z mymachine.example.com evntslog - ID47 [exampleSDID@32473 iut=\"3\" eventSource=\"Application\"] An application event log entry", 20, 5, "mymachine.example.com", "evntslog", "An application event log entry"},
		{"<14>1 2026-10-30T12:00:00Z fw01 - - - - interface ether1 link down", 1, 6, "fw01", "", "interface ether1 link down"},
		{"<190>system,info,account user admin logged in from 10.0.0.5 via winbox", 23, 6, "", "", "system,info,account user admin logged in from 10.0.0.5 via winbox"},
		{"date=2026-10-30 time=12:00:00 devname=FGT30G logid=0100032001 msg=\"Administrator admin logged in\"", 1, 5, "", "", "date=2026-10-30 time=12:00:00 devname=FGT30G logid=0100032001 msg=\"Administrator admin logged in\""},
	} {
		f, s, h, tg, m := ParseSyslog([]byte(c.in))
		if f != c.fac || s != c.sev || h != c.host || tg != c.tag || m != c.msg {
			t.Errorf("%q:\n got %d %d %q %q %q\nwant %d %d %q %q %q", c.in, f, s, h, tg, m, c.fac, c.sev, c.host, c.tag, c.msg)
		}
	}
}

type memStore struct {
	mu  sync.Mutex
	got []store.LogMessage
}

func (m *memStore) AddLogs(_ context.Context, ms []store.LogMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.got = append(m.got, ms...)
	return nil
}
func (m *memStore) DeviceAddresses(context.Context) (map[netip.Addr]int64, error) {
	return map[netip.Addr]int64{netip.MustParseAddr("127.0.0.1"): 7}, nil
}
func (m *memStore) PruneLogs(context.Context, time.Time) (int64, error) { return 0, nil }

// TestReceive: syslog and traps over UDP reach the store with the device
// of the sender; an inform is answered; a wrong community is dropped.
func TestReceive(t *testing.T) {
	st := &memStore{}
	r := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := r.Apply(Config{Syslog: true, SyslogPort: 0, Traps: false, KeepDays: 30, Unknown: true}); err != nil {
		t.Fatal(err)
	}
	sysAddr := r.Addr(false)
	// a second receiver for traps (port 0 = any free port)
	rt := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := rt.Apply(Config{Traps: true, TrapPort: 0, Communities: []string{"wzlab"}, Unknown: true}); err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer rt.Close()
	r.Refresh(context.Background())
	rt.Refresh(context.Background())
	send := func(to net.Addr, b []byte) net.Conn {
		c, err := net.Dial("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(to.(*net.UDPAddr).Port))) // from the device address, also where :: answers as ::1
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Write(b); err != nil {
			t.Fatal(err)
		}
		return c
	}
	send(sysAddr, []byte("<11>Oct 30 12:00:00 sw1 kernel: port 5 down")).Close()
	send(rt.Addr(true), snmp.EncodeTrapV2("other", snmp.MustOID("1.3.6.1.6.3.1.1.5.3"), 1)).Close()
	trap := snmp.EncodeTrapV2("wzlab", snmp.MustOID("1.3.6.1.6.3.1.1.5.3"), 1)
	for i := range trap {
		if trap[i] == 0xa7 {
			trap[i] = 0xa6 // an inform
			break
		}
	}
	c := send(rt.Addr(true), trap)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1500)
	if n, err := c.Read(buf); err != nil || n == 0 {
		t.Fatalf("the inform was not answered: %v", err)
	}
	c.Close()
	time.Sleep(200 * time.Millisecond)
	_ = r.Flush(context.Background())
	_ = rt.Flush(context.Background())
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.got) != 2 {
		t.Fatalf("stored %d messages: %+v", len(st.got), st.got)
	}
	for _, m := range st.got {
		if m.DeviceID != 7 {
			t.Fatalf("device not found: %+v", m)
		}
		switch m.Kind {
		case store.LogSyslog:
			if m.Severity != 3 || m.Tag != "kernel" || m.Message != "port 5 down" || m.Host != "sw1" {
				t.Fatalf("syslog: %+v", m)
			}
		case store.LogTrap:
			if m.Message != "linkDown" {
				t.Fatalf("trap: %+v", m)
			}
		}
	}
}
