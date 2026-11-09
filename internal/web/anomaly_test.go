package web_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/alert"
	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/tsdb"
)

// TestAnomalyTrigger: after four weeks of a ping time that is low at night
// and high in working hours, a usual value opens nothing and a value far
// from what is usual for this hour opens a problem that says so.
func TestAnomalyTrigger(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "wan", "address", "10.0.0.7", "ping", "1")
	cs, err := e.st.DeviceChecks(ctx, 1)
	if err != nil || len(cs) == 0 {
		t.Fatal("no ping check", err)
	}
	ping := cs[0]
	var rtt int32
	for _, s := range ping.Series {
		if s.Metric == "rtt_ms" {
			rtt = s.ID
		}
	}
	usual := func(at time.Time) float64 {
		at = at.In(e.now.Location())
		if wd := at.Weekday(); wd >= time.Monday && wd <= time.Friday && at.Hour() >= 9 && at.Hour() < 17 {
			return 40
		}
		return 10
	}
	for h := 28 * 24; h > 0; h-- {
		at := e.now.Add(-time.Duration(h) * time.Hour)
		e.db.Add(tsdb.Sample{Series: rtt, T: at, V: usual(at) + float64(h%3) - 1})
	}
	if err := e.db.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if r := c.post("/alerts/triggers", "name", "WAN latency unusual", "kind", "anomaly", "metric", "rtt_ms", "aop", "<>", "sensitivity", "3.5", "for", "0", "severity", "warning", "enabled", "1"); r.code >= 400 {
		t.Fatalf("trigger: %d %s", r.code, r.body)
	}
	if b := c.get("/alerts/triggers").body; !strings.Contains(b, "higher or lower than usual") {
		t.Fatal("the trigger list does not say what it watches")
	}
	eval := func(v float64) []*store.Problem {
		if _, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: ping.ID, At: e.now, Status: store.StatusUp, Values: map[string]float64{"rtt_ms": v, "loss_pct": 0}}}); err != nil {
			t.Fatal(err)
		}
		eng := alert.New(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
		eng.SetClock(func() time.Time { return e.now })
		eng.HourlyHistory = e.srv.MetricHourly
		if err := eng.Evaluate(ctx); err != nil {
			t.Fatal(err)
		}
		ps, _ := e.st.Problems(ctx, store.ProblemFilter{Open: true, Suppressed: true})
		return ps
	}
	if ps := eval(usual(e.now)); len(ps) != 0 {
		t.Fatalf("a usual value opened %q", ps[0].Detail)
	}
	ps := eval(usual(e.now) + 200)
	if len(ps) != 1 || !strings.Contains(ps[0].Detail, "unusually high") || !strings.Contains(ps[0].Detail, "normal range") {
		for _, p := range ps {
			t.Log(p.Title, p.Detail)
		}
		t.Fatalf("%d problems", len(ps))
	}
	// the sensitivity must be one of the sensible ones
	if r := c.post("/alerts/triggers", "name", "x", "kind", "anomaly", "metric", "rtt_ms", "aop", ">", "sensitivity", "0.5", "severity", "warning"); r.code != 400 {
		t.Fatalf("sensitivity 0.5: %d", r.code)
	}
}
