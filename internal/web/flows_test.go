package web_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// TestFlows: stored flow minutes show as top lists and a graph; viewers see
// them, only administrators change the collector.
func TestFlows(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "edge", "address", "10.0.0.1")
	c.post("/devices/new", "name", "nas", "address", "10.0.0.20")
	at := e.now.Add(-10 * time.Minute).Truncate(time.Minute)
	a := netip.MustParseAddr
	rows := []store.FlowRow{
		{At: at, Exporter: a("10.0.0.1"), DeviceID: 1, Src: a("10.0.0.20"), Dst: a("203.0.113.5"), Proto: 6, Port: 443, Bytes: 6000000, Packets: 5000},
		{At: at, Exporter: a("10.0.0.1"), DeviceID: 1, Src: a("10.0.0.30"), Dst: a("8.8.8.8"), Proto: 17, Port: 53, Bytes: 1000, Packets: 10},
		{At: at.Add(time.Minute), Exporter: a("10.0.0.1"), DeviceID: 1, Bytes: 500, Packets: 5}, // the rest of a minute
		{At: at.Add(-48 * time.Hour), Exporter: a("10.0.0.1"), DeviceID: 1, Src: a("10.0.0.99"), Dst: a("1.1.1.1"), Bytes: 999999999, Packets: 1},
	}
	if err := e.st.AddFlows(ctx, rows); err != nil {
		t.Fatal(err)
	}
	r := c.get("/flows?range=1h")
	for _, want := range []string{"Top conversations", `href="/devices/2">nas`, "203.0.113.5", "TCP 443 (HTTPS)", "UDP 53 (DNS)", "5.7 MiB", `<option value="1"`} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("flows page lacks %q", want)
		}
	}
	if strings.Contains(r.body, "10.0.0.99") {
		t.Fatal("an old flow is in the hour")
	}
	if r := c.get("/flows?range=7d&device=1"); !strings.Contains(r.body, "10.0.0.99") {
		t.Fatal("the week lacks the old flow")
	}
	var ans struct {
		Series []struct {
			Step int64      `json:"step"`
			Avg  []*float64 `json:"avg"`
			T    []int64    `json:"t"`
			Unit string     `json:"unit"`
		} `json:"series"`
	}
	_ = json.Unmarshal([]byte(c.get("/flows/series/1?range=1h&points=60").body), &ans)
	if len(ans.Series) != 1 || ans.Series[0].Step != 60 || ans.Series[0].Unit != "bit/s" {
		t.Fatalf("series: %+v", ans)
	}
	found := false
	for i, ts := range ans.Series[0].T {
		if ts == at.Unix() && ans.Series[0].Avg[i] != nil && math.Abs(*ans.Series[0].Avg[i]-float64(6001000*8)/60) < 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no rate at %d: %+v", at.Unix(), ans.Series[0])
	}
	if r := c.get("/devices/1"); !strings.Contains(r.body, `href="/flows?device=1"`) {
		t.Fatal("no flows button on the exporter")
	}
	if r := c.get("/devices/2"); strings.Contains(r.body, `href="/flows?device=2"`) {
		t.Fatal("flows button on a device without flows")
	}

	// settings
	if r := c.post("/settings/flows", "enabled", "", "netflow_port", "2055", "sflow_port", "2055", "keep_days", "7"); !strings.Contains(c.get(r.loc).body, "two different ports") {
		t.Fatal("same ports accepted")
	}
	if r := c.post("/settings/flows", "enabled", "", "netflow_port", "9995", "sflow_port", "6343", "keep_days", "14"); r.code != http.StatusSeeOther {
		t.Fatalf("save: %d", r.code)
	}
	if r := c.get("/settings/flows"); !strings.Contains(r.body, `value="9995"`) || !strings.Contains(r.body, `value="14"`) {
		t.Fatal("settings not kept")
	}

	c.post("/users", "username", "watcher", "password", "watcher password 1", "role", "viewer")
	v := e.client("192.168.1.30")
	v.post("/login", "username", "watcher", "password", "watcher password 1")
	e.enroll(v)
	v.post("/account/password", "current", "watcher password 1", "password", "new watcher pass 1", "password2", "new watcher pass 1")
	if r := v.get("/flows"); r.code != http.StatusOK || !strings.Contains(r.body, "203.0.113.5") {
		t.Fatalf("viewer: %d", r.code)
	}
	if r := v.get("/settings/flows"); r.code != http.StatusForbidden {
		t.Fatalf("viewer settings: %d", r.code)
	}
}
