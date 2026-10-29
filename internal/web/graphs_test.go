package web_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/max-zit/watchzit/internal/store"
)

// TestDeviceGraphs: the graphs of a device arranged by the user (kinds,
// sizes, thresholds, several values), refused when wrong, and back to the
// automatic ones.
func TestDeviceGraphs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "srv", "address", "10.0.0.1", "ping", "1")
	c.post("/devices/new", "name", "other", "address", "10.0.0.2", "ping", "1")
	ping := func(dev int64) *store.Check { cs, _ := e.st.DeviceChecks(ctx, dev); return cs[0] }
	for _, dev := range []int64{1, 2} {
		if _, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: ping(dev).ID, At: e.now, Status: store.StatusUp, Values: map[string]float64{"rtt_ms": 1, "loss_pct": 0}}}); err != nil {
			t.Fatal(err)
		}
	}
	mine, theirs := ping(1).Series, ping(2).Series
	if len(mine) < 2 || len(theirs) == 0 {
		t.Fatalf("series: %+v", mine)
	}
	r := c.get("/devices/1")
	if !strings.Contains(r.body, "data-graph-edit") || !strings.Contains(r.body, `id="graph-edit"`) || !strings.Contains(r.body, `"series":[{"id":`) {
		t.Fatal("no graph editor on the device page")
	}
	max, warn := 50.0, 20.0
	l := store.GraphLayout{Panels: []store.GraphPanel{{ID: "p1", Title: "Latency and loss", Series: []int32{mine[0].ID, mine[1].ID}, Type: "bar", Stack: true, Width: "full", Height: "l", Max: &max, Warn: &warn,
		Cols: 2, H: 240, Colors: map[string]string{strconv.Itoa(int(mine[1].ID)): "#ff8800"}}}}
	if r := c.postJSON("/devices/1/graphs", l); r.code != http.StatusOK {
		t.Fatalf("save: %d %s", r.code, r.body)
	}
	r = c.get("/devices/1")
	for _, want := range []string{`data-panel="p1"`, `data-type="bar"`, `data-stack="1"`, `data-max="50"`, `data-warn="20"`, "w-full", "h-l", "Latency and loss", `data-cols="2"`, `data-h="240"`, `data-colors="|#ff8800"`} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("arranged graph lacks %s", want)
		}
	}
	// refused: another device's series, no series, a bad kind, min above max
	for _, bad := range []store.GraphPanel{{ID: "x", Series: []int32{theirs[0].ID}}, {ID: "x"}, {ID: "x", Series: []int32{mine[0].ID}, Type: "pie"},
		{ID: "x", Series: []int32{mine[0].ID}, Min: &max, Max: &warn}, {ID: "../x", Series: []int32{mine[0].ID}},
		{ID: "x", Series: []int32{mine[0].ID}, H: 20}} {
		if r := c.postJSON("/devices/1/graphs", store.GraphLayout{Panels: []store.GraphPanel{bad}}); r.code != http.StatusBadRequest {
			t.Fatalf("bad graph %+v accepted: %d", bad, r.code)
		}
	}
	// a hidden series is not graphed; back to automatic
	if r := c.postJSON("/devices/1/graphs", store.GraphLayout{Hidden: []int32{mine[0].ID, mine[1].ID}}); r.code != http.StatusOK {
		t.Fatalf("hide: %d", r.code)
	}
	if r := c.get("/devices/1"); strings.Contains(r.body, `class="panel graph`) {
		t.Fatal("hidden series still graphed")
	}
	c.post("/devices/1/graphs?reset=1")
	if r := c.get("/devices/1"); !strings.Contains(r.body, `class="panel graph`) || strings.Contains(r.body, `data-panel="p1"`) {
		t.Fatal("reset did not bring the automatic graphs back")
	}
}
