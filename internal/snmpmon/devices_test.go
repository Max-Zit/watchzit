package snmpmon

import (
	"context"
	"strings"
	"testing"

	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/store"
)

// TestPrinterUPSHealth: discovery offers printer supplies, the UPS and
// MikroTik gauges; supplies come as percent, the UPS is down on battery,
// and an APC card without the UPS-MIB works too.
func TestPrinterUPSHealth(t *testing.T) {
	a, err := snmp.NewTestAgent("pub")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	LoadTestDevice(a)
	str := func(o snmp.OID, s string) { a.Set(snmp.Variable{OID: o, Kind: snmp.KindOctetString, Bytes: []byte(s)}) }
	integer := func(o snmp.OID, v int64) { a.Set(snmp.Variable{OID: o, Kind: snmp.KindInteger, Int: v}) }
	str(prtSupplyDescr.Append(1, 1), "Black Toner Cartridge")
	integer(prtSupplyMax.Append(1, 1), 4000)
	integer(prtSupplyLevel.Append(1, 1), 1000)
	str(prtSupplyDescr.Append(1, 2), "Waste Toner Box")
	integer(prtSupplyMax.Append(1, 2), -2)
	integer(prtSupplyLevel.Append(1, 2), -3)
	integer(upsBatteryStatus, 2)
	integer(upsMinutesLeft, 42)
	integer(upsChargeLeft, 96)
	integer(upsOutputSource, 3)
	integer(upsOutputLoad, 31)
	str(mtxrGaugeName.Append(13), "cpu-temperature")
	integer(mtxrGaugeValue.Append(13), 48)
	integer(mtxrGaugeUnit.Append(13), 1)
	str(mtxrGaugeName.Append(8), "voltage")
	integer(mtxrGaugeValue.Append(8), 241)
	integer(mtxrGaugeUnit.Append(8), 3)

	c, err := snmp.Dial(a.Addr(), snmp.Config{Version: 2, Community: "pub"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	_, _, props, err := Discover(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.CheckInput{}
	for _, p := range props {
		in := p.Check
		if err := in.Clean(); err != nil {
			t.Fatalf("%+v: %v", p.Check, err)
		}
		got[in.Name] = in
	}
	if s := got["Black Toner Cartridge"]; s.Type != store.CheckSNMPSupply || s.Params.OID != "1.1" {
		t.Fatalf("supply %+v", s)
	}
	if u := got["UPS"]; u.Type != store.CheckSNMPUPS {
		t.Fatalf("ups %+v", got)
	}
	if v := got["voltage"]; v.Type != store.CheckSNMPOID || v.Params.Unit != "V" || v.Params.Scale != 0.1 || got["cpu temperature"].Params.Unit != "°C" {
		t.Fatalf("health %+v %+v", v, got["cpu temperature"])
	}
	poll := func(ch *store.Check) Reading {
		t.Helper()
		r, err := Poll(ctx, c, ch)
		if err != nil {
			t.Fatal(ch.Type, err)
		}
		return r
	}
	if r := poll(&store.Check{Type: store.CheckSNMPSupply, Params: store.CheckParams{OID: "1.1"}}); r.Gauges["level_pct"] != 25 || !r.Up {
		t.Fatalf("toner %+v", r)
	}
	if r := poll(&store.Check{Type: store.CheckSNMPSupply, Params: store.CheckParams{OID: "1.2"}}); len(r.Gauges) != 0 || r.Text != "some left" {
		t.Fatalf("waste box %+v", r)
	}
	if r := poll(&store.Check{Type: store.CheckSNMPUPS}); !r.Up || r.Gauges["charge_pct"] != 96 || r.Gauges["runtime_min"] != 42 || r.Gauges["load_pct"] != 31 || !strings.HasPrefix(r.Text, "on mains") {
		t.Fatalf("ups %+v", r)
	}
	integer(upsOutputSource, 5)
	if r := poll(&store.Check{Type: store.CheckSNMPUPS}); r.Up || r.Gauges["on_battery"] != 1 || !strings.HasPrefix(r.Text, "on battery") {
		t.Fatalf("on battery %+v", r)
	}
	// an APC card without the UPS-MIB
	a.Delete(upsBatteryStatus)
	integer(apcBatteryStatus, 3)
	integer(apcCapacity, 20)
	a.Set(snmp.Variable{OID: apcRuntime, Kind: snmp.KindTimeTicks, Uint: 6 * 60 * 100})
	integer(apcOutputStatus, 2)
	if r := poll(&store.Check{Type: store.CheckSNMPUPS}); r.Up || r.Gauges["runtime_min"] != 6 || r.Gauges["charge_pct"] != 20 || !strings.HasPrefix(r.Text, "battery low") {
		t.Fatalf("apc %+v", r)
	}
}
