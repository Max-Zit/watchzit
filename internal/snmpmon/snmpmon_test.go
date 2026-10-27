package snmpmon

import (
	"context"
	"testing"

	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/store"
)

func TestDiscoverAndPoll(t *testing.T) {
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
	f, ifs, props, err := Discover(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if f.Vendor != "MikroTik" || f.SysName != "test-router" {
		t.Fatalf("%+v", f)
	}
	if len(ifs) != 2 || !ifs[0].OperUp || ifs[1].OperUp || ifs[0].Speed != 1e9 || ifs[0].Alias != "uplink" {
		t.Fatalf("%+v", ifs)
	}
	def := map[string]bool{}
	for _, p := range props {
		in := p.Check
		if err := in.Clean(); err != nil {
			t.Fatal(err)
		}
		def[in.Name] = p.Default
	}
	want := map[string]bool{"SNMP": true, "CPU": true, "main memory": true, "system disk": true, "ether1": true, "ether2": false}
	for n, d := range want {
		if got, ok := def[n]; !ok || got != d {
			t.Errorf("%s: proposed %v, default %v", n, ok, got)
		}
	}

	poll := func(typ string, p store.CheckParams) Reading {
		t.Helper()
		r, err := Poll(ctx, c, &store.Check{Type: typ, Params: p})
		if err != nil {
			t.Fatal(typ, err)
		}
		return r
	}
	if r := poll(store.CheckSNMPCPU, store.CheckParams{}); r.Gauges["cpu_pct"] != 12 {
		t.Fatal(r)
	}
	if r := poll(store.CheckSNMPStor, store.CheckParams{Index: 65536, Descr: "main memory"}); r.Gauges["used_pct"] != 50 || r.Gauges["used_b"] != 128<<20 {
		t.Fatal(r)
	}
	if r := poll(store.CheckSNMPIf, store.CheckParams{IfIndex: 2}); r.Up || r.Text != "link down" {
		t.Fatal(r)
	}
	SetTestTraffic(a, 1, 1000, 2000)
	if r := poll(store.CheckSNMPIf, store.CheckParams{IfIndex: 1}); !r.Up || r.Counters["in_bps"].V != 1000 || r.Counters["in_bps"].Mul != 8 {
		t.Fatal(r)
	}
	// a storage row that moved after a reboot is found by its name
	a.Delete(hrStorageDescr.Append(131072))
	a.Set(snmp.Variable{OID: hrStorageDescr.Append(131072), Kind: snmp.KindOctetString, Bytes: []byte("other")})
	a.Set(snmp.Variable{OID: hrStorageDescr.Append(7), Kind: snmp.KindOctetString, Bytes: []byte("system disk")})
	for _, o := range []snmp.OID{hrStorageUnits, hrStorageSize, hrStorageUsed} {
		a.Set(snmp.Variable{OID: o.Append(7), Kind: snmp.KindInteger, Int: 10})
	}
	a.Set(snmp.Variable{OID: hrStorageType.Append(7), Kind: snmp.KindOID, Obj: hrStorageFixedDisk})
	r := poll(store.CheckSNMPStor, store.CheckParams{Index: 131072, Descr: "system disk"})
	if r.Params == nil || r.Params.Index != 7 || r.Gauges["used_pct"] != 100 {
		t.Fatal(r)
	}
	// custom OID with a multiplier
	if r := poll(store.CheckSNMPOID, store.CheckParams{OID: "1.3.6.1.2.1.25.3.3.1.2.1", Scale: 0.5, Unit: "x"}); r.Gauges["value"] != 6 {
		t.Fatal(r)
	}
}
