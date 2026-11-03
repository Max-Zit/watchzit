package web_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/agent"
	"github.com/max-zit/watchzit/internal/proto"
	"github.com/max-zit/watchzit/internal/store"
)

// TestRemoteCollector: an enrolled agent becomes a remote collector, a
// device is assigned to it, and its TCP check runs on the agent and comes
// back to the console; when the collector goes away the check is unknown
// (not down), and turning the role off gives the device back to the console.
func TestRemoteCollector(t *testing.T) {
	agent.ProtectDir = func(string) error { return nil }
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")

	// a port at the "remote site"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	c.post("/devices/new", "name", "site-nas", "address", "127.0.0.1", "kind", "nas")
	tcp, err := e.st.CreateCheck(ctx, 1, store.CheckInput{Type: store.CheckTCP, Name: "SMB", Params: store.CheckParams{Port: port}, IntervalS: 10})
	if err != nil {
		t.Fatal(err)
	}

	url, fp := agentServer(t, e)
	e.srv.AgentFingerprint = fp
	r := c.post("/agents/tokens", "name", "Site", "days", "1", "max_uses", "1")
	code := regexp.MustCompile(`WZ1-[A-Za-z0-9_-]+`).FindString(r.body)
	d, err := agent.ParseEnrollCode(code)
	if err != nil {
		t.Fatal(err)
	}
	d.Server = url
	dir := t.TempDir()
	cfg, err := enrollWithAddresses(ctx, dir, d, []string{"10.20.0.5"})
	if err != nil {
		t.Fatal(err)
	}
	coll := cfg.DeviceID
	cs := "/devices/" + itoa64(coll)

	// only agents can collect; a device cannot be assigned to a non-collector
	if r := c.post("/devices/1/collector", "role", "on"); !strings.Contains(c.get(r.loc).body, "only a device with the WatchZit agent") {
		t.Fatal("a device without an agent became a collector")
	}
	if r := c.post("/devices/1/collector", "collector", itoa64(coll)); !strings.Contains(c.get(r.loc).body, "not a remote collector") {
		t.Fatal("assigned to a non-collector")
	}
	if r := c.post(cs+"/collector", "role", "on"); r.code != http.StatusSeeOther {
		t.Fatalf("role: %d", r.code)
	}
	if b := c.get("/devices/1").body; !strings.Contains(b, `<option value="`+itoa64(coll)+`" >Remote collector`) && !strings.Contains(b, `value="`+itoa64(coll)+`"`) {
		t.Fatal("the device page does not offer the collector")
	}
	c.post("/devices/1/collector", "collector", itoa64(coll))
	if dv, _ := e.st.DeviceByID(ctx, 1); dv.CollectorID != coll {
		t.Fatalf("assigned to %d", dv.CollectorID)
	}

	a := &agent.Agent{Dir: dir, Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sample: func(context.Context, []string) *proto.Report {
			return &proto.Report{Time: time.Now().UTC(), CPU: 1, MemTotal: 1 << 30}
		},
		Collect: func(context.Context) *proto.Inventory { return &proto.Inventory{Hostname: "probe1"} }}
	actx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = a.Run(actx); close(done) }()

	check := func() *store.Check { x, _ := e.st.CheckByID(ctx, tcp); return x }
	waitFor(t, "the collector's result", func() bool { x := check(); return x.Status == store.StatusUp && strings.Contains(x.LastText, "open") })
	if b := c.get(cs).body; !strings.Contains(b, "Runs the checks of 1 devices") {
		t.Fatal("the collector page does not say what it runs")
	}

	// the collector goes away: unknown, not down
	cancel()
	<-done
	waitFor(t, "unknown after the collector left", func() bool { return check().Status == store.StatusUnknown })

	// role off: the device is the console's again
	c.post(cs+"/collector", "role", "off")
	if dv, _ := e.st.DeviceByID(ctx, 1); dv.CollectorID != 0 || dv.CollectedBy != "" {
		t.Fatalf("still collected: %+v", dv)
	}
}
