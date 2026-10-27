package snmpmon

import (
	"context"
	"net"
	"net/netip"
	"strings"

	"github.com/max-zit/watchzit/internal/snmp"
)

// Neighbour tables: LLDP-MIB (IEEE 802.1AB, most switches and routers)
// and MikroTik's own table (MNDP/CDP/LLDP neighbours of RouterOS).
var (
	lldpLocPortID   = snmp.MustOID("1.0.8802.1.1.2.1.3.7.1.3")
	lldpLocPortDesc = snmp.MustOID("1.0.8802.1.1.2.1.3.7.1.4")
	lldpRemChassis  = snmp.MustOID("1.0.8802.1.1.2.1.4.1.1.5")
	lldpRemPortSub  = snmp.MustOID("1.0.8802.1.1.2.1.4.1.1.6")
	lldpRemPortID   = snmp.MustOID("1.0.8802.1.1.2.1.4.1.1.7")
	lldpRemPortDesc = snmp.MustOID("1.0.8802.1.1.2.1.4.1.1.8")
	lldpRemSysName  = snmp.MustOID("1.0.8802.1.1.2.1.4.1.1.9")
	lldpRemManAddr  = snmp.MustOID("1.0.8802.1.1.2.1.4.2.1.3") // index carries the address

	mtxrNbAddress  = snmp.MustOID("1.3.6.1.4.1.14988.1.1.11.1.1.2")
	mtxrNbMAC      = snmp.MustOID("1.3.6.1.4.1.14988.1.1.11.1.1.3")
	mtxrNbIdentity = snmp.MustOID("1.3.6.1.4.1.14988.1.1.11.1.1.6")
	mtxrNbIf       = snmp.MustOID("1.3.6.1.4.1.14988.1.1.11.1.1.8")
)

// Neighbour is a device seen on a local port.
type Neighbour struct {
	LocalIf   int    // ifIndex on this device (0 = unknown)
	LocalPort string // its name as the protocol reports it
	Name      string // system name / identity of the neighbour
	Port      string // the neighbour's port (id or description)
	PortDesc  string
	Address   netip.Addr
	MAC       string // chassis or neighbour MAC
	Via       string // "LLDP" or "MikroTik"
}

// Neighbours reads both tables; a device without them returns none.
func Neighbours(ctx context.Context, c *snmp.Client, ifs []Interface) ([]Neighbour, error) {
	out, err := lldpNeighbours(ctx, c, ifs)
	if err != nil {
		return nil, err
	}
	mt, err := mikrotikNeighbours(ctx, c)
	if err != nil {
		return nil, err
	}
	return append(out, mt...), nil
}

// Interface is the little discovery needs to map port names to ifIndex.
type Interface struct {
	Index int
	Name  string
	Descr string
}

func ifIndexByName(ifs []Interface, names ...string) int {
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		for _, i := range ifs {
			if strings.EqualFold(i.Name, n) || strings.EqualFold(i.Descr, n) {
				return i.Index
			}
		}
	}
	return 0
}

func lldpNeighbours(ctx context.Context, c *snmp.Client, ifs []Interface) ([]Neighbour, error) {
	// local ports: lldpLocPortNum → id/description
	locID, locDesc := map[uint32]string{}, map[uint32]string{}
	for _, t := range []struct {
		col snmp.OID
		m   map[uint32]string
	}{{lldpLocPortID, locID}, {lldpLocPortDesc, locDesc}} {
		err := c.Walk(ctx, t.col, func(v snmp.Variable) error {
			if len(v.OID) == len(t.col)+1 {
				t.m[v.OID[len(t.col)]] = str(v)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	// remote rows are indexed timeMark.localPortNum.remIndex
	type key struct{ port, idx uint32 }
	rows := map[key]*Neighbour{}
	var order []key
	get := func(k key, lp uint32) *Neighbour {
		n := rows[k]
		if n == nil {
			n = &Neighbour{Via: "LLDP", LocalPort: firstNonEmpty(locID[lp], locDesc[lp])}
			n.LocalIf = ifIndexByName(ifs, locID[lp], locDesc[lp])
			if n.LocalIf == 0 {
				for _, i := range ifs {
					if i.Index == int(lp) {
						n.LocalIf = i.Index // many agents number LLDP ports by ifIndex
					}
				}
			}
			rows[k] = n
			order = append(order, k)
		}
		return n
	}
	var portSub = map[key]int64{}
	for _, col := range []snmp.OID{lldpRemChassis, lldpRemPortSub, lldpRemPortID, lldpRemPortDesc, lldpRemSysName} {
		err := c.Walk(ctx, col, func(v snmp.Variable) error {
			if len(v.OID) != len(col)+3 {
				return nil
			}
			lp, ri := v.OID[len(col)+1], v.OID[len(col)+2]
			k := key{lp, ri}
			n := get(k, lp)
			switch col.String() {
			case lldpRemChassis.String():
				if len(v.Bytes) == 6 {
					n.MAC = net.HardwareAddr(v.Bytes).String()
				}
			case lldpRemPortSub.String():
				portSub[k] = v.Int
			case lldpRemPortID.String():
				if portSub[k] == 3 && len(v.Bytes) == 6 { // MAC address port id
					n.Port = net.HardwareAddr(v.Bytes).String()
				} else {
					n.Port = str(v)
				}
			case lldpRemPortDesc.String():
				n.PortDesc = str(v)
			case lldpRemSysName.String():
				n.Name = str(v)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	// management addresses: …timeMark.port.idx.subtype.len.addr…
	err := c.Walk(ctx, lldpRemManAddr, func(v snmp.Variable) error {
		o := v.OID[len(lldpRemManAddr):]
		if len(o) >= 9 && o[3] == 1 && o[4] == 4 { // IPv4
			if n := rows[key{o[1], o[2]}]; n != nil && !n.Address.IsValid() {
				n.Address = netip.AddrFrom4([4]byte{byte(o[5]), byte(o[6]), byte(o[7]), byte(o[8])}) // #nosec G115 -- OID arcs of an address are bytes
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]Neighbour, 0, len(order))
	for _, k := range order {
		out = append(out, *rows[k])
	}
	return out, nil
}

func mikrotikNeighbours(ctx context.Context, c *snmp.Client) ([]Neighbour, error) {
	rows := map[uint32]*Neighbour{}
	var order []uint32
	for _, col := range []snmp.OID{mtxrNbAddress, mtxrNbMAC, mtxrNbIdentity, mtxrNbIf} {
		err := c.Walk(ctx, col, func(v snmp.Variable) error {
			if len(v.OID) != len(col)+1 {
				return nil
			}
			i := v.OID[len(col)]
			n := rows[i]
			if n == nil {
				n = &Neighbour{Via: "MikroTik"}
				rows[i] = n
				order = append(order, i)
			}
			switch col.String() {
			case mtxrNbAddress.String():
				if a, ok := netip.AddrFromSlice(v.Bytes); ok {
					n.Address = a.Unmap()
				}
			case mtxrNbMAC.String():
				if len(v.Bytes) == 6 {
					n.MAC = net.HardwareAddr(v.Bytes).String()
				}
			case mtxrNbIdentity.String():
				n.Name = str(v)
			case mtxrNbIf.String():
				n.LocalIf = int(v.Int)
				if v.Kind != snmp.KindInteger {
					n.LocalIf = int(v.Uint) // #nosec G115 -- ifIndex
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	out := make([]Neighbour, 0, len(order))
	for _, i := range order {
		out = append(out, *rows[i])
	}
	return out, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
