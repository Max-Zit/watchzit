//go:build !windows

package probe

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

var errRefused = syscall.ECONNREFUSED

// pinger uses an unprivileged ICMP datagram socket (Linux allows it to the
// groups in net.ipv4.ping_group_range, which systemd opens to everyone) and
// falls back to a raw socket (root or CAP_NET_RAW).
type pinger struct {
	conn  *icmp.PacketConn
	dst   net.Addr
	v6    bool
	id    int
	proto int
}

func newPinger(dst netip.Addr) (*pinger, error) {
	p := &pinger{v6: !dst.Is4(), id: os.Getpid() & 0xffff, proto: 1}
	udp, raw, any := "udp4", "ip4:icmp", "0.0.0.0"
	if p.v6 {
		udp, raw, any, p.proto = "udp6", "ip6:ipv6-icmp", "::", 58
	}
	ip := net.IP(dst.AsSlice())
	if c, err := icmp.ListenPacket(udp, any); err == nil {
		p.conn, p.dst = c, &net.UDPAddr{IP: ip, Zone: dst.Zone()}
		return p, nil
	}
	c, err := icmp.ListenPacket(raw, any)
	if err != nil {
		return nil, errors.New("ICMP is not allowed for the server process (needs net.ipv4.ping_group_range or CAP_NET_RAW)")
	}
	p.conn, p.dst = c, &net.IPAddr{IP: ip, Zone: dst.Zone()}
	return p, nil
}

func (p *pinger) close() { _ = p.conn.Close() }

func (p *pinger) echo(ctx context.Context, seq int, token []byte, timeout time.Duration) (time.Duration, error) {
	var typ icmp.Type = ipv4.ICMPTypeEcho
	var reply icmp.Type = ipv4.ICMPTypeEchoReply
	if p.v6 {
		typ, reply = ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
	}
	m := icmp.Message{Type: typ, Body: &icmp.Echo{ID: p.id, Seq: seq, Data: token}}
	b, err := m.Marshal(nil)
	if err != nil {
		return 0, err
	}
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = p.conn.SetReadDeadline(deadline)
	start := time.Now()
	if _, err := p.conn.WriteTo(b, p.dst); err != nil {
		return 0, err
	}
	buf := make([]byte, 1500)
	for {
		n, _, err := p.conn.ReadFrom(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return 0, errTimeout
			}
			return 0, err
		}
		rm, err := icmp.ParseMessage(p.proto, buf[:n])
		if err != nil || rm.Type != reply {
			continue
		}
		// The kernel sets the identifier of datagram sockets itself, so the
		// random token and the sequence number identify our answer.
		if e, ok := rm.Body.(*icmp.Echo); ok && e.Seq == seq && bytes.Equal(e.Data, token) {
			return time.Since(start), nil
		}
	}
}
