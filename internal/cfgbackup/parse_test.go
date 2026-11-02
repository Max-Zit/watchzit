package cfgbackup

import "testing"

func TestPorts(t *testing.T) {
	mt := `# 2026-11-02 by RouterOS 7.25
/interface ethernet set [ find default-name=ether1 ] comment="uplink core-sw ether24"
/interface bridge add name=br-lab vlan-filtering=yes
/ip address add address=10.0.0.1/30 interface=ether1 network=10.0.0.0
/ip address add address=192.168.88.1/24 interface=br-lab network=192.168.88.0
`
	ps := Ports(MikroTik, mt)
	if len(ps) != 2 || ps[0].Interface != "ether1" || ps[0].Description != "uplink core-sw ether24" || ps[0].Prefixes[0].String() != "10.0.0.1/30" || ps[1].Prefixes[0].Bits() != 24 {
		t.Fatalf("mikrotik: %+v", ps)
	}
	cisco := "hostname r1\n!\ninterface GigabitEthernet0/1\n description to fw1 port2\n ip address 10.0.0.2 255.255.255.252\n!\ninterface Vlan10\n ip address 10.10.0.1 255.255.255.0\n"
	ps = Ports(Cisco, cisco)
	if len(ps) != 2 || ps[0].Description != "to fw1 port2" || ps[0].Prefixes[0].String() != "10.0.0.2/30" {
		t.Fatalf("cisco: %+v", ps)
	}
	forti := "config system interface\n    edit \"port1\"\n        set ip 10.0.0.5 255.255.255.252\n        set description \"wan r1\"\n    next\n    edit \"lan\"\n        set ip 192.168.1.1 255.255.255.0\n    next\nend\n"
	ps = Ports(FortiGate, forti)
	if len(ps) != 2 || ps[0].Interface != "port1" || ps[0].Description != "wan r1" || ps[0].Prefixes[0].String() != "10.0.0.5/30" {
		t.Fatalf("fortigate: %+v", ps)
	}
}
