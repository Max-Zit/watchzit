package snmpmon

import "github.com/max-zit/watchzit/internal/snmp"

// LoadTestDevice fills a test agent with a small router: two interfaces
// (ether1 up, ether2 without link), a processor, memory and a disk. Tests
// of the poller and the console use it.
func LoadTestDevice(a *snmp.TestAgent) {
	str := func(o snmp.OID, s string) { a.Set(snmp.Variable{OID: o, Kind: snmp.KindOctetString, Bytes: []byte(s)}) }
	num := func(o snmp.OID, k snmp.Kind, v uint64) { a.Set(snmp.Variable{OID: o, Kind: k, Uint: v}) }
	integer := func(o snmp.OID, v int64) { a.Set(snmp.Variable{OID: o, Kind: snmp.KindInteger, Int: v}) }
	str(sysDescr, "RouterOS test")
	a.Set(snmp.Variable{OID: sysObjectID, Kind: snmp.KindOID, Obj: snmp.MustOID("1.3.6.1.4.1.14988.1")})
	num(sysUpTime, snmp.KindTimeTicks, 360000)
	str(sysName, "test-router")
	str(sysLocation, "Lab")
	for i, name := range []string{"ether1", "ether2"} {
		idx := uint32(i + 1)
		str(ifDescr.Append(idx), name)
		str(ifName.Append(idx), name)
		integer(ifType.Append(idx), 6)
		num(ifHighSpeed.Append(idx), snmp.KindGauge32, 1000)
		integer(ifAdminStatus.Append(idx), 1)
		integer(ifOperStatus.Append(idx), int64(i+1)) // ether1 up, ether2 down
		num(ifHCInOctets.Append(idx), snmp.KindCounter64, 0)
		num(ifHCOutOctets.Append(idx), snmp.KindCounter64, 0)
		num(ifInErrors.Append(idx), snmp.KindCounter32, 0)
		num(ifOutErrors.Append(idx), snmp.KindCounter32, 0)
	}
	str(ifAlias.Append(1), "uplink")
	integer(hrProcLoad.Append(1), 12)
	for _, s := range []struct {
		idx              uint32
		typ              snmp.OID
		descr            string
		units, size, use uint64
	}{{65536, hrStorageRAM, "main memory", 1024, 262144, 131072}, {131072, hrStorageFixedDisk, "system disk", 4096, 25000, 5000}} {
		a.Set(snmp.Variable{OID: hrStorageType.Append(s.idx), Kind: snmp.KindOID, Obj: s.typ})
		str(hrStorageDescr.Append(s.idx), s.descr)
		integer(hrStorageUnits.Append(s.idx), int64(s.units)) // #nosec G115 -- small test values
		integer(hrStorageSize.Append(s.idx), int64(s.size))   // #nosec G115 -- small test values
		integer(hrStorageUsed.Append(s.idx), int64(s.use))    // #nosec G115 -- small test values
	}
}

// SetTestTraffic sets the octet counters of a test interface.
func SetTestTraffic(a *snmp.TestAgent, idx uint32, in, out uint64) {
	a.Set(snmp.Variable{OID: ifHCInOctets.Append(idx), Kind: snmp.KindCounter64, Uint: in})
	a.Set(snmp.Variable{OID: ifHCOutOctets.Append(idx), Kind: snmp.KindCounter64, Uint: out})
}
