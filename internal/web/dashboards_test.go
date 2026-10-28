package web_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

func TestDashboardsAndReports(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/groups", "name", "Core")
	c.post("/devices/new", "name", "=core", "address", "10.0.0.1", "group", "1", "ping", "1")
	c.post("/devices/new", "name", "edge", "address", "10.0.0.2", "ping", "1")
	c.post("/maps", "name", "Net")

	// a dashboard with every kind of widget
	r := c.post("/dashboards", "name", "NOC")
	if r.code != http.StatusSeeOther || r.loc != "/dashboards/1?edit=1" {
		t.Fatalf("create: %d %s", r.code, r.loc)
	}
	cs, _ := e.st.DeviceChecks(ctx, 1)
	ping := itoa(int32(cs[0].ID))
	for _, w := range [][]string{
		{"kind", "graph", "check", ping, "size", "l"},
		{"kind", "value", "series", ping + "|rtt_ms", "title", "Core ping"},
		{"kind", "status", "group", "1"},
		{"kind", "problems"},
		{"kind", "map", "map", "1", "size", "s"},
	} {
		if r := c.post("/dashboards/1/widgets", w...); r.code != http.StatusSeeOther {
			t.Fatalf("widget %v: %d", w, r.code)
		}
	}
	if r := c.post("/dashboards/1/widgets", "kind", "map", "map", "0"); !strings.Contains(c.get(r.loc).body, "choose a map") {
		t.Fatal("a map widget without a map was accepted")
	}
	c.post("/dashboards/1/widgets/1", "do", "up")
	d, _ := e.st.DashboardByID(ctx, 1)
	if len(d.Widgets) != 5 || d.Widgets[0].Kind != "value" || d.Widgets[1].Size != "l" {
		t.Fatalf("widgets: %+v", d.Widgets)
	}
	// results arrive: the value widget shows the latest ping time
	if _, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: cs[0].ID, At: e.now, Status: "up", Text: "1 ms", Values: map[string]float64{"rtt_ms": 1.25, "loss_pct": 0}}}); err != nil {
		t.Fatal(err)
	}
	r = c.get("/dashboards/1")
	for _, want := range []string{`http-equiv="refresh"`, "Core ping", "1.25 ms", `data-series="`, "=core", `href="/maps/1"`, "No problems."} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("dashboard lacks %q", want)
		}
	}
	if strings.Contains(r.body, ">edge<") {
		t.Fatal("the status widget shows a device of another group")
	}
	// the checked device goes away: its widgets say so
	c.post("/dashboards/1/widgets/4", "do", "delete")
	c.post("/devices/1/delete")
	if r := c.get("/dashboards/1"); !strings.Contains(r.body, "was deleted") {
		t.Fatal("missing widget")
	}

	// availability: edge was down for an hour, of it nothing in maintenance
	start := e.now.Add(-3 * time.Hour)
	if _, err := e.st.OpenProblem(ctx, store.NewProblem{Key: "t1:d2", TriggerID: 1, DeviceID: 2, Severity: "critical", Title: "Device down", At: start}); err != nil {
		t.Fatal(err)
	}
	ps, _ := e.st.OpenProblems(ctx)
	_ = e.st.ResolveProblem(ctx, ps[0].ID, start.Add(time.Hour))
	if _, err := e.st.OpenProblem(ctx, store.NewProblem{Key: "t1:d2b", TriggerID: 1, DeviceID: 2, Severity: "critical", Title: "Device down", At: e.now.Add(-30 * time.Minute), Suppressed: "maintenance"}); err != nil {
		t.Fatal(err)
	}
	// devices count from when they were added: move edge's start back
	if _, err := e.st.Pool().Exec(ctx, `UPDATE devices SET created_at=$1 WHERE id=2`, e.now.AddDate(0, 0, -40)); err != nil {
		t.Fatal(err)
	}
	r = c.get("/reports?period=30d")
	if r.code != http.StatusOK || !strings.Contains(r.body, "99.861%") || !strings.Contains(r.body, "30 min") {
		t.Fatalf("report: %d", r.code)
	}
	r = c.get("/reports.csv?period=30d")
	if !strings.Contains(r.body, "edge,,10.0.0.2,99.861,3600,1800,2,") || r.hdr.Get("Content-Type") != "text/csv; charset=utf-8" {
		t.Fatalf("csv: %s", r.body)
	}
	// a formula-like name is neutralised in the CSV
	c.post("/devices/new", "name", "=cmd", "address", "10.0.0.9")
	if r := c.get("/reports.csv?period=7d"); !strings.Contains(r.body, "'=cmd") {
		t.Fatalf("csv injection: %s", r.body)
	}

	// a viewer sees dashboards but cannot change them
	c.post("/users", "username", "watcher", "password", "watcher password 1", "role", "viewer")
	v := e.client("192.168.1.30")
	v.post("/login", "username", "watcher", "password", "watcher password 1")
	e.enroll(v)
	v.post("/account/password", "current", "watcher password 1", "password", "new watcher pass 1", "password2", "new watcher pass 1")
	if r := v.get("/dashboards/1?edit=1"); r.code != http.StatusOK || strings.Contains(r.body, "Add a widget") {
		t.Fatal("viewer got the editor")
	}
	if r := v.post("/dashboards/1/widgets", "kind", "problems"); r.code != http.StatusForbidden {
		t.Fatalf("viewer added a widget: %d", r.code)
	}
}
