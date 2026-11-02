package web_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/agent"
	"github.com/max-zit/watchzit/internal/proto"
)

// TestAgentSelfUpdate: an agent older than the server downloads the
// server's agent over its pinned channel, checks it and installs it; with
// automatic updates off it is left alone until an administrator asks.
func TestAgentSelfUpdate(t *testing.T) {
	agent.ProtectDir = func(string) error { return nil }
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	url, fp := agentServer(t, e)
	e.srv.AgentFingerprint = fp
	e.srv.Version = "9.9.9"
	oldVersion := agent.Version
	agent.Version = "9.9.8"
	defer func() { agent.Version = oldVersion }()
	dl := t.TempDir()
	e.srv.DownloadsDir = dl
	name := "watchzit-agent-linux-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name = "watchzit-agent-x64.msi"
	}
	pkg := []byte("the agent of 9.9.9 for this system")
	if err := os.WriteFile(filepath.Join(dl, name), pkg, 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var installed []string
	oldInstall := agent.Install
	agent.Install = func(path, version, _ string) error {
		b, _ := os.ReadFile(path)
		mu.Lock()
		installed = append(installed, version+":"+string(b))
		mu.Unlock()
		return nil
	}
	defer func() { agent.Install = oldInstall }()
	got := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), installed...)
	}

	// automatic updates off first
	c.post("/agents/updates", "auto", "")
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
	a := &agent.Agent{Dir: dir, Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sample:  func(context.Context, []string) *proto.Report { return &proto.Report{Time: time.Now().UTC()} },
		Collect: func(context.Context) *proto.Inventory { return &proto.Inventory{Hostname: "pc1"} }}
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = a.Run(actx) }()
	waitFor(t, "the agent online", func() bool {
		ai, err := e.st.DeviceAgent(ctx, cfg.DeviceID)
		return err == nil && ai.Online
	})
	time.Sleep(500 * time.Millisecond)
	if len(got()) != 0 {
		t.Fatal("updated although automatic updates are off")
	}
	if b := c.get("/agents").body; !strings.Contains(b, "older") || !strings.Contains(b, "9.9.9") {
		t.Fatal("the agents page does not show the older agent")
	}
	// an administrator updates all online agents now
	c.post("/agents/updates", "do", "now")
	if !strings.Contains(c.get("/agents").body, "1 agents are updating") {
		t.Fatal("no agent was asked to update")
	}
	waitFor(t, "the update installed", func() bool { return len(got()) == 1 })
	if got()[0] != "9.9.9:"+string(pkg) {
		t.Fatalf("installed %q", got()[0])
	}
	if b := c.get("/audit").body; !strings.Contains(b, "agent.update") {
		t.Fatal("the update is not in the audit log")
	}
}
