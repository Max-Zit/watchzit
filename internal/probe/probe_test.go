package probe

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestPingLoopback(t *testing.T) {
	ctx := context.Background()
	r, err := Ping(ctx, netip.MustParseAddr("127.0.0.1"), 3, time.Second)
	if err != nil && strings.Contains(err.Error(), "not allowed") {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if r.Sent != 3 || r.Received != 3 || r.Loss() != 0 {
		t.Fatalf("%+v", r)
	}
	if avg, ok := r.AvgRTT(); !ok || avg < 0 || avg > time.Second { // loopback can measure 0 on Windows
		t.Fatalf("rtt %v", avg)
	}
}

func TestPingNoAnswer(t *testing.T) {
	// TEST-NET-1 (RFC 5737) is never routed.
	r, err := Ping(context.Background(), netip.MustParseAddr("192.0.2.1"), 2, 300*time.Millisecond)
	if err != nil && strings.Contains(err.Error(), "not allowed") {
		t.Skip(err)
	}
	if err == nil && r.Received != 0 {
		t.Fatalf("answer from TEST-NET: %+v", r)
	}
	if r.Loss() != 100 {
		t.Fatalf("loss %v", r.Loss())
	}
}

func TestTCPConnect(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	lo := netip.MustParseAddr("127.0.0.1")
	if _, err := TCPConnect(context.Background(), lo, port, time.Second); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	_, err = TCPConnect(context.Background(), lo, port, time.Second)
	if err == nil || err.Error() != "connection refused" {
		t.Fatalf("closed port: %v", err)
	}
}

func TestResolve(t *testing.T) {
	a, err := Resolve(context.Background(), "127.0.0.1")
	if err != nil || a.String() != "127.0.0.1" {
		t.Fatal(a, err)
	}
	a, err = Resolve(context.Background(), "localhost")
	if err != nil || !a.IsLoopback() {
		t.Fatal(a, err)
	}
}
