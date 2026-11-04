package web_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/agent"
	"github.com/max-zit/watchzit/internal/proto"
	"github.com/max-zit/watchzit/internal/store"
)

// TestCollectorBackupAndDiscovery: a backup collector takes over the
// checks of a device while its own collector is away (no "unknown" in
// between), and a discovery range scanned from a collector finds the hosts
// of its site and adds them as devices monitored from it.
func TestCollectorBackupAndDiscovery(t *testing.T) {
	agent.ProtectDir = func(string) error { return nil }
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	listen := func(addr string) *net.TCPAddr {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Skip("cannot listen on", addr, err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				_ = conn.Close()
			}
		}()
		return ln.Addr().(*net.TCPAddr)
	}
	port := listen("127.0.0.1:0").Port
	c.post("/devices/new", "name", "site-nas", "address", "127.0.0.1", "kind", "nas")
	tcp, err := e.st.CreateCheck(ctx, 1, store.CheckInput{Type: store.CheckTCP, Name: "SMB", Params: store.CheckParams{Port: port}, IntervalS: 10})
	if err != nil {
		t.Fatal(err)
	}

	url, fp := agentServer(t, e)
	e.srv.AgentFingerprint = fp
	enroll := func(addr string) (*agent.Config, string) {
		r := c.post("/agents/tokens", "name", "Site "+addr, "days", "1", "max_uses", "1")
		d, err := agent.ParseEnrollCode(regexp.MustCompile(`WZ1-[A-Za-z0-9_-]+`).FindString(r.body))
		if err != nil {
			t.Fatal(err)
		}
		d.Server = url
		dir := t.TempDir()
		cfg, err := enrollWithAddresses(ctx, dir, d, []string{addr})
		if err != nil {
			t.Fatal(err)
		}
		return cfg, dir
	}
	cfgA, dirA := enroll("10.20.0.5")
	cfgB, dirB := enroll("10.20.0.6")
	a, b := itoa64(cfgA.DeviceID), itoa64(cfgB.DeviceID)
	c.post("/devices/"+a+"/collector", "role", "on")
	c.post("/devices/"+b+"/collector", "role", "on")
	c.post("/devices/1/collector", "collector", a, "backup", b)
	if dv, _ := e.st.DeviceByID(ctx, 1); dv.CollectorID != cfgA.DeviceID || dv.BackupID != cfgB.DeviceID {
		t.Fatalf("assigned %d / %d", dv.CollectorID, dv.BackupID)
	}
	run := func(cfg *agent.Config, dir string) context.CancelFunc {
		ag := &agent.Agent{Dir: dir, Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Sample: func(context.Context, []string) *proto.Report {
				return &proto.Report{Time: time.Now().UTC(), CPU: 1, MemTotal: 1 << 30}
			},
			Collect: func(context.Context) *proto.Inventory { return &proto.Inventory{Hostname: "probe"} }}
		actx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { _ = ag.Run(actx); close(done) }()
		return func() { cancel(); <-done }
	}
	stopA := run(cfgA, dirA)
	stopB := run(cfgB, dirB)
	defer stopB()
	check := func() *store.Check { x, _ := e.st.CheckByID(ctx, tcp); return x }
	waitFor(t, "a result from collector A", func() bool { return check().Status == store.StatusUp })

	// A goes away: B takes over, the check is not set to unknown
	stopA()
	time.Sleep(time.Second)
	if check().Status == store.StatusUnknown {
		t.Fatal("the check became unknown although a backup is there")
	}
	// a marker only a new run overwrites
	if _, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: tcp, At: e.now, Status: store.StatusDown, Text: "marker"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a result from the backup", func() bool { x := check(); return x.Status == store.StatusUp && x.LastText != "marker" })

	// discovery from the backup's site
	if b := c.get("/discovery").body; !strings.Contains(b, `name="collector"`) {
		t.Fatal("the range form does not offer the collectors")
	}
	listen("127.0.0.2:9100")
	c.post("/discovery/ranges", "cidr", "127.0.0.2", "every", "", "collector", b, "now", "1")
	var host *store.DiscoveredHost
	waitFor(t, "the host found by the collector", func() bool {
		hs, _ := e.st.DiscoveredHosts(ctx, false)
		for _, h := range hs {
			if h.Address.String() == "127.0.0.2" {
				host = h
			}
		}
		return host != nil
	})
	if host.Collector != cfgB.DeviceID {
		t.Fatalf("found by %d", host.Collector)
	}
	c.post("/discovery/hosts", "addr", "127.0.0.2", "do", "add")
	ds, _ := e.st.Devices(ctx, store.DeviceFilter{})
	for _, d := range ds {
		if d.Address == "127.0.0.2" && d.CollectorID != cfgB.DeviceID {
			t.Fatalf("the new device is monitored from %d", d.CollectorID)
		}
	}
}
