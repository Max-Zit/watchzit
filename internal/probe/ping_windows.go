//go:build windows

package probe

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var errRefused = windows.WSAECONNREFUSED

// On Windows the server pings through the ICMP helper of iphlpapi.dll,
// which needs no administrator rights (Windows is for development; the
// appliance runs Linux).
var (
	iphlpapi        = windows.NewLazySystemDLL("iphlpapi.dll")
	icmpCreateFile  = iphlpapi.NewProc("IcmpCreateFile")
	icmpSendEcho    = iphlpapi.NewProc("IcmpSendEcho")
	icmpCloseHandle = iphlpapi.NewProc("IcmpCloseHandle")
)

type pinger struct {
	h   uintptr
	dst uint32
}

func newPinger(dst netip.Addr) (*pinger, error) {
	if !dst.Is4() {
		return nil, errors.New("IPv6 ping is not available on a Windows server")
	}
	h, _, err := icmpCreateFile.Call()
	if h == uintptr(windows.InvalidHandle) {
		return nil, err
	}
	a := dst.As4()
	return &pinger{h: h, dst: binary.LittleEndian.Uint32(a[:])}, nil
}

func (p *pinger) close() { _, _, _ = icmpCloseHandle.Call(p.h) }

// icmpEchoReply is ICMP_ECHO_REPLY (64-bit layout).
type icmpEchoReply struct {
	Address       uint32
	Status        uint32
	RoundTripTime uint32
	DataSize      uint16
	Reserved      uint16
	Data          uintptr
	Options       [16]byte
}

func (p *pinger) echo(ctx context.Context, _ int, token []byte, timeout time.Duration) (time.Duration, error) {
	if d, ok := ctx.Deadline(); ok && time.Until(d) < timeout {
		timeout = time.Until(d)
	}
	if timeout < time.Millisecond {
		return 0, errTimeout
	}
	reply := make([]byte, int(unsafe.Sizeof(icmpEchoReply{}))+len(token)+64)
	start := time.Now()
	req, rep := unsafe.Pointer(&token[0]), unsafe.Pointer(&reply[0]) // #nosec G103 -- buffers for the Win32 call below
	n, _, _ := icmpSendEcho.Call(p.h, uintptr(p.dst), uintptr(req), uintptr(len(token)), 0, uintptr(rep), uintptr(len(reply)), uintptr(timeout/time.Millisecond))
	if n == 0 {
		return 0, errTimeout
	}
	r := (*icmpEchoReply)(unsafe.Pointer(&reply[0])) // #nosec G103 -- layout of ICMP_ECHO_REPLY
	if r.Status != 0 {
		return 0, errTimeout // unreachable, TTL expired …: no answer from the device
	}
	// RoundTripTime has millisecond resolution; the measured time is finer.
	return time.Since(start), nil
}
