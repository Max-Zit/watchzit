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

// TestForecastTrigger: a value that grows steadily opens a forecast
// problem when its trend reaches the threshold within the trigger's days,
// and not when it would take longer.
func TestForecastTrigger(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "nas", "address", "10.0.0.5", "ping", "1")
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
	// three days rising from 10 to 30 ms: about 6.7 ms a day
	for h := 72; h >= 0; h-- {
		e.db.Add(tsdb.Sample{Series: rtt, T: e.now.Add(-time.Duration(h) * time.Hour), V: 30 - float64(h)*20/72})
	}
	if err := e.db.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: ping.ID, At: e.now, Status: store.StatusUp, Values: map[string]float64{"rtt_ms": 30, "loss_pct": 0}}}); err != nil {
		t.Fatal(err)
	}
	for _, f := range [][2]string{{"Slow in a week", "7"}, {"Slow tomorrow", "1"}} {
		if r := c.post("/alerts/triggers", "name", f[0], "kind", "forecast", "metric", "rtt_ms", "op", ">", "threshold", "50", "days", f[1], "severity", "warning", "enabled", "1"); r.code >= 400 {
			t.Fatalf("trigger: %d %s", r.code, r.body)
		}
	}
	if b := c.get("/alerts/triggers").body; !strings.Contains(b, "within 7 days") {
		t.Fatal("the trigger list does not say how far it looks")
	}
	eng := alert.New(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	eng.SetClock(func() time.Time { return e.now })
	eng.History = e.srv.MetricHistory
	if err := eng.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	ps, _ := e.st.Problems(ctx, store.ProblemFilter{Open: true, Suppressed: true})
	if len(ps) != 1 || ps[0].Title != "Slow in a week" || !strings.Contains(ps[0].Detail, "reaches 50 ms in about 3 days") {
		for _, p := range ps {
			t.Log(p.Title, p.Detail)
		}
		t.Fatalf("%d problems", len(ps))
	}
	// a bad number of days is refused
	if r := c.post("/alerts/triggers", "name", "x", "kind", "forecast", "metric", "rtt_ms", "op", ">", "threshold", "50", "days", "0", "severity", "warning"); r.code != 400 {
		t.Fatalf("0 days: %d", r.code)
	}
}
