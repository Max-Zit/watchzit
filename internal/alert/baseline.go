package alert

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// Anomaly triggers: WatchZit learns what is usual for a value at each hour
// of the week (Monday 9:00 is not Sunday 3:00) from the last four weeks,
// and opens a problem when the value is far from that — no fixed threshold
// to guess. "Far" is a number of typical deviations (the trigger's
// threshold): robust statistics (median and median absolute deviation), so
// a few spikes in the history do not widen what counts as usual.

const (
	baselineWindow = 28 * 24 * time.Hour
	baselineMinFor = 7 * 24 * time.Hour // learning: no problems before a week of history
	baselineMinPts = 6                  // values for an hour of the week (with its neighbours)
	baselineEvery  = time.Hour          // how often a baseline is worked out again
	hoursOfWeek    = 7 * 24
)

// baseline is the usual value (median) and its spread for every hour of
// the week; NaN where there was too little history.
type baseline struct {
	at     time.Time
	ok     bool
	median [hoursOfWeek]float64
	spread [hoursOfWeek]float64
}

func hourOfWeek(t time.Time) int { return int(t.Weekday())*24 + t.Hour() }

func medianOf(v []float64) float64 {
	s := slices.Clone(v)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// learnBaseline works out the usual value per hour of the week from hourly
// points (in the server's time zone). Each hour also uses its neighbours,
// so four weeks give about twelve values per hour.
func learnBaseline(pts []Point, now time.Time) baseline {
	b := baseline{at: now}
	var all []float64
	buckets := make([][]float64, hoursOfWeek)
	var first, last time.Time
	for _, p := range pts {
		if math.IsNaN(p.V) || math.IsInf(p.V, 0) {
			continue
		}
		if first.IsZero() || p.T.Before(first) {
			first = p.T
		}
		if p.T.After(last) {
			last = p.T
		}
		h := hourOfWeek(p.T.In(now.Location()))
		buckets[h] = append(buckets[h], p.V)
		all = append(all, p.V)
	}
	for i := range b.median {
		b.median[i], b.spread[i] = math.NaN(), math.NaN()
	}
	if len(all) < 24 || last.Sub(first) < baselineMinFor {
		return b
	}
	// a floor for the spread: a value that is always the same would make
	// any tiny change "unusual"
	sorted := slices.Clone(all)
	slices.Sort(sorted)
	p5, p95 := sorted[len(sorted)*5/100], sorted[len(sorted)*95/100]
	floor := 0.05 * (p95 - p5)
	for h := range hoursOfWeek {
		var v []float64
		for _, d := range []int{-1, 0, 1} {
			v = append(v, buckets[(h+d+hoursOfWeek)%hoursOfWeek]...)
		}
		if len(v) < baselineMinPts {
			continue
		}
		m := medianOf(v)
		dev := make([]float64, len(v))
		for i, x := range v {
			dev[i] = math.Abs(x - m)
		}
		s := 1.4826 * medianOf(dev) // MAD scaled to a standard deviation
		s = max(s, floor, 0.05*math.Abs(m))
		if s == 0 {
			s = 1e-9
		}
		b.median[h], b.spread[h] = m, s
		b.ok = true
	}
	return b
}

// deviation is how many spreads v is from the usual value at t; ok false
// while that hour has no baseline.
func (b *baseline) deviation(v float64, t time.Time) (dev, usual, spread float64, ok bool) {
	h := hourOfWeek(t)
	m, s := b.median[h], b.spread[h]
	if !b.ok || math.IsNaN(m) || math.IsNaN(s) {
		return 0, 0, 0, false
	}
	return (v - m) / s, m, s, true
}

// baselineOf is the baseline of a check's metric, worked out at most every
// baselineEvery.
func (e *Engine) baselineOf(ctx context.Context, checkID int64, metric string, now time.Time) *baseline {
	k := strconv.FormatInt(checkID, 10) + ":" + metric
	if b, ok := e.baselines[k]; ok && now.Sub(b.at) < baselineEvery {
		return b
	}
	if len(e.baselines) > 20000 {
		clear(e.baselines)
	}
	b := &baseline{at: now}
	if pts, err := e.HourlyHistory(ctx, checkID, metric, now.Add(-baselineWindow), now); err == nil {
		l := learnBaseline(pts, now)
		b = &l
	} else {
		e.log.Warn("anomaly: history", "check", checkID, "metric", metric, "err", err)
	}
	e.baselines[k] = b
	return b
}

// matchAnomaly returns the checks of a device whose value is unusual for
// this hour of the week, for at least the trigger's time.
func (e *Engine) matchAnomaly(ctx context.Context, t *store.Trigger, d *store.AlertDevice, cs []*store.AlertCheck, now time.Time,
	open map[string]*store.Problem, seen map[string]bool) []*condition {
	if e.HourlyHistory == nil {
		return nil
	}
	var out []*condition
	for _, c := range cs {
		if c.Paused || c.Status != store.StatusUp || !typeIn(t.CheckTypes, c.Type) || !checkIn(t, c) {
			continue
		}
		v, ok := c.Values[t.Metric]
		if !ok || math.IsNaN(v) {
			continue
		}
		b := e.baselineOf(ctx, c.ID, t.Metric, now)
		dev, usual, spread, ok := b.deviation(v, now)
		if !ok {
			continue // still learning
		}
		cond := &condition{trigger: t, device: d, check: c}
		k := cond.key()
		limit := t.Threshold
		if open[k] != nil {
			limit *= 0.8 // closes a little inside the limit, so it does not flap
		}
		high, low := dev > limit, dev < -limit
		if !(t.Op == ">" && high || t.Op == "<" && low || t.Op == "<>" && (high || low)) {
			continue
		}
		seen[k] = true
		first, pending := e.pending[k]
		if !pending {
			first = now
			e.pending[k] = now
		}
		if open[k] == nil && now.Sub(first) < time.Duration(t.ForS)*time.Second {
			continue
		}
		unit := store.MetricUnit(c.Type, t.Metric, c.Params)
		word := "unusually high"
		if low {
			word = "unusually low"
		}
		lo, hi := usual-limit*spread, usual+limit*spread
		cond.detail = fmt.Sprintf("%s: %s now, %s (usual on %s around %02d:00: %s, normal range %s–%s)", c.Name, FormatValue(v, unit), word,
			now.Weekday(), now.Hour(), FormatValue(usual, unit), FormatValue(lo, unit), FormatValue(hi, unit))
		out = append(out, cond)
	}
	return out
}
