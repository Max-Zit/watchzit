package alert

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// Forecast triggers: the straight line through a value's last week
// (least squares) reaches a threshold within the trigger's time — a disk
// that will be full in ten days, before it is.

// Point is a stored value.
type Point struct {
	T time.Time
	V float64
}

type trend struct {
	at    time.Time
	slope float64 // per second
	ok    bool
}

const (
	trendWindow = 7 * 24 * time.Hour
	trendMinFor = 6 * time.Hour // at least this much history
	trendMinPts = 12
	trendEvery  = 30 * time.Minute // how often a trend is worked out again
)

// slope fits a straight line through the points; ok false without enough
// of them.
func slope(pts []Point) (float64, bool) {
	if len(pts) < trendMinPts || pts[len(pts)-1].T.Sub(pts[0].T) < trendMinFor {
		return 0, false
	}
	t0 := pts[0].T
	var n, sx, sy, sxx, sxy float64
	for _, p := range pts {
		if math.IsNaN(p.V) || math.IsInf(p.V, 0) {
			continue
		}
		x := p.T.Sub(t0).Seconds()
		n++
		sx += x
		sy += p.V
		sxx += x * x
		sxy += x * p.V
	}
	d := n*sxx - sx*sx
	if n < trendMinPts || d == 0 {
		return 0, false
	}
	return (n*sxy - sx*sy) / d, true
}

// trendOf is the slope of a check's metric, worked out at most every
// trendEvery.
func (e *Engine) trendOf(ctx context.Context, checkID int64, metric string, now time.Time) (float64, bool) {
	k := strconv.FormatInt(checkID, 10) + ":" + metric
	if t, ok := e.trends[k]; ok && now.Sub(t.at) < trendEvery {
		return t.slope, t.ok
	}
	if len(e.trends) > 20000 {
		clear(e.trends)
	}
	t := trend{at: now}
	if pts, err := e.History(ctx, checkID, metric, now.Add(-trendWindow), now); err == nil {
		t.slope, t.ok = slope(pts)
	} else {
		e.log.Warn("forecast: history", "check", checkID, "metric", metric, "err", err)
	}
	e.trends[k] = t
	return t.slope, t.ok
}

// matchForecast returns the checks of a device whose value is headed for
// the threshold within the trigger's time.
func (e *Engine) matchForecast(ctx context.Context, t *store.Trigger, d *store.AlertDevice, cs []*store.AlertCheck, now time.Time, open map[string]*store.Problem) []*condition {
	if e.History == nil {
		return nil
	}
	var out []*condition
	for _, c := range cs {
		if c.Paused || c.Status != store.StatusUp || !typeIn(t.CheckTypes, c.Type) {
			continue
		}
		v, ok := c.Values[t.Metric]
		if !ok || math.IsNaN(v) {
			continue
		}
		s, ok := e.trendOf(ctx, c.ID, t.Metric, now)
		if !ok {
			continue
		}
		cond := &condition{trigger: t, device: d, check: c}
		horizon := float64(t.ForS)
		if open[cond.key()] != nil {
			horizon *= 1.25 // stays open a little longer, so it does not flap
		}
		var eta float64
		switch {
		case t.Op == ">" && v >= t.Threshold, t.Op == "<" && v <= t.Threshold:
			eta = 0
		case t.Op == ">" && s > 0:
			eta = (t.Threshold - v) / s
		case t.Op == "<" && s < 0:
			eta = (v - t.Threshold) / -s
		default:
			continue
		}
		if eta > horizon {
			continue
		}
		unit := store.MetricUnit(c.Type, t.Metric, c.Params)
		rate := FormatValue(s*86400, unit)
		if s > 0 {
			rate = "+" + rate
		}
		if eta == 0 {
			cond.detail = fmt.Sprintf("%s: %s now (%s a day), past %s", c.Name, FormatValue(v, unit), rate, FormatValue(t.Threshold, unit))
		} else {
			when := now.Add(time.Duration(eta) * time.Second)
			days := "less than a day"
			if n := int(eta / 86400); n == 1 {
				days = "about a day"
			} else if n > 1 {
				days = "about " + strconv.Itoa(n) + " days"
			}
			cond.detail = fmt.Sprintf("%s: %s now (%s a day), reaches %s in %s (%s)", c.Name, FormatValue(v, unit), rate, FormatValue(t.Threshold, unit), days, when.Format("2006-01-02"))
		}
		out = append(out, cond)
	}
	return out
}
