package snmpmon

import (
	"context"
	"errors"
	"net"
	"net/netip"

	"github.com/max-zit/watchzit/internal/snmp"
)

// Where devices are plugged in, beyond the neighbour protocols: Cisco's CDP
// cache, the MAC address tables of switches (which MAC is learnt on which
// port, BRIDGE-MIB and Q-BRIDGE-MIB) and the ARP tables of routers (which
// address has which MAC).

var (
	cdpAddress = snmp.MustOID("1.3.6.1.4.1.9.9.23.1.2.1.1.4")
	cdpDevice  = snmp.MustOID("1.3.6.1.4.1.9.9.23.1.2.1.1.6")
	cdpPort    = snmp.MustOID("1.3.6.1.4.1.9.9.23.1.2.1.1.7")

	dot1dTpFdbPort   = snmp.MustOID("1.3.6.1.2.1.17.4.3.1.2")
	dot1dTpFdbStatus = snmp.MustOID("1.3.6.1.2.1.17.4.3.1.3")
	dot1qTpFdbPort   = snmp.MustOID("1.3.6.1.2.1.17.7.1.2.2.1.2")
	dot1qTpFdbStatus = snmp.MustOID("1.3.6.1.2.1.17.7.1.2.2.1.3")

	ipNetToMediaPhys    = snmp.MustOID("1.3.6.1.2.1.4.22.1.2")
	ipNetToPhysicalPhys = snmp.MustOID("1.3.6.1.2.1.4.35.1.4")
)

// MaxTable bounds the rows read of one table (a core switch may know tens
// of thousands of MACs; the rest is not needed to place devices).
const MaxTable = 50000

var errEnough = errors.New("enough rows")

// walkMax walks a column but stops after MaxTable rows.
func walkMax(ctx context.Context, c *snmp.Client, col snmp.OID, fn func(snmp.Variable)) error {
	n := 0
	err := c.Walk(ctx, col, func(v snmp.Variable) error {
		fn(v)
		if n++; n >= MaxTable {
			return errEnough
		}
		return nil
	})
	if errors.Is(err, errEnough) {
		return nil
	}
	return err
}

// cdpNeighbours reads Cisco's CDP cache (index ifIndex.deviceIndex).
func cdpNeighbours(ctx context.Context, c *snmp.Client) ([]Neighbour, error) {
	type key struct{ ifx, idx uint32 }
	rows := map[key]*Neighbour{}
	var order []key
	for _, col := range []snmp.OID{cdpAddress, cdpDevice, cdpPort} {
		err := walkMax(ctx, c, col, func(v snmp.Variable) {
			if len(v.OID) != len(col)+2 {
				return
			}
			k := key{v.OID[len(col)], v.OID[len(col)+1]}
			n := rows[k]
			if n == nil {
				n = &Neighbour{Via: "CDP", LocalIf: int(k.ifx)}
				rows[k] = n
				order = append(order, k)
			}
			switch col.String() {
			case cdpAddress.String():
				if len(v.Bytes) == 4 {
					n.Address = netip.AddrFrom4([4]byte(v.Bytes))
				}
			case cdpDevice.String():
				n.Name = str(v)
			case cdpPort.String():
				n.Port = str(v)
			}
		})
		if err != nil {
			return nil, err
		}
	}
	out := make([]Neighbour, 0, len(order))
	for _, k := range order {
		out = append(out, *rows[k])
	}
	return out, nil
}

// FDBEntry is a MAC address a switch learnt on a port.
type FDBEntry struct {
	IfIndex int
	MAC     string
	VLAN    int
}

func macOf(o snmp.OID) (string, bool) {
	if len(o) != 6 {
		return "", false
	}
	b := make([]byte, 6)
	for i, x := range o {
		if x > 255 {
			return "", false
		}
		b[i] = byte(x)
	}
	return net.HardwareAddr(b).String(), true
}

// FDB reads the MAC address table: Q-BRIDGE-MIB (with VLANs) when the
// switch has it, else BRIDGE-MIB. Only learnt entries count (not the
// switch's own addresses).
func FDB(ctx context.Context, c *snmp.Client) ([]FDBEntry, error) {
	ports := map[int64]int{} // bridge port → ifIndex
	err := walkMax(ctx, c, dot1dBasePortIfIndex, func(v snmp.Variable) {
		if len(v.OID) == len(dot1dBasePortIfIndex)+1 {
			ports[int64(v.OID[len(dot1dBasePortIfIndex)])] = int(v.Int)
		}
	})
	if err != nil {
		return nil, err
	}
	ifOf := func(bp int64) int {
		if i, ok := ports[bp]; ok {
			return i
		}
		return 0
	}
	var out []FDBEntry
	// Q-BRIDGE: index fdbId(VLAN).mac
	type qk struct {
		vlan int
		mac  string
	}
	status := map[qk]int64{}
	_ = walkMax(ctx, c, dot1qTpFdbStatus, func(v snmp.Variable) {
		o := v.OID[len(dot1qTpFdbStatus):]
		if len(o) == 7 {
			if m, ok := macOf(o[1:]); ok {
				status[qk{int(o[0]), m}] = v.Int
			}
		}
	})
	err = walkMax(ctx, c, dot1qTpFdbPort, func(v snmp.Variable) {
		o := v.OID[len(dot1qTpFdbPort):]
		if len(o) != 7 {
			return
		}
		m, ok := macOf(o[1:])
		if !ok {
			return
		}
		if st, ok := status[qk{int(o[0]), m}]; ok && st != 3 { // 3 = learned
			return
		}
		if ifx := ifOf(v.Int); ifx > 0 {
			out = append(out, FDBEntry{IfIndex: ifx, MAC: m, VLAN: int(o[0])})
		}
	})
	if err != nil {
		return nil, err
	}
	if len(out) > 0 {
		return out, nil
	}
	// BRIDGE-MIB: index mac
	st1 := map[string]int64{}
	_ = walkMax(ctx, c, dot1dTpFdbStatus, func(v snmp.Variable) {
		if m, ok := macOf(v.OID[len(dot1dTpFdbStatus):]); ok {
			st1[m] = v.Int
		}
	})
	err = walkMax(ctx, c, dot1dTpFdbPort, func(v snmp.Variable) {
		m, ok := macOf(v.OID[len(dot1dTpFdbPort):])
		if !ok {
			return
		}
		if st, ok := st1[m]; ok && st != 3 {
			return
		}
		if ifx := ifOf(v.Int); ifx > 0 {
			out = append(out, FDBEntry{IfIndex: ifx, MAC: m})
		}
	})
	return out, err
}

// ARPEntry is an address and the MAC a router or server knows for it.
type ARPEntry struct {
	IfIndex int
	Addr    netip.Addr
	MAC     string
}

// ARP reads the IPv4 ARP table (ipNetToMediaTable, else
// ipNetToPhysicalTable).
func ARP(ctx context.Context, c *snmp.Client) ([]ARPEntry, error) {
	var out []ARPEntry
	err := walkMax(ctx, c, ipNetToMediaPhys, func(v snmp.Variable) {
		o := v.OID[len(ipNetToMediaPhys):]
		if len(o) != 5 || len(v.Bytes) != 6 {
			return
		}
		a := netip.AddrFrom4([4]byte{byte(o[1]), byte(o[2]), byte(o[3]), byte(o[4])}) // #nosec G115 -- address arcs are bytes
		out = append(out, ARPEntry{IfIndex: int(o[0]), Addr: a, MAC: net.HardwareAddr(v.Bytes).String()})
	})
	if err != nil || len(out) > 0 {
		return out, err
	}
	// ipNetToPhysicalTable: ifIndex.type(1).len(4).a.b.c.d
	err = walkMax(ctx, c, ipNetToPhysicalPhys, func(v snmp.Variable) {
		o := v.OID[len(ipNetToPhysicalPhys):]
		if len(o) != 7 || o[1] != 1 || o[2] != 4 || len(v.Bytes) != 6 {
			return
		}
		a := netip.AddrFrom4([4]byte{byte(o[3]), byte(o[4]), byte(o[5]), byte(o[6])}) // #nosec G115 -- address arcs are bytes
		out = append(out, ARPEntry{IfIndex: int(o[0]), Addr: a, MAC: net.HardwareAddr(v.Bytes).String()})
	})
	return out, err
}
