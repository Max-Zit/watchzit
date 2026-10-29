package snmpmon

import (
	"context"
	"testing"

	"github.com/max-zit/watchzit/internal/snmp"
)

func TestVRRP(t *testing.T) {
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
	if st, err := VRRP(ctx, c); err != nil || st != "" {
		t.Fatalf("no VRRP: %q %v", st, err)
	}
	// VRRPv2 router 1 on interface 2 backup, VRRPv3 router 5 master
	a.Set(snmp.Variable{OID: vrrpOperState.Append(2, 1), Kind: snmp.KindInteger, Int: 2})
	if st, _ := VRRP(ctx, c); st != "backup" {
		t.Fatalf("backup: %q", st)
	}
	a.Set(snmp.Variable{OID: vrrpv3OperationSt.Append(2, 5, 1), Kind: snmp.KindInteger, Int: 3})
	if st, _ := VRRP(ctx, c); st != "master" {
		t.Fatalf("master: %q", st)
	}
}
