package web

import (
	"context"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// pathView is a network path check on the device page.
type pathView struct {
	Check   *store.Check
	Now     *store.PathRun
	Day     []store.HopStat
	Changes [][2]store.PathRun
	Devices map[string]*store.DeviceRef // hops that are monitored devices
}

func (s *Server) pathViews(ctx context.Context, cs []*store.Check) ([]pathView, error) {
	var out []pathView
	for _, c := range cs {
		if c.Type != store.CheckTrace {
			continue
		}
		v := pathView{Check: c}
		var err error
		if v.Now, err = s.store.PathNow(ctx, c.ID); err != nil {
			return nil, err
		}
		if v.Day, err = s.store.PathHops(ctx, c.ID, s.now().Add(-24*time.Hour)); err != nil {
			return nil, err
		}
		if v.Changes, err = s.store.PathChanges(ctx, c.ID, 10); err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		var addrs []string
		add := func(a string) {
			if a != "" && !seen[a] {
				seen[a] = true
				addrs = append(addrs, a)
			}
		}
		if v.Now != nil {
			for _, h := range v.Now.Hops {
				add(h.Addr)
			}
		}
		for _, h := range v.Day {
			add(h.Addr)
		}
		if v.Devices, err = s.store.DevicesAt(ctx, addrs); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
