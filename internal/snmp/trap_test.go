package snmp

import (
	"net/netip"
	"strings"
	"testing"
)

// TestTraps: v1 and v2c traps and an inform are read; an inform gets its
// answer; anything else is refused.
func TestTraps(t *testing.T) {
	v2 := EncodeTrapV2("public", MustOID("1.3.6.1.6.3.1.1.5.3"), 4200,
		Variable{OID: MustOID("1.3.6.1.2.1.2.2.1.1.5"), Kind: KindInteger, Int: 5},
		Variable{OID: MustOID("1.3.6.1.2.1.2.2.1.2.5"), Kind: KindOctetString, Bytes: []byte("ether5")})
	tr, err := ParseTrap(v2)
	if err != nil || tr.Version != 2 || tr.Community != "public" || tr.Uptime != 4200 || len(tr.Vars) != 2 || tr.Inform {
		t.Fatalf("v2c: %+v %v", tr, err)
	}
	if txt := tr.Text(); !strings.HasPrefix(txt, "linkDown ") || !strings.Contains(txt, "=ether5") {
		t.Fatalf("text: %s", txt)
	}
	if tr.InformAnswer() != nil {
		t.Fatal("a trap was answered")
	}
	inf := append([]byte(nil), v2...)
	for i := range inf {
		if inf[i] == pduTrapV2 {
			inf[i] = pduInform
			break
		}
	}
	tr, err = ParseTrap(inf)
	if err != nil || !tr.Inform {
		t.Fatalf("inform: %+v %v", tr, err)
	}
	if _, _, p, err := decCommunity(tr.InformAnswer()); err != nil || p.typ != pduResponse || len(p.vars) != 4 {
		t.Fatalf("inform answer: %v", err)
	}
	v1 := EncodeTrapV1("private", MustOID("1.3.6.1.4.1.14988"), netip.MustParseAddr("10.0.0.7"), 6, 42, 100)
	tr, err = ParseTrap(v1)
	if err != nil || tr.Version != 1 || tr.Agent.String() != "10.0.0.7" || tr.TrapOID.String() != "1.3.6.1.4.1.14988.0.42" {
		t.Fatalf("v1: %+v %v", tr, err)
	}
	tr, _ = ParseTrap(EncodeTrapV1("x", MustOID("1.3.6.1.4.1.9"), netip.MustParseAddr("10.0.0.1"), 0, 0, 1))
	if tr.Text() != "coldStart" {
		t.Fatalf("generic v1: %s", tr.Text())
	}
	get := encCommunity(1, "public", &pdu{typ: pduGet, reqID: 1})
	if _, err := ParseTrap(get); err == nil {
		t.Fatal("a get request was read as a trap")
	}
	if _, err := ParseTrap([]byte{0x30, 0x80, 1}); err == nil {
		t.Fatal("garbage was read as a trap")
	}
}
