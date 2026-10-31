package snmp

import (
	"net/netip"
	"testing"
)

// FuzzParseTrap: no datagram may crash the trap receiver.
func FuzzParseTrap(f *testing.F) {
	f.Add(EncodeTrapV2("public", MustOID("1.3.6.1.6.3.1.1.5.3"), 7))
	f.Add(EncodeTrapV1("public", MustOID("1.3.6.1.4.1.14988"), netip.MustParseAddr("10.0.0.1"), 6, 1, 7))
	f.Add([]byte{0x30, 0x03, 0x02, 0x01, 0x01})
	f.Fuzz(func(t *testing.T, b []byte) {
		if tr, err := ParseTrap(b); err == nil {
			_ = tr.Text()
			_ = tr.InformAnswer()
		}
	})
}
