package snmpmon

import (
	"context"
	"fmt"
	"testing"

	"github.com/max-zit/watchzit/internal/snmp"
)

func TestVLANs(t *testing.T) {
	a, err := snmp.NewTestAgent("pub")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	LoadTestDevice(a)
	c, err := snmp.Dial(a.Addr(), snmp.Config{Version: 2, Community: "pub"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()

	// no VLAN table: nothing, even with port VLAN IDs
	a.Set(snmp.Variable{OID: dot1qPvid.Append(1), Kind: snmp.KindGauge32, Uint: 1})
	if vs, err := VLANs(ctx, c); err != nil || len(vs) != 0 {
		t.Fatalf("no table: %v %+v", err, vs)
	}

	// bridge ports 1 and 2 are interfaces 1 and 2; VLAN 10 tagged on both,
	// VLAN 20 untagged on port 2, VLAN 30 named but on no port; port 1's
	// native VLAN is 1 (in the table)
	str := func(o snmp.OID, b []byte) { a.Set(snmp.Variable{OID: o, Kind: snmp.KindOctetString, Bytes: b}) }
	for p := uint32(1); p <= 2; p++ {
		a.Set(snmp.Variable{OID: dot1dBasePortIfIndex.Append(p), Kind: snmp.KindInteger, Int: int64(p)})
	}
	str(dot1qVlanStaticName.Append(1), []byte("default"))
	str(dot1qVlanStaticName.Append(10), []byte("mgmt"))
	str(dot1qVlanStaticName.Append(20), []byte("voice"))
	str(dot1qVlanStaticName.Append(30), []byte("spare"))
	str(dot1qVlanCurrentEgressPorts.Append(0, 10), []byte{0xC0})
	str(dot1qVlanCurrentUntaggedPorts.Append(0, 10), []byte{0x00})
	str(dot1qVlanCurrentEgressPorts.Append(0, 20), []byte{0x40})
	str(dot1qVlanCurrentUntaggedPorts.Append(0, 20), []byte{0x40})
	a.Set(snmp.Variable{OID: dot1qPvid.Append(1), Kind: snmp.KindGauge32, Uint: 1})
	vs, err := VLANs(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(vs)
	want := "[{1 default 1 false} {10 mgmt 1 true} {10 mgmt 2 true} {20 voice 2 false} {30 spare 0 false}]"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if fmt.Sprint(portList([]byte{0x81, 0x01})) != "[1 8 16]" {
		t.Fatal(portList([]byte{0x81, 0x01}))
	}
}
