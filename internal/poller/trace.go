package poller

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/max-zit/watchzit/internal/probe"
	"github.com/max-zit/watchzit/internal/store"
)

// runTrace traces the path to a device: the hops go with the result (the
// console keeps them as path history), and path_changed is 1 when a hop
// that answered both times now has another address, or the destination
// is a different number of hops away.
func (p *Poller) runTrace(ctx context.Context, c *store.Check, addr netip.Addr, timeout time.Duration, o *outcome, sample func(string, float64)) {
	hops, err := probe.Trace(ctx, addr, c.Params.MaxHops, c.Params.Count, timeout)
	if err != nil {
		o.res.Text = err.Error()
		return
	}
	path := make([]store.PathHop, len(hops))
	addrs := make([]string, len(hops))
	for i, h := range hops {
		ph := store.PathHop{TTL: h.TTL, Other: h.Other, Sent: h.Sent, Recv: h.Recv, Final: h.Final}
		if h.Addr.IsValid() {
			ph.Addr = h.Addr.String()
		}
		if b, a, w, ok := h.Stats(); ok {
			ph.Best, ph.Avg, ph.Worst = round2(b), round2(a), round2(w)
		}
		path[i], addrs[i] = ph, ph.Addr
	}
	o.res.Path = path
	p.mu.Lock()
	prev, had := p.lastPath[c.ID]
	p.lastPath[c.ID] = addrs
	p.mu.Unlock()
	changed := 0.0
	if had && pathChanged(prev, addrs) {
		changed = 1
	}
	sample("path_changed", changed)
	sample("hops", float64(len(hops)))
	last := path[len(path)-1]
	if !last.Final || last.Recv == 0 {
		sample("loss_pct", 100)
		answered := ""
		for i := len(path) - 1; i >= 0; i-- {
			if path[i].Addr != "" {
				answered = fmt.Sprintf(" after hop %d (%s)", path[i].TTL, path[i].Addr)
				break
			}
		}
		o.res.Text = "the device did not answer" + answered
		return
	}
	sample("loss_pct", round2(last.Loss()))
	sample("rtt_ms", last.Avg)
	o.res.Status = store.StatusUp
	o.res.Text = fmt.Sprintf("%d hops, %.2f ms, %.0f%% loss", len(path), last.Avg, last.Loss())
	if from := lossFrom(path); from != nil {
		o.res.Text += fmt.Sprintf(", from hop %d (%s)", from.TTL, from.Addr)
	}
	if changed == 1 {
		o.res.Text += ", path changed"
	}
}

func pathChanged(prev, now []string) bool {
	if len(prev) != len(now) {
		return true
	}
	for i := range now {
		if prev[i] != "" && now[i] != "" && prev[i] != now[i] {
			return true
		}
	}
	return false
}

// lossFrom is the hop where the loss to the destination starts: from it on
// every hop loses about as much. A router that drops only the answers about
// itself (rate limits) has hops after it without loss, so it is not named.
func lossFrom(path []store.PathHop) *store.PathHop {
	last := path[len(path)-1]
	if last.Loss() == 0 || len(path) < 2 {
		return nil
	}
	var from *store.PathHop
	for i := len(path) - 2; i >= 0; i-- {
		h := &path[i]
		if h.Addr == "" {
			continue
		}
		if h.Loss() < last.Loss()*0.8 {
			break
		}
		from = h
	}
	return from
}
