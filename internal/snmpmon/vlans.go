package snmpmon

import (
	"context"
	"sort"

	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/store"
)

// Q-BRIDGE-MIB (RFC 4363) and BRIDGE-MIB: VLANs of switches.
var (
	dot1dBasePortIfIndex          = snmp.MustOID("1.3.6.1.2.1.17.1.4.1.2")
	dot1qVlanCurrentEgressPorts   = snmp.MustOID("1.3.6.1.2.1.17.7.1.4.2.1.4") // .timeMark.vlan
	dot1qVlanCurrentUntaggedPorts = snmp.MustOID("1.3.6.1.2.1.17.7.1.4.2.1.5")
	dot1qVlanStaticName           = snmp.MustOID("1.3.6.1.2.1.17.7.1.4.3.1.1") // .vlan
	dot1qVlanStaticEgressPorts    = snmp.MustOID("1.3.6.1.2.1.17.7.1.4.3.1.2")
	dot1qVlanStaticUntaggedPorts  = snmp.MustOID("1.3.6.1.2.1.17.7.1.4.3.1.4")
	dot1qPvid                     = snmp.MustOID("1.3.6.1.2.1.17.7.1.4.5.1.1") // .bridgePort
)

// VLANs reads the VLANs of a switch and the ports that carry them. The
// current (operational) tables are preferred to the configured ones;
// bridge ports are turned into interface indexes. A device without a VLAN
// table gives none (its port VLAN IDs alone say too little).
func VLANs(ctx context.Context, c *snmp.Client) ([]store.VLANPort, error) {
	last := func(root snmp.OID) (map[int]snmp.Variable, error) {
		m := map[int]snmp.Variable{}
		err := c.Walk(ctx, root, func(v snmp.Variable) error {
			if len(v.OID) > len(root) {
				m[int(v.OID[len(v.OID)-1])] = v
			}
			return nil
		})
		return m, err
	}
	names, err := last(dot1qVlanStaticName)
	if err != nil {
		return nil, err
	}
	egress, err := last(dot1qVlanCurrentEgressPorts)
	if err != nil {
		return nil, err
	}
	untagged, err := last(dot1qVlanCurrentUntaggedPorts)
	if err != nil {
		return nil, err
	}
	if len(egress) == 0 {
		if egress, err = last(dot1qVlanStaticEgressPorts); err != nil {
			return nil, err
		}
		if untagged, err = last(dot1qVlanStaticUntaggedPorts); err != nil {
			return nil, err
		}
	}
	if len(names) == 0 && len(egress) == 0 {
		return ciscoVLANs(ctx, c)
	}
	base, err := last(dot1dBasePortIfIndex)
	if err != nil {
		return nil, err
	}
	pvid, err := last(dot1qPvid)
	if err != nil {
		return nil, err
	}
	ifIndex := func(port int) int {
		if v, ok := base[port]; ok && whole(v) > 0 {
			return whole(v)
		}
		return port
	}
	ids := map[int]bool{}
	for id := range names {
		ids[id] = true
	}
	for id := range egress {
		ids[id] = true
	}
	var out []store.VLANPort
	for id := range ids {
		if id < 1 || id > 4094 {
			continue
		}
		name := str(names[id])
		members := portList(egress[id].Bytes)
		plain := map[int]bool{}
		for _, p := range portList(untagged[id].Bytes) {
			plain[p] = true
		}
		// ports whose native VLAN this is count as untagged members
		for port, v := range pvid {
			if whole(v) == id && !contains(members, port) {
				members = append(members, port)
				plain[port] = true
			}
		}
		if len(members) == 0 {
			out = append(out, store.VLANPort{VLAN: id, Name: name})
		}
		for _, p := range members {
			out = append(out, store.VLANPort{VLAN: id, Name: name, IfIndex: ifIndex(p), Tagged: !plain[p]})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].VLAN != out[j].VLAN {
			return out[i].VLAN < out[j].VLAN
		}
		return out[i].IfIndex < out[j].IfIndex
	})
	return out, nil
}

// portList reads a PortList: bit 7 of the first octet is port 1.
func portList(b []byte) []int {
	var ports []int
	for i, o := range b {
		for bit := 0; bit < 8; bit++ {
			if o&(0x80>>bit) != 0 {
				ports = append(ports, i*8+bit+1)
			}
		}
	}
	return ports
}

// whole reads an INTEGER or an unsigned value (Gauge32, Unsigned32).
func whole(v snmp.Variable) int {
	if v.Kind == snmp.KindInteger {
		return int(v.Int)
	}
	return int(v.Uint) // #nosec G115 -- VLAN and port numbers
}

func contains(list []int, x int) bool {
	for _, y := range list {
		if y == x {
			return true
		}
	}
	return false
}

// CISCO-VTP-MIB and CISCO-VLAN-MEMBERSHIP-MIB: Catalyst switches, which
// report their VLANs here rather than in Q-BRIDGE-MIB.
var (
	vtpVlanName                 = snmp.MustOID("1.3.6.1.4.1.9.9.46.1.3.1.1.4") // .domain.vlan
	vlanTrunkPortVlansEnabled   = snmp.MustOID("1.3.6.1.4.1.9.9.46.1.6.1.1.4") // .ifIndex: VLANs 0–1023
	vlanTrunkPortNativeVlan     = snmp.MustOID("1.3.6.1.4.1.9.9.46.1.6.1.1.5")
	vlanTrunkPortDynamicStatus  = snmp.MustOID("1.3.6.1.4.1.9.9.46.1.6.1.1.14") // 1 trunking
	vlanTrunkPortVlansEnabled2k = snmp.MustOID("1.3.6.1.4.1.9.9.46.1.6.1.1.17") // 1024–2047
	vlanTrunkPortVlansEnabled3k = snmp.MustOID("1.3.6.1.4.1.9.9.46.1.6.1.1.18")
	vlanTrunkPortVlansEnabled4k = snmp.MustOID("1.3.6.1.4.1.9.9.46.1.6.1.1.19")
	vmVlan                      = snmp.MustOID("1.3.6.1.4.1.9.9.68.1.2.2.1.2") // .ifIndex: access VLAN
)

// ciscoVLANs reads the VLANs of a Catalyst: access ports carry their VLAN
// untagged; trunk ports carry the allowed VLANs that exist, tagged, and the
// native VLAN untagged. The reserved VLANs 1002–1005 are left out.
func ciscoVLANs(ctx context.Context, c *snmp.Client) ([]store.VLANPort, error) {
	walk := func(root snmp.OID) (map[int]snmp.Variable, error) {
		m := map[int]snmp.Variable{}
		err := c.Walk(ctx, root, func(v snmp.Variable) error {
			if len(v.OID) > len(root) {
				m[int(v.OID[len(v.OID)-1])] = v
			}
			return nil
		})
		return m, err
	}
	names, err := walk(vtpVlanName)
	if err != nil || len(names) == 0 {
		return nil, err
	}
	exists := func(id int) bool { _, ok := names[id]; return ok && (id < 1002 || id > 1005) }
	var out []store.VLANPort
	add := func(id, idx int, tagged bool) {
		if exists(id) {
			out = append(out, store.VLANPort{VLAN: id, Name: str(names[id]), IfIndex: idx, Tagged: tagged})
		}
	}
	access, err := walk(vmVlan)
	if err != nil {
		return nil, err
	}
	for idx, v := range access {
		add(whole(v), idx, false)
	}
	status, err := walk(vlanTrunkPortDynamicStatus)
	if err != nil {
		return nil, err
	}
	native, _ := walk(vlanTrunkPortNativeVlan)
	var maps [4]map[int]snmp.Variable
	for i, oid := range []snmp.OID{vlanTrunkPortVlansEnabled, vlanTrunkPortVlansEnabled2k, vlanTrunkPortVlansEnabled3k, vlanTrunkPortVlansEnabled4k} {
		if maps[i], err = walk(oid); err != nil {
			return nil, err
		}
	}
	for idx, s := range status {
		if whole(s) != 1 {
			continue
		}
		nat := whole(native[idx])
		add(nat, idx, false)
		for block, m := range maps {
			for _, bit := range portList(m[idx].Bytes) {
				if id := block*1024 + bit - 1; id != nat {
					add(id, idx, true)
				}
			}
		}
	}
	used := map[int]bool{}
	for _, v := range out {
		used[v.VLAN] = true
	}
	for id := range names {
		if exists(id) && !used[id] {
			out = append(out, store.VLANPort{VLAN: id, Name: str(names[id])})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].VLAN != out[j].VLAN {
			return out[i].VLAN < out[j].VLAN
		}
		return out[i].IfIndex < out[j].IfIndex
	})
	return out, nil
}
