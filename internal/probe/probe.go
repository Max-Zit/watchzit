// Package probe measures devices from the server: ICMP echo (ping) and
// TCP connections. Later collectors (SNMP, HTTP, DNS …) live next to it.
package probe

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// Resolve turns an IP address or host name into an address (IPv4 first).
func Resolve(ctx context.Context, host string) (netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return a.Unmap(), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("cannot resolve %s", host)
	}
	for _, a := range addrs {
		if a.Unmap().Is4() {
			return a.Unmap(), nil
		}
	}
	if len(addrs) == 0 {
		return netip.Addr{}, fmt.Errorf("cannot resolve %s", host)
	}
	return addrs[0], nil
}

// PingResult is the outcome of a series of echo requests.
type PingResult struct {
	Sent, Received int
	RTTs           []time.Duration
}

// Loss is the share of lost requests in percent.
func (r PingResult) Loss() float64 {
	if r.Sent == 0 {
		return 100
	}
	return float64(r.Sent-r.Received) * 100 / float64(r.Sent)
}

// AvgRTT is the average round-trip time of the answered requests.
func (r PingResult) AvgRTT() (time.Duration, bool) {
	if len(r.RTTs) == 0 {
		return 0, false
	}
	var sum time.Duration
	for _, d := range r.RTTs {
		sum += d
	}
	return sum / time.Duration(len(r.RTTs)), true
}

// Ping sends count echo requests, 200 ms apart, each waiting up to timeout.
func Ping(ctx context.Context, dst netip.Addr, count int, timeout time.Duration) (PingResult, error) {
	var res PingResult
	p, err := newPinger(dst)
	if err != nil {
		return res, err
	}
	defer p.close()
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return res, err
	}
	for i := 0; i < count; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return res, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
		res.Sent++
		rtt, err := p.echo(ctx, i+1, token, timeout)
		if errors.Is(err, errTimeout) {
			continue
		}
		if err != nil {
			return res, err
		}
		res.Received++
		res.RTTs = append(res.RTTs, rtt)
	}
	return res, nil
}

var errTimeout = errors.New("timeout")

// TCPConnect opens and closes a TCP connection and returns how long the
// handshake took.
func TCPConnect(ctx context.Context, dst netip.Addr, port int, timeout time.Duration) (time.Duration, error) {
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", netip.AddrPortFrom(dst, uint16(port)).String()) // #nosec G115 -- port validated 1..65535
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return 0, errors.New("no answer (timeout)")
		}
		if errors.Is(err, errRefused) {
			return 0, errors.New("connection refused")
		}
		return 0, err
	}
	rtt := time.Since(start)
	_ = c.Close()
	return rtt, nil
}
