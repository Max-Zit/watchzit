package probe

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestHopsOf(t *testing.T) {
	a, b, c, d := netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.1.1"), netip.MustParseAddr("10.0.1.2"), netip.MustParseAddr("192.0.2.9")
	ms := time.Millisecond
	replies := []probeReply{
		{round: 0, ttl: 1, from: a, rtt: 1 * ms}, {round: 1, ttl: 1, from: a, rtt: 3 * ms},
		{round: 0, ttl: 2, from: b, rtt: 10 * ms}, {round: 1, ttl: 2, from: c, rtt: 12 * ms}, {round: 2, ttl: 2, from: b, rtt: 11 * ms},
		{round: 0, ttl: 4, from: d, rtt: 20 * ms, final: true}, {round: 2, ttl: 4, from: d, rtt: 30 * ms, final: true},
		{round: 0, ttl: 5, from: d, rtt: 20 * ms, final: true}, // past the destination: dropped
	}
	hops := hopsOf(replies, []int{0, 3, 3, 3, 3, 3}, 4)
	if len(hops) != 4 {
		t.Fatalf("%d hops", len(hops))
	}
	if h := hops[0]; h.Addr != a || h.Loss() < 33 || h.Loss() > 34 {
		t.Errorf("hop 1: %+v", h)
	}
	if h := hops[1]; h.Addr != b || h.Other != 1 || h.Loss() != 0 {
		t.Errorf("hop 2 (two paths): %+v", h)
	}
	if h := hops[2]; h.Addr.IsValid() || h.Loss() != 100 {
		t.Errorf("hop 3 (silent): %+v", h)
	}
	best, avg, worst, ok := hops[3].Stats()
	if !hops[3].Final || !ok || best != 20 || avg != 25 || worst != 30 {
		t.Errorf("destination: %+v %v %v %v", hops[3], best, avg, worst)
	}
}

// TestTraceLoopback: the destination answers at the first hop (where raw
// sockets are allowed; elsewhere the error must say what is missing).
func TestTraceLoopback(t *testing.T) {
	hops, err := Trace(context.Background(), netip.MustParseAddr("127.0.0.1"), 5, 2, 500*time.Millisecond)
	if err != nil {
		if strings.Contains(err.Error(), "CAP_NET_RAW") {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	if len(hops) != 1 || !hops[0].Final || hops[0].Recv != 2 {
		t.Fatalf("hops: %+v", hops)
	}
	if _, err := Trace(context.Background(), netip.MustParseAddr("127.0.0.1"), 0, 1, time.Second); err == nil {
		t.Error("0 hops accepted")
	}
}
