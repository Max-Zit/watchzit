package web_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/alert"
	"github.com/max-zit/watchzit/internal/store"
)

// TestNoDataAndRepeatingMaintenance: a check without a new value opens a
// "no new data" problem; a weekly maintenance window suppresses it in each
// of its windows and not between them.
func TestNoDataAndRepeatingMaintenance(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "nas", "address", "10.0.0.5", "ping", "1")
	cs, _ := e.st.DeviceChecks(ctx, 1)
	if _, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: cs[0].ID, At: e.now, Status: store.StatusUp, Values: map[string]float64{"rtt_ms": 1}}}); err != nil {
		t.Fatal(err)
	}
	if r := c.post("/alerts/triggers", "name", "Silent check", "kind", "nodata", "for", "10", "severity", "warning", "enabled", "1"); r.code >= 400 {
		t.Fatalf("trigger: %d", r.code)
	}
	if r := c.post("/alerts/triggers", "name", "x", "kind", "nodata", "for", "1", "severity", "warning"); r.code != 400 {
		t.Fatalf("1 minute accepted: %d", r.code)
	}
	eng := alert.New(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	eng.SetClock(func() time.Time { return e.now })
	open := func() []*store.Problem {
		t.Helper()
		if err := eng.Evaluate(ctx); err != nil {
			t.Fatal(err)
		}
		ps, _ := e.st.Problems(ctx, store.ProblemFilter{Open: true, Suppressed: true})
		var out []*store.Problem
		for _, p := range ps {
			if p.Title == "Silent check" {
				out = append(out, p)
			}
		}
		return out
	}
	if len(open()) != 0 {
		t.Fatal("no data too early")
	}
	// a weekly window, now and in a week, 2 hours long
	from := e.now.Local().Add(5 * time.Minute)
	if r := c.post("/alerts/maintenance", "name", "Backup night", "from", from.Format("2006-01-02T15:04"), "to", from.Add(2*time.Hour).Format("2006-01-02T15:04"),
		"repeat", "weekly", "device", "1"); r.code != 303 {
		t.Fatalf("maintenance: %d %s", r.code, r.body)
	}
	if b := c.get("/alerts/maintenance").body; !strings.Contains(b, "Every week") || !strings.Contains(b, "Planned") {
		t.Fatalf("the list does not show the repetition: %s", b[max(0, strings.Index(b, "<main")):min(len(b), max(0, strings.Index(b, "<main"))+2500)])
	}
	// too long for its repetition
	if r := c.post("/alerts/maintenance", "name", "x", "from", from.Format("2006-01-02T15:04"), "to", from.Add(25*time.Hour).Format("2006-01-02T15:04"),
		"repeat", "daily", "device", "1"); r.code != 400 {
		t.Fatalf("25 h daily accepted: %d", r.code)
	}
	e.now = e.now.Add(11 * time.Minute)
	ps := open()
	if len(ps) != 1 || ps[0].Suppressed != "maintenance" || !strings.Contains(ps[0].Detail, "no new value") {
		t.Fatalf("in the window: %+v", ps)
	}
	e.now = e.now.Add(3 * time.Hour) // between windows
	if ps := open(); len(ps) != 1 || ps[0].Suppressed != "" {
		t.Fatalf("between windows: %+v", ps)
	}
	e.now = from.AddDate(0, 0, 7).Add(time.Minute)
	if ps := open(); len(ps) != 1 || ps[0].Suppressed != "maintenance" {
		t.Fatalf("next week: %+v", ps)
	}
}

// TestMapUnreachable: a device that is down while the device it depends on
// is down too comes to the map as unreachable (grey); the parent does not.
func TestMapUnreachable(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "router", "address", "10.0.0.1", "ping", "1")
	c.post("/devices/new", "name", "switch", "address", "10.0.0.2", "ping", "1", "parent", "1")
	c.post("/devices/new", "name", "server", "address", "10.0.0.3", "ping", "1", "parent", "2")
	var res []store.CheckResult
	for _, d := range []int64{1, 2, 3} {
		cs, _ := e.st.DeviceChecks(ctx, d)
		res = append(res, store.CheckResult{CheckID: cs[0].ID, At: e.now, Status: store.StatusDown, Text: "timeout"})
	}
	if _, err := e.st.SaveResults(ctx, res); err != nil {
		t.Fatal(err)
	}
	c.post("/maps", "name", "Net")
	c.postJSON("/maps/1/layout", store.MapLayout{Nodes: []store.MapNode{{ID: -1, Kind: "device", DeviceID: 1}, {ID: -2, Kind: "device", DeviceID: 2, X: 200}, {ID: -3, Kind: "device", DeviceID: 3, X: 400}}})
	b := c.get("/maps/1/live").body
	for _, want := range []string{`"id":1,`, `"id":2,`, `"id":3,`} {
		if !strings.Contains(b, want) {
			t.Fatalf("live data lacks %s: %s", want, b)
		}
	}
	var live struct {
		Devices map[string]struct{ Unreachable bool }
	}
	if err := json.Unmarshal([]byte(b), &live); err != nil {
		t.Fatal(err)
	}
	if live.Devices["1"].Unreachable || !live.Devices["2"].Unreachable || !live.Devices["3"].Unreachable {
		t.Fatalf("%+v", live.Devices)
	}
}
