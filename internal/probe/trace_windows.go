//go:build windows

package probe

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net/netip"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows a traceroute uses the ICMP helper with a TTL per request: a
// router's Time Exceeded comes back as IP_TTL_EXPIRED_TRANSIT with its
// address (no administrator rights needed). IPv4 only, like ping here.
type tracer struct {
	dst   netip.Addr
	token []byte
}

// ipOptionInformation is IP_OPTION_INFORMATION (64-bit layout).
type ipOptionInformation struct {
	TTL         uint8
	TOS         uint8
	Flags       uint8
	OptionsSize uint8
	_           uint32
	OptionsData uintptr
}

const (
	ipSuccess           = 0
	ipDestNetUnreach    = 11002
	ipDestHostUnreach   = 11003
	ipDestProtUnreach   = 11004
	ipDestPortUnreach   = 11005
	ipTTLExpiredTransit = 11013
	ipDestProhibited    = 11019
)

func newTracer(dst netip.Addr) (*tracer, error) {
	dst = dst.Unmap()
	if !dst.Is4() {
		return nil, errors.New("IPv6 traceroute is not available on Windows")
	}
	t := &tracer{dst: dst, token: make([]byte, 16)}
	_, _ = rand.Read(t.token)
	return t, nil
}

func (t *tracer) close() {}

// round sends the probes of all TTLs at once, each from its own handle.
func (t *tracer) round(ctx context.Context, round, maxTTL int, wait time.Duration) ([]probeReply, error) {
	if d, ok := ctx.Deadline(); ok && time.Until(d) < wait {
		wait = time.Until(d)
	}
	if wait < time.Millisecond {
		return nil, nil
	}
	a := t.dst.As4()
	dst := binary.LittleEndian.Uint32(a[:])
	var mu sync.Mutex
	var out []probeReply
	var wg sync.WaitGroup
	for ttl := 1; ttl <= maxTTL; ttl++ {
		wg.Add(1)
		go func(ttl int) {
			defer wg.Done()
			h, _, _ := icmpCreateFile.Call()
			if h == uintptr(windows.InvalidHandle) {
				return
			}
			defer icmpCloseHandle.Call(h)               //nolint:errcheck
			opt := ipOptionInformation{TTL: uint8(ttl)} // #nosec G115 -- 1..64
			reply := make([]byte, int(unsafe.Sizeof(icmpEchoReply{}))+len(t.token)+64)
			start := time.Now()
			req, rep, op := unsafe.Pointer(&t.token[0]), unsafe.Pointer(&reply[0]), unsafe.Pointer(&opt)                                                                       // #nosec G103 -- buffers for the Win32 call below
			n, _, _ := icmpSendEcho.Call(h, uintptr(dst), uintptr(req), uintptr(len(t.token)), uintptr(op), uintptr(rep), uintptr(len(reply)), uintptr(wait/time.Millisecond)) // #nosec G115 -- the wait is under a minute
			if n == 0 {
				return
			}
			r := (*icmpEchoReply)(unsafe.Pointer(&reply[0])) // #nosec G103 -- layout of ICMP_ECHO_REPLY
			var from [4]byte
			binary.LittleEndian.PutUint32(from[:], r.Address)
			pr := probeReply{round: round, ttl: ttl, from: netip.AddrFrom4(from), rtt: time.Since(start)}
			switch r.Status {
			case ipSuccess:
				pr.final = true
			case ipTTLExpiredTransit:
			case ipDestNetUnreach, ipDestHostUnreach, ipDestProtUnreach, ipDestPortUnreach, ipDestProhibited:
				pr.final = true
			default:
				return
			}
			mu.Lock()
			out = append(out, pr)
			mu.Unlock()
		}(ttl)
		time.Sleep(10 * time.Millisecond)
	}
	wg.Wait()
	return out, nil
}
