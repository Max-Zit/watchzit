package web_test

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/agent"
	"github.com/max-zit/watchzit/internal/proto"
	"github.com/max-zit/watchzit/internal/store"
)

// TestAgentProcessesAndEvents: watched process names become checks the
// agent fills (none running is down), and error messages of the agent's
// system logs show under Logs and match log triggers' agent source.
func TestAgentProcessesAndEvents(t *testing.T) {
	agent.ProtectDir = func(string) error { return nil }
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	url, fp := agentServer(t, e)
	e.srv.AgentFingerprint = fp
	r := c.post("/agents/tokens", "name", "Servers", "days", "7")
	d, err := agent.ParseEnrollCode(regexp.MustCompile(`WZ1-[A-Za-z0-9_-]+`).FindString(r.body))
	if err != nil {
		t.Fatal(err)
	}
	d.Server = url
	dir := t.TempDir()
	cfg, err := agent.Enroll(ctx, dir, d)
	if err != nil {
		t.Fatal(err)
	}
	var sent atomic.Bool
	a := &agent.Agent{Dir: dir, Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sample:  func(context.Context, []string) *proto.Report { return &proto.Report{Time: time.Now().UTC()} },
		Collect: func(context.Context) *proto.Inventory { return &proto.Inventory{Hostname: "srv1"} },
		Procs: func(_ context.Context, names []string) []proto.Process {
			var out []proto.Process
			for _, n := range names {
				p := proto.Process{Name: n}
				if n == "nginx" {
					p.Count, p.CPU, p.Mem = 3, 1.5, 64<<20
				}
				out = append(out, p)
			}
			return out
		},
		Events: func(context.Context) []proto.Event {
			if sent.Swap(true) {
				return nil
			}
			return []proto.Event{{Time: time.Now().UTC(), Level: 3, Source: "disk", Log: "System", ID: 7, Message: "The device has a bad block."}}
		}}
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = a.Run(actx) }()
	waitFor(t, "the agent online", func() bool {
		ai, err := e.st.DeviceAgent(ctx, cfg.DeviceID)
		return err == nil && ai.Online
	})
	dev := "/devices/" + itoa(int32(cfg.DeviceID))
	if r := c.post(dev+"/processes", "processes", "nginx, sqlservr.exe; nginx"); r.code != 303 {
		t.Fatalf("processes: %d", r.code)
	}
	if r := c.post(dev+"/processes", "processes", "rm -rf /;"); !strings.Contains(c.get(dev).body, "is not a process name") && r.code != 303 {
		t.Fatal("a bad name was not refused")
	}
	waitFor(t, "process checks filled", func() bool {
		cs, _ := e.st.DeviceChecks(ctx, cfg.DeviceID)
		n := 0
		for _, x := range cs {
			if x.Type == store.CheckAgentProcess {
				if x.Params.Descr == "nginx" && x.Status == store.StatusUp && strings.Contains(x.LastText, "3 running") ||
					x.Params.Descr == "sqlservr.exe" && x.Status == store.StatusDown {
					n++
				}
			}
		}
		return n == 2
	})
	waitFor(t, "the event under Logs", func() bool {
		return strings.Contains(c.get("/logs?kind=agent").body, "The device has a bad block.")
	})
	if b := c.get("/logs?kind=agent").body; !strings.Contains(b, "System/disk") || !strings.Contains(b, "[7]") {
		t.Fatal("source or event ID missing")
	}
}
