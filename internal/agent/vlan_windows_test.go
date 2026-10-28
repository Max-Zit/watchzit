package agent

import "testing"

func TestGUIDAt(t *testing.T) {
	row := make([]byte, 40)
	copy(row[12:], []byte{0x40, 0xFC, 0x29, 0x6B, 0x47, 0xCA, 0x67, 0x10, 0xB3, 0x1D, 0x00, 0xDD, 0x01, 0x06, 0x62, 0xDA})
	if g := guidAt(row, 12); g != "{6B29FC40-CA47-1067-B31D-00DD010662DA}" {
		t.Fatal(g)
	}
	_ = adapterVLANs() // reads the registry without failing
}
