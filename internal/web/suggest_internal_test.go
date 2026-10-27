package web

import (
	"net/netip"
	"testing"

	"github.com/max-zit/watchzit/internal/snmpmon"
	"github.com/max-zit/watchzit/internal/store"
)

// Two routers that see each other on two links (MikroTik table on one,
// LLDP on the other) become two suggestions with ports on both ends.
func TestMatchNeighbours(t *testing.T) {
	r1 := &mapDev{d: &store.Device{ID: 1, Name: "r1"}, addr: netip.MustParseAddr("192.168.1.1"), name: []string{"r1"},
		ifs: []store.Interface{{Index: 2, Name: "ether1", MAC: "aa:00:00:00:01:01"}, {Index: 3, Name: "ether2", MAC: "aa:00:00:00:01:02"}}}
	r2 := &mapDev{d: &store.Device{ID: 2, Name: "r2"}, addr: netip.MustParseAddr("192.168.1.2"), name: []string{"r2", "core-2"},
		ifs: []store.Interface{{Index: 5, Name: "ether1", MAC: "aa:00:00:00:02:01"}, {Index: 6, Name: "ether2", MAC: "aa:00:00:00:02:02"}}}
	seen := []struct {
		from *mapDev
		n    snmpmon.Neighbour
	}{
		{r1, snmpmon.Neighbour{LocalIf: 2, Address: netip.MustParseAddr("192.168.1.2"), MAC: "aa:00:00:00:02:01", Via: "MikroTik"}},
		{r1, snmpmon.Neighbour{LocalIf: 3, Address: netip.MustParseAddr("10.0.0.2"), MAC: "aa:00:00:00:02:02", Via: "MikroTik"}},
		{r2, snmpmon.Neighbour{LocalIf: 6, LocalPort: "ether2", Name: "r1", Port: "ether2", Via: "LLDP"}},
		{r2, snmpmon.Neighbour{LocalIf: 6, Name: "unknown-switch", Via: "LLDP"}},
	}
	got := matchNeighbours([]*mapDev{r1, r2}, func(yield func(*mapDev, snmpmon.Neighbour)) {
		for _, s := range seen {
			yield(s.from, s.n)
		}
	})
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if g := got[0]; g.A != 1 || g.B != 2 || g.AIf != 2 || g.BIf != 5 {
		t.Fatalf("first: %+v", g)
	}
	if g := got[1]; g.AIf != 3 || g.BIf != 6 || g.Via != "MikroTik, LLDP" || g.BName != "ether2" {
		t.Fatalf("second: %+v", g)
	}
}
