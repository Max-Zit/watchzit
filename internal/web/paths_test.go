package web_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// TestNetworkPath: a path check is added with its defaults, its runs are
// kept, and the device page shows the path, the hops of the last day (with
// monitored routers named) and the changes.
func TestNetworkPath(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "branch-fw", "address", "198.51.100.7")
	c.post("/devices/new", "name", "core", "address", "10.0.0.1")
	if r := c.post("/devices/1/checks", "type", "trace", "interval", "30"); r.code != http.StatusBadRequest || !strings.Contains(r.body, "once a minute") {
		t.Fatalf("30 s accepted: %d", r.code)
	}
	if r := c.post("/devices/1/checks", "type", "trace"); r.code != http.StatusSeeOther {
		t.Fatalf("add: %d %s", r.code, r.body)
	}
	cs, _ := e.st.DeviceChecks(ctx, 1)
	var tr *store.Check
	for _, x := range cs {
		if x.Type == "trace" {
			tr = x
		}
	}
	if tr == nil || tr.IntervalS != 300 || tr.Params.Count != 3 || tr.Params.MaxHops != 30 || tr.Name != "Network path" {
		t.Fatalf("check: %+v", tr)
	}
	run := func(at time.Time, changed float64, second string) {
		t.Helper()
		_, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: tr.ID, At: at, Status: store.StatusUp, Text: "3 hops",
			Values: map[string]float64{"rtt_ms": 20, "loss_pct": 0, "hops": 3, "path_changed": changed},
			Path: []store.PathHop{{TTL: 1, Addr: "10.0.0.1", Sent: 3, Recv: 3, Best: 1, Avg: 1.5, Worst: 2},
				{TTL: 2, Addr: second, Sent: 3, Recv: 2, Best: 9, Avg: 10, Worst: 11}, {TTL: 3, Addr: "198.51.100.7", Sent: 3, Recv: 3, Avg: 20, Final: true}}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	run(now.Add(-40*24*time.Hour), 0, "203.0.113.1") // older than 30 days: removed by the next run
	run(now.Add(-2*time.Hour), 0, "203.0.113.1")
	run(now.Add(-time.Hour), 1, "203.0.113.99")
	if ch, err := e.st.PathChanges(ctx, tr.ID, 10); err != nil || len(ch) != 1 || ch[0][1].Hops[1].Addr != "203.0.113.1" {
		t.Fatalf("changes: %v %+v", err, ch)
	}
	day, _ := e.st.PathHops(ctx, tr.ID, now.Add(-24*time.Hour))
	if len(day) != 4 {
		t.Fatalf("day: %+v", day)
	}
	r := c.get("/devices/1")
	for _, want := range []string{`id="path-`, "203.0.113.99", `<a href="/devices/2">core</a>`, "33 %", "Last 24 hours per hop", "<b>203.0.113.99</b>"} {
		if !strings.Contains(r.body, want) {
			t.Errorf("device page lacks %q", want)
		}
	}
	if all, _ := e.st.PathHops(ctx, tr.ID, time.Time{}); len(all) == 0 || all[0].Runs != 2 {
		t.Errorf("runs kept: %+v", all)
	}
	// a collector's path is cleaned: bad addresses dropped, TTLs renumbered
	p := store.CleanPath([]store.PathHop{{TTL: 9, Addr: "not-an-ip", Sent: 99}, {TTL: 1, Addr: "::ffff:10.0.0.1"}})
	if p[0].TTL != 1 || p[0].Addr != "" || p[0].Sent != 10 || p[1].TTL != 2 {
		t.Errorf("clean: %+v", p)
	}
}
