// Package snmpmon knows the standard MIBs WatchZit monitors over SNMP:
// what a device is (SNMPv2-MIB), its interfaces (IF-MIB), processors and
// storage (HOST-RESOURCES-MIB). It finds what can be monitored on a device
// (low-level discovery) and reads the values of the SNMP checks.
package snmpmon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/store"
)

// OIDs used here.
var (
	sysDescr    = snmp.MustOID("1.3.6.1.2.1.1.1.0")
	sysObjectID = snmp.MustOID("1.3.6.1.2.1.1.2.0")
	sysUpTime   = snmp.MustOID("1.3.6.1.2.1.1.3.0")
	sysContact  = snmp.MustOID("1.3.6.1.2.1.1.4.0")
	sysName     = snmp.MustOID("1.3.6.1.2.1.1.5.0")
	sysLocation = snmp.MustOID("1.3.6.1.2.1.1.6.0")
	hrSysUptime = snmp.MustOID("1.3.6.1.2.1.25.1.1.0")

	ifDescr       = snmp.MustOID("1.3.6.1.2.1.2.2.1.2")
	ifType        = snmp.MustOID("1.3.6.1.2.1.2.2.1.3")
	ifSpeed       = snmp.MustOID("1.3.6.1.2.1.2.2.1.5")
	ifPhysAddress = snmp.MustOID("1.3.6.1.2.1.2.2.1.6")
	ifAdminStatus = snmp.MustOID("1.3.6.1.2.1.2.2.1.7")
	ifOperStatus  = snmp.MustOID("1.3.6.1.2.1.2.2.1.8")
	ifInOctets    = snmp.MustOID("1.3.6.1.2.1.2.2.1.10")
	ifInErrors    = snmp.MustOID("1.3.6.1.2.1.2.2.1.14")
	ifOutOctets   = snmp.MustOID("1.3.6.1.2.1.2.2.1.16")
	ifOutErrors   = snmp.MustOID("1.3.6.1.2.1.2.2.1.20")
	ifName        = snmp.MustOID("1.3.6.1.2.1.31.1.1.1.1")
	ifHCInOctets  = snmp.MustOID("1.3.6.1.2.1.31.1.1.1.6")
	ifHCOutOctets = snmp.MustOID("1.3.6.1.2.1.31.1.1.1.10")
	ifHighSpeed   = snmp.MustOID("1.3.6.1.2.1.31.1.1.1.15")
	ifAlias       = snmp.MustOID("1.3.6.1.2.1.31.1.1.1.18")

	hrStorageType  = snmp.MustOID("1.3.6.1.2.1.25.2.3.1.2")
	hrStorageDescr = snmp.MustOID("1.3.6.1.2.1.25.2.3.1.3")
	hrStorageUnits = snmp.MustOID("1.3.6.1.2.1.25.2.3.1.4")
	hrStorageSize  = snmp.MustOID("1.3.6.1.2.1.25.2.3.1.5")
	hrStorageUsed  = snmp.MustOID("1.3.6.1.2.1.25.2.3.1.6")
	hrProcLoad     = snmp.MustOID("1.3.6.1.2.1.25.3.3.1.2")

	hrStorageRAM       = snmp.MustOID("1.3.6.1.2.1.25.2.1.2")
	hrStorageFixedDisk = snmp.MustOID("1.3.6.1.2.1.25.2.1.4")
	hrStorageFlash     = snmp.MustOID("1.3.6.1.2.1.25.2.1.9")
)

// Vendors by the enterprise number in sysObjectID.
var vendors = map[uint32]string{
	9: "Cisco", 11: "HP", 171: "D-Link", 253: "Xerox", 311: "Microsoft", 318: "APC", 367: "Ricoh", 534: "Eaton",
	641: "Lexmark", 674: "Dell", 1248: "Epson", 1347: "Kyocera", 1602: "Canon", 1916: "Extreme", 2011: "Huawei",
	2435: "Brother", 2636: "Juniper", 4526: "Netgear", 6876: "VMware", 8072: "Net-SNMP", 11863: "TP-Link",
	12356: "Fortinet", 14988: "MikroTik", 25461: "Palo Alto", 25506: "HPE", 30065: "Arista", 41112: "Ubiquiti",
}

// Vendor names the maker from a sysObjectID.
func Vendor(oid snmp.OID) string {
	ent := snmp.MustOID("1.3.6.1.4.1")
	if len(oid) > len(ent) && oid.HasPrefix(ent) {
		return vendors[oid[len(ent)]]
	}
	return ""
}

// Facts reads what the device says about itself.
func Facts(ctx context.Context, c *snmp.Client) (store.Facts, error) {
	vs, err := c.Get(ctx, sysDescr, sysObjectID, sysName, sysLocation, sysContact)
	if err != nil {
		return store.Facts{}, err
	}
	f := store.Facts{SysDescr: str(vs[0]), SysName: str(vs[2]), SysLocation: str(vs[3]), SysContact: str(vs[4])}
	if vs[1].Kind == snmp.KindOID {
		f.SysObjectID = vs[1].Obj.String()
		f.Vendor = Vendor(vs[1].Obj)
	}
	return f, nil
}

func str(v snmp.Variable) string {
	if !v.Exists() {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(v.String()), "\x00")
}

// table walks columns and returns values by row index (last OID arc).
func table(ctx context.Context, c *snmp.Client, cols ...snmp.OID) (map[string]map[int]snmp.Variable, []int, error) {
	out := map[string]map[int]snmp.Variable{}
	seen := map[int]bool{}
	var rows []int
	for _, col := range cols {
		m := map[int]snmp.Variable{}
		err := c.Walk(ctx, col, func(v snmp.Variable) error {
			if len(v.OID) != len(col)+1 {
				return nil
			}
			i := int(v.OID[len(col)])
			m[i] = v
			if !seen[i] {
				seen[i] = true
				rows = append(rows, i)
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
		out[col.String()] = m
	}
	return out, rows, nil
}

// Interfaces lists the network interfaces.
func Interfaces(ctx context.Context, c *snmp.Client) ([]store.Interface, error) {
	t, rows, err := table(ctx, c, ifDescr, ifType, ifSpeed, ifPhysAddress, ifAdminStatus, ifOperStatus, ifName, ifHighSpeed, ifAlias)
	if err != nil {
		return nil, err
	}
	get := func(col snmp.OID, i int) snmp.Variable { return t[col.String()][i] }
	var out []store.Interface
	for _, i := range rows {
		it := store.Interface{Index: i, Descr: str(get(ifDescr, i)), Name: str(get(ifName, i)), Alias: str(get(ifAlias, i))}
		if v := get(ifType, i); v.Kind == snmp.KindInteger {
			it.Type = int(v.Int)
		}
		if v := get(ifHighSpeed, i); v.Exists() && v.Uint > 0 {
			it.Speed = int64(v.Uint) * 1_000_000 // #nosec G115 -- Mbit/s fit easily
		} else if v := get(ifSpeed, i); v.Exists() {
			it.Speed = int64(v.Uint) // #nosec G115 -- 32-bit value
		}
		if v := get(ifPhysAddress, i); len(v.Bytes) == 6 {
			it.MAC = net.HardwareAddr(v.Bytes).String()
		}
		it.AdminUp = get(ifAdminStatus, i).Int == 1
		it.OperUp = get(ifOperStatus, i).Int == 1
		out = append(out, it)
	}
	return out, nil
}

// Storage is a row of hrStorageTable.
type Storage struct {
	Index int
	Descr string
	Kind  string // memory, disk, other
	Size  float64
}

// Storages lists memory and disks.
func Storages(ctx context.Context, c *snmp.Client) ([]Storage, error) {
	t, rows, err := table(ctx, c, hrStorageType, hrStorageDescr, hrStorageUnits, hrStorageSize)
	if err != nil {
		return nil, err
	}
	var out []Storage
	for _, i := range rows {
		s := Storage{Index: i, Descr: str(t[hrStorageDescr.String()][i]), Kind: "other"}
		typ := t[hrStorageType.String()][i].Obj
		switch {
		case typ.Compare(hrStorageRAM) == 0:
			s.Kind = "memory"
		case typ.Compare(hrStorageFixedDisk) == 0, typ.Compare(hrStorageFlash) == 0:
			s.Kind = "disk"
		}
		u, _ := t[hrStorageUnits.String()][i].Float()
		n, _ := t[hrStorageSize.String()][i].Float()
		s.Size = u * n
		out = append(out, s)
	}
	return out, nil
}

// Proposal is a check discovery suggests.
type Proposal struct {
	Check   store.CheckInput
	Default bool   // selected unless the user unticks it
	Detail  string // speed, size … for the list
	Group   string // system, interface, storage
}

// pseudo file systems that are never worth a disk check
var skipMounts = []string{"/dev", "/run", "/proc", "/sys", "/snap", "/var/lib/docker", "/boot/efi"}

// Discover finds what can be monitored on a device.
func Discover(ctx context.Context, c *snmp.Client) (store.Facts, []store.Interface, []Proposal, error) {
	f, err := Facts(ctx, c)
	if err != nil {
		return f, nil, nil, err
	}
	props := []Proposal{{Check: store.CheckInput{Type: store.CheckSNMP}, Default: true, Group: "system", Detail: f.SysDescr}}
	if loads, err := c.WalkAll(ctx, hrProcLoad); err == nil && len(loads) > 0 {
		props = append(props, Proposal{Check: store.CheckInput{Type: store.CheckSNMPCPU}, Default: true, Group: "system", Detail: fmt.Sprintf("%d × CPU", len(loads))})
	}
	if sts, err := Storages(ctx, c); err == nil {
		for _, s := range sts {
			if s.Kind == "other" || s.Size <= 0 {
				continue
			}
			def := true
			for _, p := range skipMounts {
				if s.Descr == p || strings.HasPrefix(s.Descr, p+"/") {
					def = false
				}
			}
			props = append(props, Proposal{Check: store.CheckInput{Type: store.CheckSNMPStor, Params: store.CheckParams{Index: s.Index, Descr: s.Descr}},
				Default: def, Group: s.Kind, Detail: bytesText(s.Size)})
		}
	}
	ifs, err := Interfaces(ctx, c)
	if err != nil {
		return f, nil, props, err
	}
	for _, it := range ifs {
		props = append(props, Proposal{Check: store.CheckInput{Type: store.CheckSNMPIf, Params: store.CheckParams{IfIndex: it.Index, IfName: it.Label()}},
			Default: it.AdminUp && it.OperUp && !loopback(it), Group: "interface", Detail: speedText(it.Speed) + aliasText(it.Alias)})
	}
	return f, ifs, props, nil
}

// loopback interfaces carry no traffic worth a graph
func loopback(it store.Interface) bool {
	n := strings.ToLower(it.Label())
	return it.Type == 24 || n == "lo" || strings.HasPrefix(n, "loopback")
}

func aliasText(a string) string {
	if a == "" {
		return ""
	}
	return " · " + a
}

func bytesText(b float64) string {
	u := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for b >= 1024 && i < len(u)-1 {
		b /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", b, u[i])
}

func speedText(bps int64) string {
	switch {
	case bps <= 0:
		return ""
	case bps >= 1e9 && bps%1e9 == 0:
		return fmt.Sprintf("%d Gbit/s", bps/1e9)
	case bps >= 1e6:
		return fmt.Sprintf("%d Mbit/s", bps/1e6)
	}
	return fmt.Sprintf("%d kbit/s", bps/1e3)
}

// ---- polling

// Reading is the raw outcome of one SNMP check run: gauges are stored as
// they are, counters become rates in the poller.
type Reading struct {
	Gauges   map[string]float64
	Counters map[string]Counter
	Up       bool
	Text     string
	Params   *store.CheckParams // changed parameters to save (storage renumbered)
}

// Counter is a raw counter value.
type Counter struct {
	V    uint64
	Bits int     // 32 or 64
	Mul  float64 // the rate is multiplied by it (octets → bits)
}

var errMissing = errors.New("the device does not report this value (any more)")

// Poll runs one SNMP check.
func Poll(ctx context.Context, c *snmp.Client, ch *store.Check) (Reading, error) {
	r := Reading{Gauges: map[string]float64{}, Counters: map[string]Counter{}}
	p := ch.Params
	switch ch.Type {
	case store.CheckSNMP:
		vs, err := c.Get(ctx, sysUpTime, hrSysUptime, sysName)
		if err != nil {
			return r, err
		}
		up := vs[0]
		if vs[1].Exists() {
			up = vs[1]
		}
		if up.Exists() {
			r.Gauges["uptime_s"] = float64(up.Uint / 100)
		}
		r.Up, r.Text = true, "answers"
		if up.Exists() {
			r.Text = "up " + up.String()
		}
	case store.CheckSNMPIf:
		i := uint32(p.IfIndex) // #nosec G115 -- validated positive
		vs, err := c.Get(ctx, ifHCInOctets.Append(i), ifHCOutOctets.Append(i), ifInOctets.Append(i), ifOutOctets.Append(i),
			ifInErrors.Append(i), ifOutErrors.Append(i), ifAdminStatus.Append(i), ifOperStatus.Append(i), ifHighSpeed.Append(i))
		if err != nil {
			return r, err
		}
		if !vs[7].Exists() {
			return r, fmt.Errorf("interface %d not found on the device", p.IfIndex)
		}
		if vs[0].Exists() && vs[1].Exists() {
			r.Counters["in_bps"] = Counter{vs[0].Uint, 64, 8}
			r.Counters["out_bps"] = Counter{vs[1].Uint, 64, 8}
		} else {
			r.Counters["in_bps"] = Counter{vs[2].Uint, 32, 8}
			r.Counters["out_bps"] = Counter{vs[3].Uint, 32, 8}
		}
		if vs[4].Exists() {
			r.Counters["in_err"] = Counter{vs[4].Uint, 32, 1}
		}
		if vs[5].Exists() {
			r.Counters["out_err"] = Counter{vs[5].Uint, 32, 1}
		}
		admin, oper := vs[6].Int, vs[7].Int
		switch {
		case admin == 2:
			r.Up, r.Text = true, "disabled"
		case oper == 1:
			r.Up, r.Text = true, "up"
			if vs[8].Exists() && vs[8].Uint > 0 {
				r.Text += ", " + speedText(int64(vs[8].Uint)*1_000_000) // #nosec G115 -- Mbit/s
			}
		default:
			r.Text = map[int64]string{2: "link down", 3: "testing", 5: "dormant", 6: "not present", 7: "lower layer down"}[oper]
			if r.Text == "" {
				r.Text = "down"
			}
		}
	case store.CheckSNMPCPU:
		vs, err := c.WalkAll(ctx, hrProcLoad)
		if err != nil {
			return r, err
		}
		if len(vs) == 0 {
			return r, errMissing
		}
		sum := 0.0
		for _, v := range vs {
			f, _ := v.Float()
			sum += f
		}
		avg := sum / float64(len(vs))
		r.Gauges["cpu_pct"] = avg
		r.Up, r.Text = true, fmt.Sprintf("%.0f%%", avg)
	case store.CheckSNMPStor:
		idx := p.Index
		vs, err := storageRow(ctx, c, idx)
		if err != nil {
			return r, err
		}
		if str(vs[0]) != p.Descr && p.Descr != "" {
			// rows move after a reboot (Linux mounts): find it by its name
			sts, err := Storages(ctx, c)
			if err != nil {
				return r, err
			}
			idx = 0
			for _, s := range sts {
				if s.Descr == p.Descr {
					idx = s.Index
				}
			}
			if idx == 0 {
				return r, fmt.Errorf("%q is not reported by the device", p.Descr)
			}
			if vs, err = storageRow(ctx, c, idx); err != nil {
				return r, err
			}
			np := p
			np.Index = idx
			r.Params = &np
		}
		units, _ := vs[1].Float()
		size, _ := vs[2].Float()
		used, _ := vs[3].Float()
		if size <= 0 || !vs[3].Exists() {
			return r, errMissing
		}
		r.Gauges["used_b"] = used * units
		pct := used / size * 100
		r.Gauges["used_pct"] = pct
		r.Up, r.Text = true, fmt.Sprintf("%.0f%% of %s", pct, bytesText(size*units))
	case store.CheckSNMPOID:
		o, err := snmp.ParseOID(p.OID)
		if err != nil {
			return r, err
		}
		vs, err := c.Get(ctx, o)
		if err != nil {
			return r, err
		}
		v := vs[0]
		if !v.Exists() {
			return r, errMissing
		}
		if p.Rate && v.IsCounter() {
			bits := 32
			if v.Kind == snmp.KindCounter64 {
				bits = 64
			}
			mul := p.Scale
			if mul == 0 {
				mul = 1
			}
			r.Counters["value"] = Counter{v.Uint, bits, mul}
			r.Up, r.Text = true, "counter "+v.String()
			break
		}
		f, ok := v.Float()
		if !ok {
			return r, fmt.Errorf("the value %q is not a number", truncate(v.String(), 40))
		}
		if p.Scale != 0 {
			f *= p.Scale
		}
		r.Gauges["value"] = f
		r.Up, r.Text = true, strings.TrimSpace(fmt.Sprintf("%g %s", f, p.Unit))
	default:
		return r, errors.New("unknown SNMP check")
	}
	return r, nil
}

func storageRow(ctx context.Context, c *snmp.Client, idx int) ([]snmp.Variable, error) {
	i := uint32(idx) // #nosec G115 -- validated positive
	return c.Get(ctx, hrStorageDescr.Append(i), hrStorageUnits.Append(i), hrStorageSize.Append(i), hrStorageUsed.Append(i))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
