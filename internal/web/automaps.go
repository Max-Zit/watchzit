package web

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// Links (and devices) a map can draw by itself from where the devices are
// plugged in (topology.go): suggested in the editor, or added every 15
// minutes on maps that ask for it. What the user removes from such a map
// stays removed.

type suggestDevice struct {
	ID      int64      `json:"id"`
	Name    string     `json:"name"`
	Address string     `json:"address"`
	Kind    string     `json:"kind"`
	Near    int64      `json:"near"` // the device on the map it is linked to
	Via     string     `json:"via"`
	Link    suggestion `json:"link"` // the link that leads to it
}

type suggestAnswer struct {
	Links   []suggestion    `json:"links"`
	Devices []suggestDevice `json:"devices"`
	Auto    store.MapAuto   `json:"auto"`
	Read    string          `json:"read,omitempty"` // when the devices were read last
}

// handleMapSuggest proposes the links between devices of the map and the
// devices linked to them that are not on it; refresh=1 reads the devices
// of the map again first.
func (s *Server) handleMapSuggest(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	ctx := r.Context()
	l, err := s.store.MapLayout(ctx, m.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	on := map[int64]bool{}
	var ids []int64
	for _, n := range l.Nodes {
		if n.Kind == store.NodeDevice {
			on[n.DeviceID] = true
			ids = append(ids, n.DeviceID)
		}
	}
	if r.FormValue("refresh") == "1" && s.poller != nil && len(ids) > 0 {
		rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		s.poller.Topology(rctx, ids...)
		cancel()
	}
	links, err := s.findLinks(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	a := suggestAnswer{Links: []suggestion{}, Devices: []suggestDevice{}}
	if auto, err := s.store.MapAutoOf(ctx, m.ID); err == nil {
		a.Auto = *auto
	}
	seen := map[int64]bool{}
	for _, k := range links {
		switch {
		case on[k.A] && on[k.B]:
			a.Links = append(a.Links, k)
		case on[k.A] != on[k.B]:
			other, near := k.B, k.A
			if on[k.B] {
				other, near = k.A, k.B
			}
			if seen[other] {
				continue
			}
			seen[other] = true
			if d, err := s.store.DeviceByID(ctx, other); err == nil {
				a.Devices = append(a.Devices, suggestDevice{ID: d.ID, Name: d.Name, Address: d.Address, Kind: d.Kind, Near: near, Via: k.Via, Link: k})
			}
		}
	}
	if at, err := s.store.TopoReadAt(ctx); err == nil {
		var last time.Time
		for _, id := range ids {
			if at[id].After(last) {
				last = at[id]
			}
		}
		if !last.IsZero() {
			a.Read = last.Format(time.RFC3339)
		}
	}
	writeJSON(w, http.StatusOK, a)
}

// handleMapAuto switches the automatic links and devices of a map.
func (s *Server) handleMapAuto(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	var in store.MapAuto
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if err := s.store.SetMapAuto(r.Context(), m.ID, in.Links, in.Devices); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "map.auto", "map:"+m.Name, "links by themselves %v, devices too %v", in.Links, in.Devices && in.Links)
	added := 0
	if in.Links {
		added, _ = s.autoMap(r.Context(), m.ID, nil)
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": added})
}

// RunAutoMaps adds what was found to the maps that ask for it, every 15
// minutes (the devices are read every 15 minutes too).
func (s *Server) RunAutoMaps(ctx context.Context) {
	t := time.NewTicker(15 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.AutoMapsOnce(ctx)
		}
	}
}

// AutoMapsOnce runs the automatic maps once.
func (s *Server) AutoMapsOnce(ctx context.Context) {
	ids, err := s.store.AutoMaps(ctx)
	if err != nil || len(ids) == 0 {
		return
	}
	links, err := s.findLinks(ctx)
	if err != nil {
		s.log.Warn("automatic maps: links not found", "err", err)
		return
	}
	for _, id := range ids {
		if n, err := s.autoMap(ctx, id, links); err != nil {
			s.log.Warn("automatic map failed", "map", id, "err", err)
		} else if n > 0 {
			_ = s.store.Audit(ctx, "-", "server", "map.auto", "map:"+strconv.FormatInt(id, 10), "added "+strconv.Itoa(n)+" links or devices the devices report")
		}
	}
}

// autoMap adds the missing links (and, when the map wants them, devices)
// to one map; links is nil to find them first.
func (s *Server) autoMap(ctx context.Context, mapID int64, links []suggestion) (int, error) {
	auto, err := s.store.MapAutoOf(ctx, mapID)
	if err != nil || !auto.Links {
		return 0, err
	}
	if links == nil {
		if links, err = s.findLinks(ctx); err != nil {
			return 0, err
		}
	}
	l, err := s.store.MapLayout(ctx, mapID)
	if err != nil {
		return 0, err
	}
	nodeOf := func(dev int64) *store.MapNode {
		for i := range l.Nodes {
			if n := &l.Nodes[i]; n.Kind == store.NodeDevice && n.DeviceID == dev {
				return n
			}
		}
		return nil
	}
	ignored := func(k string) bool { return slices.Contains(auto.Ignore, k) }
	joined := func(a, b *store.MapNode) bool {
		for _, k := range l.Links {
			if (k.A == a.ID && k.B == b.ID) || (k.A == b.ID && k.B == a.ID) {
				return true // a cable between them is drawn already (maybe by hand)
			}
		}
		return false
	}
	nextID := int64(-1)
	added := 0
	for _, k := range links {
		na, nb := nodeOf(k.A), nodeOf(k.B)
		if auto.Devices && (na == nil) != (nb == nil) {
			// one end on the map: bring the other device next to it
			near, other := na, k.B
			if na == nil {
				near, other = nb, k.A
			}
			if ignored(store.TopoDeviceKey(other)) {
				continue
			}
			x, y := freeSpotNear(l, near)
			l.Nodes = append(l.Nodes, store.MapNode{ID: nextID, Kind: store.NodeDevice, DeviceID: other, X: x, Y: y, Auto: true})
			nextID--
			added++
			na, nb = nodeOf(k.A), nodeOf(k.B)
		}
		if na == nil || nb == nil || joined(na, nb) || ignored(store.TopoLinkKey(k.A, k.AIf, k.B, k.BIf)) {
			continue
		}
		l.Links = append(l.Links, store.MapLink{ID: nextID, A: na.ID, B: nb.ID, AIf: k.AIf, BIf: k.BIf, Kind: "ethernet", Route: "straight", Auto: true})
		nextID--
		added++
	}
	if added == 0 {
		return 0, nil
	}
	l.SeenLink, l.SeenNode = 0, 0 // the map keeps all it had; nothing was removed by hand
	if _, err := s.store.SaveMapLayout(ctx, mapID, l); err != nil {
		return 0, err
	}
	return added, nil
}

// freeSpotNear finds a place next to a node that no other node takes.
func freeSpotNear(l *store.MapLayout, near *store.MapNode) (float64, float64) {
	cx, cy := near.X, near.Y
	if near.Parent != 0 { // in a rack or on a hypervisor: beside that
		for _, p := range l.Nodes {
			if p.ID == near.Parent {
				cx = p.X + 300
			}
		}
	}
	for r := 170.0; r < 3000; r += 120 {
		for i := 0; i < 12; i++ {
			a := float64(i) * math.Pi / 6
			x, y := math.Round(cx+r*math.Cos(a)), math.Round(cy+r*math.Sin(a))
			free := true
			for _, n := range l.Nodes {
				if math.Abs(n.X-x) < 110 && math.Abs(n.Y-y) < 110 {
					free = false
					break
				}
			}
			if free {
				return x, y
			}
		}
	}
	return cx + 200, cy + 200
}
