//go:build !windows

package probe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// tracer sends echo requests with a TTL over a raw ICMP socket (the
// answers of routers, Time Exceeded, do not reach datagram sockets): the
// server has CAP_NET_RAW, the agent runs as root.
type tracer struct {
	conn  *icmp.PacketConn
	p4    *ipv4.PacketConn
	p6    *ipv6.PacketConn
	dst   netip.Addr
	id    int
	proto int
	token []byte
}

func newTracer(dst netip.Addr) (*tracer, error) {
	t := &tracer{dst: dst.Unmap(), proto: 1, token: make([]byte, 8)}
	_, _ = rand.Read(t.token)
	var b [2]byte
	_, _ = rand.Read(b[:])
	t.id = int(binary.BigEndian.Uint16(b[:]))
	network, any := "ip4:icmp", "0.0.0.0"
	if !t.dst.Is4() {
		network, any, t.proto = "ip6:ipv6-icmp", "::", 58
	}
	c, err := icmp.ListenPacket(network, any)
	if err != nil {
		return nil, errors.New("traceroute needs a raw ICMP socket (root or CAP_NET_RAW)")
	}
	t.conn = c
	if t.dst.Is4() {
		t.p4 = c.IPv4PacketConn()
	} else {
		t.p6 = c.IPv6PacketConn()
	}
	return t, nil
}

func (t *tracer) close() { _ = t.conn.Close() }

func (t *tracer) setTTL(ttl int) error {
	if t.p4 != nil {
		return t.p4.SetTTL(ttl)
	}
	return t.p6.SetHopLimit(ttl)
}

// round sends one probe per TTL, 10 ms apart, and reads the answers until
// all came or wait passed after the last one.
func (t *tracer) round(ctx context.Context, round, maxTTL int, wait time.Duration) ([]probeReply, error) {
	var typ icmp.Type = ipv4.ICMPTypeEcho
	if t.p6 != nil {
		typ = ipv6.ICMPTypeEchoRequest
	}
	sentAt := make([]time.Time, maxTTL+1)
	type got struct {
		b    []byte
		from netip.Addr
		at   time.Time
	}
	ch := make(chan got, 4*maxTTL)
	stop := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 1500)
		for {
			n, from, err := t.conn.ReadFrom(buf)
			at := time.Now()
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() {
					select {
					case <-stop:
						return
					default:
						_ = t.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
						continue
					}
				}
				return
			}
			ip, _ := from.(*net.IPAddr)
			if ip == nil {
				continue
			}
			a, _ := netip.AddrFromSlice(ip.IP)
			select {
			case ch <- got{append([]byte{}, buf[:n]...), a.Unmap(), at}:
			default:
			}
		}
	}()
	defer func() {
		close(stop)
		_ = t.conn.SetReadDeadline(time.Now())
		<-readDone
	}()
	_ = t.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	dst := &net.IPAddr{IP: net.IP(t.dst.AsSlice()), Zone: t.dst.Zone()}
	var out []probeReply
	seen := map[int]bool{}
	handle := func(g got) {
		if r, ok := t.parse(g.b, round); ok && !seen[r.ttl] && r.ttl <= maxTTL && !sentAt[r.ttl].IsZero() {
			seen[r.ttl] = true
			r.from, r.rtt, r.round = g.from, g.at.Sub(sentAt[r.ttl]), round
			if r.from == t.dst {
				r.final = true
			}
			out = append(out, r)
		}
	}
	for ttl := 1; ttl <= maxTTL; ttl++ {
		if err := t.setTTL(ttl); err != nil {
			return nil, err
		}
		m := icmp.Message{Type: typ, Body: &icmp.Echo{ID: t.id, Seq: round<<8 | ttl, Data: t.token}}
		b, err := m.Marshal(nil)
		if err != nil {
			return nil, err
		}
		sentAt[ttl] = time.Now()
		if _, err := t.conn.WriteTo(b, dst); err != nil {
			var ne net.Error
			if !errors.As(err, &ne) { // "no route" and the like end the round
				return nil, err
			}
		}
		select {
		case g := <-ch:
			handle(g)
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			return out, nil
		}
	}
	end := time.NewTimer(wait)
	defer end.Stop()
	for len(seen) < maxTTL {
		select {
		case g := <-ch:
			handle(g)
		case <-end.C:
			return out, nil
		case <-ctx.Done():
			return out, nil
		}
	}
	return out, nil
}

// parse reads an echo reply of ours, or a Time Exceeded / Unreachable that
// quotes one of our requests; it returns its TTL.
func (t *tracer) parse(b []byte, round int) (probeReply, bool) {
	m, err := icmp.ParseMessage(t.proto, b)
	if err != nil {
		return probeReply{}, false
	}
	var quoted []byte
	final := false
	switch body := m.Body.(type) {
	case *icmp.Echo:
		if m.Type != ipv4.ICMPTypeEchoReply && m.Type != ipv6.ICMPTypeEchoReply {
			return probeReply{}, false
		}
		return t.match(body.ID, body.Seq, round, true)
	case *icmp.TimeExceeded:
		quoted = body.Data
	case *icmp.DstUnreach:
		quoted, final = body.Data, true
	default:
		return probeReply{}, false
	}
	// the quoted IP header, then the first 8 bytes of our echo request
	var off int
	if t.p4 != nil {
		if len(quoted) < 20 || quoted[0]>>4 != 4 {
			return probeReply{}, false
		}
		off = int(quoted[0]&0x0f) * 4
	} else {
		off = 40
	}
	if len(quoted) < off+8 {
		return probeReply{}, false
	}
	e := quoted[off:]
	if (t.p4 != nil && e[0] != 8) || (t.p6 != nil && e[0] != 128) {
		return probeReply{}, false
	}
	return t.match(int(binary.BigEndian.Uint16(e[4:])), int(binary.BigEndian.Uint16(e[6:])), round, final)
}

func (t *tracer) match(id, seq, round int, final bool) (probeReply, bool) {
	if id != t.id || seq>>8 != round {
		return probeReply{}, false
	}
	return probeReply{ttl: seq & 0xff, final: final}, true
}
