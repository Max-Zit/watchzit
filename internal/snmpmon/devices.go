package snmpmon

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/store"
)

// Printers, UPSs and MikroTik health sensors.
//
// Printers: the supplies of the Printer-MIB (RFC 3805) — toner, ink, drums,
// waste boxes — as a level in percent of their capacity. UPSs: the UPS-MIB
// (RFC 1628) and, where a card does not offer it, the APC PowerNet MIB:
// charge, runtime, load and whether the load runs on battery. MikroTik:
// the gauges of MIKROTIK-MIB (temperatures, voltages, fans, power) as
// plain OID checks with their unit.

var (
	prtSupplyDescr = snmp.MustOID("1.3.6.1.2.1.43.11.1.1.6")
	prtSupplyMax   = snmp.MustOID("1.3.6.1.2.1.43.11.1.1.8")
	prtSupplyLevel = snmp.MustOID("1.3.6.1.2.1.43.11.1.1.9")

	upsBatteryStatus = snmp.MustOID("1.3.6.1.2.1.33.1.2.1.0") // 1 unknown, 2 normal, 3 low, 4 depleted
	upsMinutesLeft   = snmp.MustOID("1.3.6.1.2.1.33.1.2.3.0")
	upsChargeLeft    = snmp.MustOID("1.3.6.1.2.1.33.1.2.4.0")
	upsOutputSource  = snmp.MustOID("1.3.6.1.2.1.33.1.4.1.0")     // 3 normal, 4 bypass, 5 battery, 6 booster, 7 reducer
	upsOutputLoad    = snmp.MustOID("1.3.6.1.2.1.33.1.4.4.1.5.1") // % of the first output line

	apcBatteryStatus = snmp.MustOID("1.3.6.1.4.1.318.1.1.1.2.1.1.0") // 1 unknown, 2 normal, 3 low, 4 fault
	apcCapacity      = snmp.MustOID("1.3.6.1.4.1.318.1.1.1.2.2.1.0")
	apcRuntime       = snmp.MustOID("1.3.6.1.4.1.318.1.1.1.2.2.3.0") // TimeTicks
	apcOutputStatus  = snmp.MustOID("1.3.6.1.4.1.318.1.1.1.4.1.1.0") // 2 on line, 3 on battery …
	apcOutputLoad    = snmp.MustOID("1.3.6.1.4.1.318.1.1.1.4.2.3.0")

	mtxrGaugeName  = snmp.MustOID("1.3.6.1.4.1.14988.1.1.3.100.1.2")
	mtxrGaugeValue = snmp.MustOID("1.3.6.1.4.1.14988.1.1.3.100.1.3")
	mtxrGaugeUnit  = snmp.MustOID("1.3.6.1.4.1.14988.1.1.3.100.1.4")
)

// Supply is a printer supply row (its index is two numbers: the printer
// and the supply).
type Supply struct {
	Index string // "1.2"
	Descr string
}

// Supplies lists a printer's supplies.
func Supplies(ctx context.Context, c *snmp.Client) ([]Supply, error) {
	var out []Supply
	err := c.Walk(ctx, prtSupplyDescr, func(v snmp.Variable) error {
		if len(v.OID) != len(prtSupplyDescr)+2 {
			return nil
		}
		idx := v.OID[len(prtSupplyDescr):]
		out = append(out, Supply{Index: idx.String(), Descr: cleanLabel(str(v))})
		return nil
	})
	return out, err
}

// cleanLabel trims what printers put around a supply's name.
func cleanLabel(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "\x00")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// supplyLevel turns the Printer-MIB level into percent; ok false when the
// printer does not know it (-2 unknown, -3 "some left").
func supplyLevel(level, max float64) (float64, string, bool) {
	switch {
	case level == -3:
		return 0, "some left", false
	case level < 0 || max <= 0:
		return 0, "level not reported", false
	}
	pct := level / max * 100
	if pct > 100 {
		pct = 100
	}
	return pct, fmt.Sprintf("%.0f%%", pct), true
}

// hasUPS tells which UPS MIB a device answers ("" none).
func hasUPS(ctx context.Context, c *snmp.Client) string {
	if vs, err := c.Get(ctx, upsBatteryStatus, apcBatteryStatus); err == nil {
		switch {
		case vs[0].Exists():
			return "ups-mib"
		case vs[1].Exists():
			return "apc"
		}
	}
	return ""
}

// mikrotikUnits are the gauge units of MIKROTIK-MIB: the unit shown and the
// factor the raw value is multiplied by.
var mikrotikUnits = map[int64]struct {
	unit  string
	scale float64
}{1: {"°C", 0}, 2: {"°F", 0}, 3: {"V", 0.1}, 4: {"A", 0.1}, 5: {"W", 0.1}, 6: {"", 0}, 7: {"rpm", 0}}

// healthProposals are the MikroTik gauges as OID checks.
func healthProposals(ctx context.Context, c *snmp.Client) []Proposal {
	t, rows, err := table(ctx, c, mtxrGaugeName, mtxrGaugeValue, mtxrGaugeUnit)
	if err != nil {
		return nil
	}
	var out []Proposal
	for _, i := range rows {
		name := cleanLabel(str(t[mtxrGaugeName.String()][i]))
		u, ok := mikrotikUnits[t[mtxrGaugeUnit.String()][i].Int]
		if name == "" || !ok || !t[mtxrGaugeValue.String()][i].Exists() {
			continue
		}
		label := strings.ReplaceAll(name, "-", " ")
		out = append(out, Proposal{Check: store.CheckInput{Type: store.CheckSNMPOID, Name: label,
			Params: store.CheckParams{OID: mtxrGaugeValue.Append(uint32(i)).String(), Unit: u.unit, Scale: u.scale}}, // #nosec G115 -- a table row
			Default: true, Group: "health", Detail: name})
	}
	return out
}

// devicesDiscover adds what discovery finds on printers, UPSs and MikroTik.
func devicesDiscover(ctx context.Context, c *snmp.Client, props []Proposal) []Proposal {
	if sup, err := Supplies(ctx, c); err == nil {
		for _, s := range sup {
			props = append(props, Proposal{Check: store.CheckInput{Type: store.CheckSNMPSupply, Name: s.Descr, Params: store.CheckParams{OID: s.Index, Descr: s.Descr}},
				Default: true, Group: "printer", Detail: s.Descr})
		}
	}
	if mib := hasUPS(ctx, c); mib != "" {
		props = append(props, Proposal{Check: store.CheckInput{Type: store.CheckSNMPUPS, Name: "UPS"}, Default: true, Group: "ups",
			Detail: map[string]string{"ups-mib": "UPS-MIB", "apc": "APC PowerNet"}[mib]})
	}
	return append(props, healthProposals(ctx, c)...)
}

// pollSupply reads a printer supply.
func pollSupply(ctx context.Context, c *snmp.Client, p store.CheckParams, r *Reading) error {
	idx, err := snmp.ParseOID(p.OID)
	if err != nil {
		return err
	}
	vs, err := c.Get(ctx, prtSupplyDescr.Append(idx...), prtSupplyMax.Append(idx...), prtSupplyLevel.Append(idx...))
	if err != nil {
		return err
	}
	if !vs[2].Exists() {
		return errMissing
	}
	level, _ := vs[2].Float()
	max, _ := vs[1].Float()
	pct, text, ok := supplyLevel(level, max)
	if ok {
		r.Gauges["level_pct"] = pct
	}
	r.Up, r.Text = true, text
	if ok && pct <= 0 {
		r.Up, r.Text = false, "empty"
	}
	return nil
}

// pollUPS reads a UPS: down while it runs on battery or its battery is low
// or failed.
func pollUPS(ctx context.Context, c *snmp.Client, r *Reading) error {
	vs, err := c.Get(ctx, upsBatteryStatus, upsMinutesLeft, upsChargeLeft, upsOutputSource, upsOutputLoad)
	if err != nil {
		return err
	}
	var status, source int64
	var charge, minutes, load float64
	var haveCharge, haveMinutes, haveLoad, onBattery bool
	if vs[0].Exists() {
		status, source = vs[0].Int, vs[3].Int
		charge, haveCharge = vs[2].Float()
		minutes, haveMinutes = vs[1].Float()
		load, haveLoad = vs[4].Float()
		onBattery = source == 5
	} else {
		as, err := c.Get(ctx, apcBatteryStatus, apcCapacity, apcRuntime, apcOutputStatus, apcOutputLoad)
		if err != nil {
			return err
		}
		if !as[0].Exists() {
			return errMissing
		}
		status = as[0].Int
		charge, haveCharge = as[1].Float()
		if t, ok := as[2].Float(); ok {
			minutes, haveMinutes = t/100/60, true // TimeTicks
		}
		load, haveLoad = as[4].Float()
		onBattery = as[3].Int == 3
	}
	var parts []string
	if haveCharge {
		r.Gauges["charge_pct"] = charge
		parts = append(parts, fmt.Sprintf("battery %.0f%%", charge))
	}
	if haveMinutes {
		r.Gauges["runtime_min"] = minutes
		parts = append(parts, strconv.Itoa(int(minutes))+" min")
	}
	if haveLoad {
		r.Gauges["load_pct"] = load
		parts = append(parts, fmt.Sprintf("load %.0f%%", load))
	}
	r.Gauges["on_battery"] = 0
	r.Up = true
	state := "on mains"
	switch {
	case onBattery:
		r.Gauges["on_battery"] = 1
		r.Up, state = false, "on battery"
	case status == 3:
		r.Up, state = false, "battery low"
	case status == 4:
		r.Up, state = false, "battery depleted or failed"
	}
	r.Text = strings.Join(append([]string{state}, parts...), " · ")
	return nil
}
