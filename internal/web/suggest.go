package web

import (
	"net/netip"
	"strings"

	"github.com/max-zit/watchzit/internal/snmpmon"
	"github.com/max-zit/watchzit/internal/store"
)

// suggestion is a link the neighbour tables of the devices show.
type suggestion struct {
	A     int64  `json:"a"` // device ids
	B     int64  `json:"b"`
	AIf   int    `json:"a_if,omitempty"`
	BIf   int    `json:"b_if,omitempty"`
	AName string `json:"a_name,omitempty"`
	BName string `json:"b_name,omitempty"`
	Via   string `json:"via"`
}

type mapDev struct {
	d    *store.Device
	ifs  []store.Interface
	addr netip.Addr
	name []string // names it may be known by (lower case)
}

// matchNeighbours finds the map devices the neighbours are and merges
// what both ends report about the same link.
func matchNeighbours(devs []*mapDev, each func(func(*mapDev, snmpmon.Neighbour))) []suggestion {
	byAddr := map[netip.Addr]*mapDev{}
	byName := map[string]*mapDev{}
	byMAC := map[string]*mapDev{}
	for _, d := range devs {
		if d.addr.IsValid() {
			byAddr[d.addr] = d
		}
		for _, n := range d.name {
			byName[n] = d
		}
		for _, i := range d.ifs {
			if i.MAC != "" {
				byMAC[strings.ToLower(i.MAC)] = d
			}
		}
	}
	portOf := func(d *mapDev, n snmpmon.Neighbour) (int, string) {
		// the MAC of the remote port (MikroTik, LLDP chassis = port MAC on small devices) is the surest
		if n.MAC != "" {
			for _, i := range d.ifs {
				if strings.EqualFold(i.MAC, n.MAC) {
					return i.Index, i.Label()
				}
			}
		}
		for _, i := range d.ifs {
			for _, p := range []string{n.Port, n.PortDesc} {
				if p != "" && (strings.EqualFold(i.Name, p) || strings.EqualFold(i.Descr, p) || strings.EqualFold(i.MAC, p)) {
					return i.Index, i.Label()
				}
			}
		}
		return 0, firstOf(n.Port, n.PortDesc)
	}
	ifName := func(d *mapDev, idx int) string {
		for _, i := range d.ifs {
			if i.Index == idx {
				return i.Label()
			}
		}
		return ""
	}
	var out []suggestion
	each(func(from *mapDev, n snmpmon.Neighbour) {
		to := byAddr[n.Address]
		if to == nil && n.Name != "" {
			to = byName[strings.ToLower(n.Name)]
		}
		if to == nil && n.MAC != "" {
			to = byMAC[strings.ToLower(n.MAC)]
		}
		if to == nil || to == from {
			return
		}
		bIf, bName := portOf(to, n)
		sg := suggestion{A: from.d.ID, B: to.d.ID, AIf: n.LocalIf, BIf: bIf, AName: firstOf(ifName(from, n.LocalIf), n.LocalPort), BName: bName, Via: n.Via}
		// the other end may have reported the same link already
		for i := range out {
			o := &out[i]
			if o.A == sg.B && o.B == sg.A && (o.AIf == 0 || sg.BIf == 0 || o.AIf == sg.BIf) && (o.BIf == 0 || sg.AIf == 0 || o.BIf == sg.AIf) {
				if o.AIf == 0 {
					o.AIf, o.AName = sg.BIf, sg.BName
				}
				if o.BIf == 0 {
					o.BIf, o.BName = sg.AIf, sg.AName
				}
				if !strings.Contains(o.Via, sg.Via) {
					o.Via += ", " + sg.Via
				}
				return
			}
			if o.A == sg.A && o.B == sg.B && o.AIf == sg.AIf {
				if !strings.Contains(o.Via, sg.Via) {
					o.Via += ", " + sg.Via
				}
				if o.BIf == 0 {
					o.BIf, o.BName = sg.BIf, sg.BName
				}
				return
			}
		}
		out = append(out, sg)
	})
	if out == nil {
		out = []suggestion{}
	}
	return out
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
