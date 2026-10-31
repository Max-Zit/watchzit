// Package discovery finds the hosts of an address range: it pings every
// address, tries a few TCP ports on the ones that answer (and on all of them
// when ping is blocked), asks SNMP who they are and looks up their DNS names.
package discovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// Ports tried on every address: SSH, Telnet, HTTP, HTTPS, RDP, WinBox,
// SMB, printers (JetDirect), VNC.
var Ports = []int{22, 23, 80, 443, 3389, 8291, 445, 9100, 5900}

// MaxHosts limits a range (a /22).
const MaxHosts = 1024

// Host is an address that answered.
type Host struct {
	Address  netip.Addr
	Ping     bool
	RTT      time.Duration
	Ports    []int
	DNSName  string
	SysName  string
	SysDescr string
	Vendor   string
	SNMP     int64 // profile that answered (0: none)
}

// Name is the best name of the host.
func (h Host) Name() string {
	for _, n := range []string{h.SysName, strings.TrimSuffix(h.DNSName, ".")} {
		if n != "" {
			return n
		}
	}
	return h.Address.String()
}

// Kind guesses the device kind.
func (h Host) Kind() string {
	d := strings.ToLower(h.SysDescr + " " + h.Vendor)
	has := func(p int) bool {
		for _, x := range h.Ports {
			if x == p {
				return true
			}
		}
		return false
	}
	switch {
	case strings.Contains(d, "fortigate") || strings.Contains(d, "palo alto") || strings.Contains(d, "sophos") || strings.Contains(d, "pfsense") || strings.Contains(d, "opnsense"):
		return "firewall"
	case strings.Contains(d, "routeros") || strings.Contains(d, "mikrotik") || has(8291):
		return "router"
	case strings.Contains(d, "switch") || strings.Contains(d, "catalyst") || strings.Contains(d, "procurve") || strings.Contains(d, "aruba"):
		return "switch"
	case strings.Contains(d, "printer") || strings.Contains(d, "laserjet") || has(9100):
		return "printer"
	case strings.Contains(d, "ups") || strings.Contains(d, "apc"):
		return "ups"
	case strings.Contains(d, "windows") || has(3389):
		if strings.Contains(d, "server") {
			return "server"
		}
		return "pc"
	case strings.Contains(d, "linux") || has(22):
		return "server"
	}
	return "other"
}

// Prober does the network work (tests replace it).
type Prober struct {
	Ping     func(ctx context.Context, a netip.Addr) (time.Duration, bool)
	Dial     func(ctx context.Context, a netip.Addr, port int) bool
	Identify func(ctx context.Context, a netip.Addr) (sysName, sysDescr, vendor string, profile int64) // SNMP
	Lookup   func(ctx context.Context, a netip.Addr) string                                            // reverse DNS
	Parallel int
}

// Range returns the addresses of a CIDR range or a single address
// (without network and broadcast addresses of IPv4 ranges).
func Range(s string) ([]netip.Addr, error) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return []netip.Addr{a}, nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return nil, errors.New("enter a network such as 192.168.1.0/24 or one address")
	}
	p = p.Masked()
	if !p.Addr().Is4() {
		return nil, errors.New("only IPv4 ranges can be scanned")
	}
	if p.Bits() < 22 {
		return nil, errors.New("a range is at most a /22 (1024 addresses)")
	}
	var out []netip.Addr
	for a := p.Addr(); p.Contains(a); a = a.Next() {
		out = append(out, a)
	}
	if len(out) > 2 {
		out = out[1 : len(out)-1]
	}
	return out, nil
}

// Scan probes the addresses and returns the hosts that answered.
func (p *Prober) Scan(ctx context.Context, addrs []netip.Addr) []Host {
	par := p.Parallel
	if par <= 0 {
		par = 64
	}
	var mu sync.Mutex
	var out []Host
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	for _, a := range addrs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(a netip.Addr) {
			defer wg.Done()
			defer func() { <-sem }()
			h := Host{Address: a}
			h.RTT, h.Ping = p.Ping(ctx, a)
			for _, port := range Ports {
				if p.Dial(ctx, a, port) {
					h.Ports = append(h.Ports, port)
				}
			}
			if !h.Ping && len(h.Ports) == 0 {
				return
			}
			if p.Identify != nil {
				h.SysName, h.SysDescr, h.Vendor, h.SNMP = p.Identify(ctx, a)
			}
			if p.Lookup != nil {
				h.DNSName = p.Lookup(ctx, a)
			}
			mu.Lock()
			out = append(out, h)
			mu.Unlock()
		}(a)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].Address.Less(out[j].Address) })
	return out
}

// DialTCP tries a TCP connection with a short timeout.
func DialTCP(ctx context.Context, a netip.Addr, port int) bool {
	d := net.Dialer{Timeout: 800 * time.Millisecond}
	c, err := d.DialContext(ctx, "tcp", netip.AddrPortFrom(a, uint16(port)).String()) // #nosec G115 -- fixed ports
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// LookupName is the reverse DNS name of an address ("" when none).
func LookupName(ctx context.Context, a netip.Addr) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(ctx, a.String())
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}
