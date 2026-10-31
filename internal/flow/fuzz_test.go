package flow

import (
	"net/netip"
	"testing"
)

// FuzzDecode: no datagram may crash the collector (it listens to anyone).
func FuzzDecode(f *testing.F) {
	f.Add(v5("10.0.0.5", "8.8.8.8", 100, 50000, 443))
	f.Add([]byte{0, 9, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 7, 0, 0, 0, 12, 1, 4, 0, 1, 0, 8, 0, 4})
	f.Add([]byte{0, 10, 0, 24, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 42, 0, 2, 0, 8, 1, 4, 0, 1})
	f.Add([]byte{0, 0, 0, 5, 0, 0, 0, 1, 10, 0, 0, 9, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 8})
	d := NewDecoder()
	ex := netip.MustParseAddr("10.0.0.1")
	f.Fuzz(func(t *testing.T, b []byte) {
		// templates learnt from earlier inputs stay, like on a real collector
		_, _ = d.Decode(ex, b)
	})
}
