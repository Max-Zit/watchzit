package web

import (
	"context"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"github.com/max-zit/watchzit/internal/cfgbackup"
	"github.com/max-zit/watchzit/internal/snmpmon"
	"github.com/max-zit/watchzit/internal/store"
)

// Where devices are plugged in, from everything the console knows, in
// order of how sure it is:
//
//  1. neighbour tables (LLDP, CDP, MikroTik): both ends and their ports;
//  2. MAC address tables: the switch port that learnt a device's MAC
//     (its own interfaces, an agent's network cards, or the MAC a router's
//     ARP table has for its address) — the access port with the fewest
//     addresses, never a port to another switch;
//  3. port descriptions (SNMP ifAlias, or the saved configuration) that
//     name another device;
//  4. saved configurations: two devices with addresses in the same
//     point-to-point network (/30, /31).

// maxEdgeMACs: a port that learnt more addresses leads to another switch.
const maxEdgeMACs = 8

type topoDev struct {
	mapDev
	macs map[string]int // MAC → its ifIndex on this device (0: not a port of it)
}

// findLinks works out the links between all devices.
func (s *Server) findLinks(ctx context.Context) ([]suggestion, error) {
	devs, err := s.store.Devices(ctx, store.DeviceFilter{})
	if err != nil {
		return nil, err
	}
	allIfs, err := s.store.AllInterfaces(ctx)
	if err != nil {
		return nil, err
	}
	topo, err := s.store.LoadTopology(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[int64]*topoDev{}
	var list []*topoDev
	var mds []*mapDev
	byAddr := map[netip.Addr]*topoDev{}
	for _, d := range devs {
		td := &topoDev{mapDev: mapDev{d: d, ifs: allIfs[d.ID], name: []string{strings.ToLower(d.Name)}}, macs: map[string]int{}}
		if d.SNMPProfile != 0 {
			if f, err := s.store.DeviceFacts(ctx, d.ID); err == nil && f.SysName != "" {
				td.name = append(td.name, strings.ToLower(f.SysName))
			}
		}
		td.addr, _ = netip.ParseAddr(d.Address)
		if td.addr.IsValid() {
			byAddr[td.addr] = td
		}
		for _, i := range td.ifs {
			if m := store.NormalizeMAC(i.MAC); m != "" {
				td.macs[m] = i.Index
			}
		}
		byID[d.ID] = td
		list = append(list, td)
		mds = append(mds, &td.mapDev)
	}
	// 1. neighbour tables
	links := matchNeighbours(mds, func(yield func(*mapDev, snmpmon.Neighbour)) {
		for _, n := range topo.Neighbours {
			if td := byID[n.DeviceID]; td != nil {
				yield(&td.mapDev, snmpmon.Neighbour{LocalIf: n.IfIndex, LocalPort: n.LocalPort, Name: n.Name, Port: n.RemotePort, PortDesc: n.RemoteDesc, Address: n.Address, MAC: n.MAC, Via: n.Via})
			}
		}
	})
	links = physicalOnly(links, byID)
	type pair struct{ a, b int64 }
	linked := map[pair]bool{}
	mark := func(a, b int64) { linked[pair{a, b}], linked[pair{b, a}] = true, true }
	for _, l := range links {
		mark(l.A, l.B)
	}
	// MACs of devices from ARP tables (printers, cameras, servers without SNMP)
	for _, a := range topo.ARP {
		if td := byAddr[a.Address]; td != nil && td.d.ID != a.DeviceID {
			if _, ok := td.macs[a.MAC]; !ok {
				td.macs[a.MAC] = 0
			}
		}
	}
	// 2. MAC address tables
	type port struct {
		dev int64
		ifx int
	}
	count := map[port]int{}
	byMAC := map[string][]port{}
	bridges := map[int64]bool{}
	for _, f := range topo.FDB {
		p := port{f.DeviceID, f.IfIndex}
		count[p]++
		byMAC[f.MAC] = append(byMAC[f.MAC], p)
		bridges[f.DeviceID] = true
	}
	uplink := map[port]bool{}
	for _, l := range links { // a port with a neighbour that is a switch itself
		if bridges[l.B] {
			uplink[port{l.A, l.AIf}] = true
		}
		if bridges[l.A] {
			uplink[port{l.B, l.BIf}] = true
		}
	}
	for _, td := range list {
		var best port
		bestN, bestIf := 0, 0
		for m, ifx := range td.macs {
			for _, p := range byMAC[m] {
				if p.dev == td.d.ID || uplink[p] || count[p] > maxEdgeMACs {
					continue
				}
				if bestN == 0 || count[p] < bestN || (count[p] == bestN && (p.dev < best.dev || (p.dev == best.dev && p.ifx < best.ifx))) {
					best, bestN, bestIf = p, count[p], ifx
				}
			}
		}
		if bestN == 0 || linked[pair{best.dev, td.d.ID}] {
			continue
		}
		sw := byID[best.dev]
		if sw == nil {
			continue
		}
		links = append(links, suggestion{A: sw.d.ID, B: td.d.ID, AIf: best.ifx, BIf: bestIf, AName: ifLabel(sw.ifs, best.ifx), BName: ifLabel(td.ifs, bestIf), Via: "MAC table"})
		mark(sw.d.ID, td.d.ID)
	}
	// descriptions: SNMP ifAlias first, then the saved configurations
	type desc struct {
		dev  *topoDev
		ifx  int
		text string
	}
	var descs []desc
	for _, td := range list {
		for _, i := range td.ifs {
			if strings.TrimSpace(i.Alias) != "" {
				descs = append(descs, desc{td, i.Index, i.Alias})
			}
		}
	}
	type p2p struct {
		dev *topoDev
		ifx int
	}
	nets := map[netip.Prefix][]p2p{}
	backups, _ := s.store.DeviceBackups(ctx)
	for _, b := range backups {
		td := byID[b.DeviceID]
		if td == nil {
			continue
		}
		v, err := s.store.LatestConfig(ctx, b.DeviceID)
		if err != nil || v == nil {
			continue
		}
		for _, p := range cfgbackup.Ports(b.Method, v.Content) {
			ifx := ifIndexOf(td.ifs, p.Interface)
			if p.Description != "" && !hasAlias(td.ifs, ifx) {
				descs = append(descs, desc{td, ifx, p.Description})
			}
			for _, pr := range p.Prefixes {
				if pr.Addr().Is4() && pr.Bits() >= 30 {
					nets[pr.Masked()] = append(nets[pr.Masked()], p2p{td, ifx})
				}
			}
		}
	}
	// 3. port descriptions that name another device
	for _, ds := range descs {
		other, oIf := namedIn(ds.text, ds.dev, list)
		if other == nil || linked[pair{ds.dev.d.ID, other.d.ID}] {
			continue
		}
		links = append(links, suggestion{A: ds.dev.d.ID, B: other.d.ID, AIf: ds.ifx, BIf: oIf, AName: ifLabel(ds.dev.ifs, ds.ifx), BName: ifLabel(other.ifs, oIf), Via: "Port description"})
		mark(ds.dev.d.ID, other.d.ID)
	}
	// 4. point-to-point networks of the configurations
	keys := make([]netip.Prefix, 0, len(nets))
	for k := range nets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	for _, k := range keys {
		ends := nets[k]
		if len(ends) != 2 || ends[0].dev == ends[1].dev || linked[pair{ends[0].dev.d.ID, ends[1].dev.d.ID}] {
			continue
		}
		a, b := ends[0], ends[1]
		links = append(links, suggestion{A: a.dev.d.ID, B: b.dev.d.ID, AIf: a.ifx, BIf: b.ifx, AName: ifLabel(a.dev.ifs, a.ifx), BName: ifLabel(b.dev.ifs, b.ifx), Via: "Configuration " + k.String()})
		mark(a.dev.d.ID, b.dev.d.ID)
	}
	return links, nil
}

func ifLabel(ifs []store.Interface, idx int) string {
	for _, i := range ifs {
		if i.Index == idx && idx > 0 {
			return i.Label()
		}
	}
	return ""
}

func ifIndexOf(ifs []store.Interface, name string) int {
	for _, i := range ifs {
		if name != "" && (strings.EqualFold(i.Name, name) || strings.EqualFold(i.Descr, name)) {
			return i.Index
		}
	}
	return 0
}

func hasAlias(ifs []store.Interface, idx int) bool {
	for _, i := range ifs {
		if i.Index == idx && strings.TrimSpace(i.Alias) != "" {
			return true
		}
	}
	return false
}

var wordSep = regexp.MustCompile(`[^\pL\pN._-]+`)

// namedIn finds the device a port description names (the longest name that
// is a whole word of it, at least three characters) and, when the text also
// names one of its ports, that port.
func namedIn(text string, self *topoDev, list []*topoDev) (*topoDev, int) {
	words := wordSep.Split(strings.ToLower(text), -1)
	has := map[string]bool{}
	for _, w := range words {
		has[strings.Trim(w, "._-")] = true
	}
	var best *topoDev
	bestLen := 0
	for _, td := range list {
		if td == self {
			continue
		}
		for _, n := range td.name {
			short, _, _ := strings.Cut(n, ".")
			for _, cand := range []string{n, short} {
				if len(cand) >= 3 && has[cand] && len(cand) > bestLen {
					best, bestLen = td, len(cand)
				}
			}
		}
	}
	if best == nil {
		return nil, 0
	}
	for _, i := range best.ifs {
		for _, n := range []string{i.Name, i.Descr} {
			if n != "" && has[strings.ToLower(n)] {
				return best, i.Index
			}
		}
	}
	return best, 0
}

// physicalIf tells whether a port is a real one (Ethernet, SFP, Wi-Fi, a
// link aggregation) and not a VLAN, tunnel, bridge or VRRP interface:
// neighbour protocols are heard over those too. Without an interface type
// (read over an API) the name decides.
func physicalIf(ifs []store.Interface, idx int) bool {
	for _, i := range ifs {
		if i.Index != idx {
			continue
		}
		switch i.Type {
		case 6, 62, 69, 71, 117, 161: // ethernetCsmacd, fastEther, fastEtherFX, ieee80211, gigabitEthernet, ieee8023adLag
			return true
		case 0:
		default:
			return false
		}
		n := strings.ToLower(i.Name)
		for _, p := range []string{"vlan", "vxlan", "vrrp", "bridge", "br-", "bond", "lo", "gre", "eoip", "ipip", "wg", "wireguard", "tun", "tap", "ovpn", "l2tp", "pppoe", "sstp", "veth", "docker", "virbr"} {
			if strings.HasPrefix(n, p) {
				return false
			}
		}
		return true
	}
	return idx == 0 // a port that is not known (0) does not count against the link
}

// physicalOnly keeps, between two devices, the links on real ports when
// there are any; otherwise one link.
func physicalOnly(links []suggestion, byID map[int64]*topoDev) []suggestion {
	type pair struct{ a, b int64 }
	key := func(l suggestion) pair {
		if l.A < l.B {
			return pair{l.A, l.B}
		}
		return pair{l.B, l.A}
	}
	phys := func(l suggestion) bool {
		a, b := byID[l.A], byID[l.B]
		return a != nil && b != nil && physicalIf(a.ifs, l.AIf) && physicalIf(b.ifs, l.BIf)
	}
	hasPhys := map[pair]bool{}
	for _, l := range links {
		if phys(l) {
			hasPhys[key(l)] = true
		}
	}
	kept := map[pair]bool{}
	out := links[:0:0]
	for _, l := range links {
		k := key(l)
		switch {
		case phys(l):
			out = append(out, l)
		case !hasPhys[k] && !kept[k]:
			out = append(out, l)
			kept[k] = true
		}
	}
	return out
}
