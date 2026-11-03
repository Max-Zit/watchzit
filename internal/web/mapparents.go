package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/max-zit/watchzit/internal/store"
)

// Dependencies suggested from a map: from the device the network hangs on
// (the root, by default the router or firewall with the most cables),
// every device depends on the one before it on the way to the root. When
// the root's uplink fails, the devices behind it are then "unreachable"
// instead of each opening a problem of its own.

// cables that do not carry the monitoring traffic
var noPathCables = []string{"power", "ha", "vxlan"}

// mapGraph is the device-to-device adjacency of a map's cables; virtual
// machines are joined to the server their hypervisor runs on.
func mapGraph(l *store.MapLayout) map[int64][]int64 {
	dev := map[int64]int64{} // node → device
	host := map[int64]int64{}
	for _, n := range l.Nodes {
		switch {
		case n.Kind == store.NodeDevice && n.DeviceID > 0:
			dev[n.ID] = n.DeviceID
		case n.Kind == store.NodeHost && n.HostDevice > 0:
			dev[n.ID], host[n.ID] = n.HostDevice, n.HostDevice
		}
	}
	adj := map[int64][]int64{}
	join := func(a, b int64) {
		if a == 0 || b == 0 || a == b || slices.Contains(adj[a], b) {
			return
		}
		adj[a] = append(adj[a], b)
		adj[b] = append(adj[b], a)
	}
	for _, k := range l.Links {
		if !slices.Contains(noPathCables, k.Kind) {
			join(dev[k.A], dev[k.B])
		}
	}
	for _, n := range l.Nodes {
		if h := host[n.Parent]; h > 0 && n.Kind == store.NodeDevice {
			join(n.DeviceID, h)
		}
	}
	for _, ns := range adj {
		slices.Sort(ns)
	}
	return adj
}

// mapRoot is the router or firewall with the most cables (else any device
// with the most cables).
func mapRoot(adj map[int64][]int64, kinds map[int64]string) int64 {
	var best int64
	score := func(id int64) int {
		s := len(adj[id])
		if k := kinds[id]; k == "router" || k == "firewall" {
			s += 10000
		}
		return s
	}
	for id := range adj {
		if best == 0 || score(id) > score(best) || score(id) == score(best) && id < best {
			best = id
		}
	}
	return best
}

// suggestParents walks the cables from the root; each device gets the one
// it was reached from. The order is the walk's, root side first.
func suggestParents(adj map[int64][]int64, root int64) (order []int64, parent map[int64]int64) {
	parent = map[int64]int64{}
	if _, ok := adj[root]; !ok {
		return nil, parent
	}
	seen := map[int64]bool{root: true}
	queue := []int64{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nb := range adj[cur] {
			if !seen[nb] {
				seen[nb] = true
				parent[nb] = cur
				order = append(order, nb)
				queue = append(queue, nb)
			}
		}
	}
	return order, parent
}

type parentRow struct {
	Device    *store.Device
	Suggested *store.Device
	Set       bool // ticked: no dependency yet
}

type mapParentsData struct {
	M         *store.Map
	Root      int64
	Roots     []*store.Device
	Rows      []parentRow
	Unlinked  int // devices on the map without a cable path to the root
	Unchanged int
}

func (s *Server) mapParents(r *http.Request, m *store.Map) (*mapParentsData, error) {
	ctx := r.Context()
	l, err := s.store.MapLayout(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	all, err := s.store.Devices(ctx, store.DeviceFilter{})
	if err != nil {
		return nil, err
	}
	byID, kinds := map[int64]*store.Device{}, map[int64]string{}
	for _, d := range all {
		byID[d.ID], kinds[d.ID] = d, d.Kind
	}
	adj := mapGraph(l)
	d := &mapParentsData{M: m}
	for id := range adj {
		if byID[id] != nil {
			d.Roots = append(d.Roots, byID[id])
		}
	}
	slices.SortFunc(d.Roots, func(a, b *store.Device) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	d.Root = mapRoot(adj, kinds)
	if q, err := strconv.ParseInt(r.FormValue("root"), 10, 64); err == nil && adj[q] != nil {
		d.Root = q
	}
	order, parent := suggestParents(adj, d.Root)
	for _, id := range order {
		dev, p := byID[id], byID[parent[id]]
		if dev == nil || p == nil {
			continue
		}
		if dev.Parent == p.ID {
			d.Unchanged++
			continue
		}
		d.Rows = append(d.Rows, parentRow{Device: dev, Suggested: p, Set: dev.Parent == 0})
	}
	d.Unlinked = len(adj) - len(order) - 1
	if d.Unlinked < 0 {
		d.Unlinked = 0
	}
	return d, nil
}

func (s *Server) handleMapParents(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	d, err := s.mapParents(r, m)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "mapparents", &Page{Title: "Dependencies from the map", Nav: "maps", Data: d})
}

// handleMapParentsApply sets the ticked suggestions (root side first, so a
// device is never made to depend on one that still depends on it).
func (s *Server) handleMapParentsApply(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	_ = r.ParseForm()
	d, err := s.mapParents(r, m)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	back := "/maps/" + strconv.FormatInt(m.ID, 10) + "/parents?root=" + strconv.FormatInt(d.Root, 10)
	want := map[string]bool{}
	for _, v := range r.Form["set"] {
		want[v] = true
	}
	n, failed := 0, 0
	for _, row := range d.Rows {
		if !want[strconv.FormatInt(row.Device.ID, 10)] {
			continue
		}
		if _, err := s.store.BulkUpdate(r.Context(), []int64{row.Device.ID}, store.BulkParent, row.Suggested.ID, ""); err != nil {
			failed++
			continue
		}
		n++
	}
	if n > 0 {
		s.audit(r, "device.bulk", "map:"+strconv.FormatInt(m.ID, 10), "dependencies of %d devices set from the map %s", n, m.Name)
	}
	lang := s.lang(r, userFrom(r))
	if failed > 0 {
		s.redirectErr(w, r, back, translate(lang, "%d dependencies set; %d could not be set (they would make a loop).", n, failed))
		return
	}
	s.redirect(w, r, back, translate(lang, "%d devices changed.", n))
}
