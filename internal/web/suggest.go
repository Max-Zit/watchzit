package web

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/probe"
	"github.com/max-zit/watchzit/internal/snmp"
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

// handleMapSuggest asks the SNMP devices of a map for their neighbours
// and turns those that are on the map into proposed links.
func (s *Server) handleMapSuggest(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	l, err := s.store.MapLayout(ctx, m.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var devs []*mapDev
	for _, n := range l.Nodes {
		if n.Kind != store.NodeDevice {
			continue
		}
		d, err := s.store.DeviceByID(ctx, n.DeviceID)
		if err != nil {
			continue
		}
		md := &mapDev{d: d, name: []string{strings.ToLower(d.Name)}}
		md.ifs, _ = s.store.Interfaces(ctx, d.ID)
		if f, err := s.store.DeviceFacts(ctx, d.ID); err == nil && f.SysName != "" {
			md.name = append(md.name, strings.ToLower(f.SysName))
		}
		md.addr, _ = probe.Resolve(ctx, d.Address)
		devs = append(devs, md)
	}
	type seen struct {
		from *mapDev
		nb   []snmpmon.Neighbour
	}
	var mu sync.Mutex
	var found []seen
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, md := range devs {
		if md.d.SNMPProfile == 0 || !md.addr.IsValid() || md.d.Paused {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			pr, err := s.store.DeviceSNMP(ctx, md.d.ID)
			if err != nil {
				return
			}
			cfg, err := s.store.SNMPConfig(pr)
			if err != nil {
				return
			}
			cfg.Timeout = 3 * time.Second
			c, err := snmp.Dial(netip.AddrPortFrom(md.addr, uint16(pr.Port)), cfg) // #nosec G115 -- port is 1–65535
			if err != nil {
				return
			}
			defer c.Close()
			var ifs []snmpmon.Interface
			for _, i := range md.ifs {
				ifs = append(ifs, snmpmon.Interface{Index: i.Index, Name: i.Name, Descr: i.Descr})
			}
			nb, err := snmpmon.Neighbours(ctx, c, ifs)
			if err != nil {
				s.log.Debug("map: neighbours failed", "device", md.d.Name, "err", err)
				return
			}
			mu.Lock()
			found = append(found, seen{md, nb})
			mu.Unlock()
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, matchNeighbours(devs, func(yield func(*mapDev, snmpmon.Neighbour)) {
		for _, f := range found {
			for _, n := range f.nb {
				yield(f.from, n)
			}
		}
	}))
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
