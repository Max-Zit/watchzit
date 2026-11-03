package poller

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/checks"
	"github.com/max-zit/watchzit/internal/probe"
	"github.com/max-zit/watchzit/internal/snmpmon"
	"github.com/max-zit/watchzit/internal/store"
)

// Topology reads the neighbour, MAC address and ARP tables of the SNMP
// devices (only those of ids when given), for the links maps suggest or
// draw by themselves.
func (p *Poller) Topology(ctx context.Context, ids ...int64) {
	devs, err := p.store.DevicesWithSNMP(ctx)
	if err != nil {
		p.log.Error("poller: cannot list SNMP devices", "err", err)
		return
	}
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, d := range devs {
		if (len(ids) > 0 && !want[d.ID]) || d.Paused || d.CollectorID != 0 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			if err := p.readTopology(ctx, d); err != nil {
				p.log.Debug("poller: topology not read", "device", d.Name, "err", err)
			}
		}()
	}
	wg.Wait()
}

func (p *Poller) readTopology(ctx context.Context, d *store.Device) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	addr, err := probe.Resolve(ctx, d.Address)
	if err != nil {
		return err
	}
	cl, err := p.snmpClient(d.SNMPProfile, addr, 3*time.Second)
	if err != nil {
		return err
	}
	defer cl.Close()
	ifs, _ := p.store.Interfaces(ctx, d.ID)
	var sifs []snmpmon.Interface
	for _, i := range ifs {
		sifs = append(sifs, snmpmon.Interface{Index: i.Index, Name: i.Name, Descr: i.Descr})
	}
	nbs, err := snmpmon.Neighbours(ctx, cl, sifs)
	if err != nil {
		return err // no answer: keep what was read before
	}
	nb := make([]store.TopoNeighbour, 0, len(nbs))
	for _, n := range nbs {
		nb = append(nb, store.TopoNeighbour{IfIndex: n.LocalIf, LocalPort: n.LocalPort, Name: n.Name, RemotePort: n.Port, RemoteDesc: n.PortDesc, Address: n.Address, MAC: n.MAC, Via: n.Via})
	}
	fdb := []store.TopoFDB{}
	if fs, err := snmpmon.FDB(ctx, cl); err == nil {
		for _, f := range fs {
			fdb = append(fdb, store.TopoFDB{IfIndex: f.IfIndex, MAC: f.MAC, VLAN: f.VLAN})
		}
	}
	arp := []store.TopoARP{}
	if as, err := snmpmon.ARP(ctx, cl); err == nil {
		for _, a := range as {
			arp = append(arp, store.TopoARP{Address: a.Addr, MAC: a.MAC, IfIndex: a.IfIndex})
		}
	}
	// a RouterOS device whose SNMP has no MAC table keeps the one its API read
	if len(fdb) == 0 && p.hasRouterAPI(ctx, d.ID) {
		fdb = nil
	}
	if len(arp) == 0 && p.hasRouterAPI(ctx, d.ID) {
		arp = nil
	}
	return p.store.SetTopology(ctx, d.ID, p.now(), nb, fdb, arp)
}

// hasRouterAPI tells whether the device has a RouterOS API check.
func (p *Poller) hasRouterAPI(ctx context.Context, id int64) bool {
	cs, err := p.store.DeviceChecks(ctx, id)
	if err != nil {
		return false
	}
	for _, c := range cs {
		if c.Type == store.CheckRouterOS && !c.Paused {
			return true
		}
	}
	return false
}

// routerTopology keeps the MAC address and ARP tables a RouterOS device
// gives over its API (its SNMP has no MAC table), and its neighbours when
// it is not read over SNMP. idx maps interface names to ifIndex.
func (p *Poller) routerTopology(ctx context.Context, c *store.Check, t checks.Target, idx map[string]int) {
	rt, err := checks.RouterOSTopology(ctx, c.Params, t)
	if err != nil {
		p.log.Debug("poller: RouterOS topology not read", "device", c.DeviceID, "err", err)
		return
	}
	fdb := make([]store.TopoFDB, 0, len(rt.Hosts))
	for _, h := range rt.Hosts {
		if i := idx[h.Port]; i > 0 {
			fdb = append(fdb, store.TopoFDB{IfIndex: i, MAC: h.MAC, VLAN: h.VLAN})
		}
	}
	arp := make([]store.TopoARP, 0, len(rt.ARP))
	for _, a := range rt.ARP {
		if ip, err := netip.ParseAddr(a.Address); err == nil {
			arp = append(arp, store.TopoARP{Address: ip, MAC: a.MAC, IfIndex: idx[a.Interface]})
		}
	}
	var nb []store.TopoNeighbour
	if d, err := p.store.DeviceByID(ctx, c.DeviceID); err == nil && d.SNMPProfile == 0 {
		nb = make([]store.TopoNeighbour, 0, len(rt.Neighbours))
		for _, n := range rt.Neighbours {
			ip, _ := netip.ParseAddr(n.Address)
			nb = append(nb, store.TopoNeighbour{IfIndex: idx[n.Interface], LocalPort: n.Interface, Name: n.Identity, RemotePort: n.Port, Address: ip, MAC: n.MAC, Via: "MikroTik"})
		}
	}
	if err := p.store.SetTopology(ctx, c.DeviceID, p.now(), nb, fdb, arp); err != nil {
		p.log.Warn("poller: cannot save the topology", "device", c.DeviceID, "err", err)
	}
}
