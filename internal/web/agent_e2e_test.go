package web_test

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/agent"
	"github.com/max-zit/watchzit/internal/proto"
	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/tlsutil"
)

// agentServer runs the agent endpoint over real TLS with client certificates.
func agentServer(t *testing.T, e *env) (url, fingerprint string) {
	t.Helper()
	certPEM, keyPEM, err := tlsutil.SelfSigned("test agent endpoint", 1)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(e.srv.AgentHandler())
	ts.TLS = tlsutil.AgentServerConfig(cert)
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts.URL, tlsutil.Fingerprint(cert)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func f64(v float64) *float64 { return &v }

// TestAgentEnrollAndReport: a code from the console enrolls a computer
// (joining an existing device by address), the agent's report becomes
// checks with values, services are watched, and the agent goes offline.
func TestAgentEnrollAndReport(t *testing.T) {
	agent.ProtectDir = func(string) error { return nil }
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "file-server", "address", "10.9.8.7")
	url, fp := agentServer(t, e)
	e.srv.AgentFingerprint = fp

	// a code with a limit of one computer
	r := c.post("/agents/tokens", "name", "Servers", "days", "7", "max_uses", "1")
	m := regexp.MustCompile(`WZ1-[A-Za-z0-9_-]+`).FindString(r.body)
	if r.code != http.StatusOK || m == "" {
		t.Fatalf("token: %d %s", r.code, r.body)
	}
	d, err := agent.ParseEnrollCode(m)
	if err != nil {
		t.Fatal(err)
	}
	if d.Fingerprint != fp {
		t.Fatalf("fingerprint in code %q", d.Fingerprint)
	}
	d.Server = url // the code names the console host; the test server listens elsewhere
	dir := t.TempDir()
	// a wrong token is refused for good
	bad := d
	bad.Token = "wzet_wrong"
	if _, err := agent.Enroll(ctx, t.TempDir(), bad); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("wrong token: %v", err)
	}
	// a wrong pin is refused too
	pin := d
	pin.Fingerprint = strings.Repeat("ab", 32)
	if _, err := agent.Enroll(ctx, t.TempDir(), pin); err == nil || !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("wrong pin: %v", err)
	}
	// the agent names the existing device's address: it joins that device
	cfg, err := enrollWithAddresses(ctx, dir, d, []string{"10.9.8.7"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeviceID != 1 || cfg.Device != "file-server" {
		t.Fatalf("joined %+v", cfg)
	}
	if _, err := agent.Enroll(ctx, t.TempDir(), d); err == nil {
		t.Fatal("a code for one computer enrolled two")
	}

	report := &proto.Report{CPU: 37.5, Load1: f64(0.42), MemTotal: 8 << 30, MemUsed: 2 << 30, Uptime: 90061,
		Disks: []proto.Disk{{Mount: "/", FS: "ext4", Total: 100 << 30, Used: 25 << 30}, {Mount: "/boot/efi", Total: 1 << 20, Used: 1 << 19}},
		Nets: []proto.Net{{Index: 2, Name: "eth0", MAC: "52:54:00:12:34:56", Speed: 1e9, Up: true, InBps: f64(1.5e6), OutBps: f64(250e3), InErr: f64(0), OutErr: f64(0), VLANs: []int{10, 20}},
			{Index: 3, Name: "eth1", MAC: "52:54:00:12:34:57", Up: false}}}
	var askedServices []string
	a := &agent.Agent{Dir: dir, Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sample: func(_ context.Context, svcs []string) *proto.Report {
			askedServices = svcs
			r := *report
			r.Time = time.Now().UTC()
			for _, n := range svcs {
				r.Services = append(r.Services, proto.Service{Name: n, State: "failed"})
			}
			return &r
		},
		Collect: func(context.Context) *proto.Inventory {
			return &proto.Inventory{Hostname: "fs01", OS: "Debian GNU/Linux 12 (bookworm)", Kernel: "6.1.0", CPUModel: "Test CPU", CPUs: 4, MemTotal: 8 << 30,
				Services: []proto.ServiceInfo{{Name: "nginx", Display: "nginx web server", Auto: true}, {Name: "cron", Auto: true}}}
		}}
	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = a.Run(actx); close(done) }()

	waitFor(t, "agent checks", func() bool {
		cs, _ := e.st.DeviceChecks(ctx, 1)
		return len(cs) >= 5
	})
	waitFor(t, "inventory", func() bool {
		ai, err := e.st.DeviceAgent(ctx, 1)
		return err == nil && len(ai.Inventory.Services) == 2 && ai.Online
	})
	cs, _ := e.st.DeviceChecks(ctx, 1)
	types := map[string]*store.Check{}
	for _, x := range cs {
		types[x.Type+"|"+x.Name] = x
	}
	for _, want := range []string{"agent|Agent", "agent_cpu|CPU", "agent_mem|Memory", "agent_disk|/", "agent_net|eth0"} {
		if types[want] == nil {
			t.Fatalf("no %s check: %v", want, keys(types))
		}
	}
	if types["agent_disk|/boot/efi"] != nil || types["agent_net|eth1"] != nil {
		t.Fatal("tiny disk or interface without link became a check")
	}
	if x := types["agent_cpu|CPU"]; x.Status != store.StatusUp || x.LastText != "38%, load 0.42" {
		t.Fatalf("cpu: %s %q", x.Status, x.LastText)
	}
	if x := types["agent_net|eth0"]; !strings.Contains(x.LastText, "1.5 Mbit/s") {
		t.Fatalf("net: %q", x.LastText)
	}
	ifs, _ := e.st.Interfaces(ctx, 1)
	if len(ifs) != 2 || ifs[0].Name != "eth0" || !ifs[0].OperUp {
		t.Fatalf("interfaces for the map: %+v", ifs)
	}
	if vs, _ := e.st.DeviceVLANs(ctx, 1); len(vs) != 2 || vs[0].ID != 10 || len(vs[1].Tagged) != 1 || vs[1].Tagged[0] != "eth0" {
		t.Fatalf("VLANs from the agent: %+v", vs)
	}

	// the device page shows the agent; watching a service tells the agent
	r = c.get("/devices/1")
	for _, want := range []string{"Debian GNU/Linux 12", "Test CPU", "nginx web server", "Watch services"} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("device page lacks %q", want)
		}
	}
	if r := c.post("/devices/1/services", "service", "nginx", "service", "not-on-the-computer"); r.code != http.StatusSeeOther {
		t.Fatalf("services: %d %s", r.code, r.body)
	}
	waitFor(t, "service check down", func() bool {
		cs, _ := e.st.DeviceChecks(ctx, 1)
		for _, x := range cs {
			if x.Type == store.CheckAgentService && x.Params.Descr == "nginx" && x.Status == store.StatusDown && x.LastText == "failed" {
				return true
			}
		}
		return false
	})
	if len(askedServices) != 1 || askedServices[0] != "nginx" {
		t.Fatalf("agent watches %v", askedServices)
	}
	if dv, _ := e.st.DeviceByID(ctx, 1); dv.Status != store.StatusWarning {
		t.Fatalf("device status %s", dv.Status)
	}
	if r := c.get("/agents"); !strings.Contains(r.body, "file-server") || !strings.Contains(r.body, "Servers") {
		t.Fatal("agents page")
	}

	// the agent goes away: offline after the channel closes
	cancel()
	<-done
	waitFor(t, "agent offline", func() bool {
		cs, _ := e.st.DeviceChecks(ctx, 1)
		for _, x := range cs {
			if x.Type == store.CheckAgent {
				return x.Status == store.StatusDown && x.LastText == "agent offline"
			}
		}
		return false
	})

	// removing the agent makes the key unknown
	if r := c.post("/devices/1/agent/remove"); r.code != http.StatusSeeOther {
		t.Fatalf("remove: %d", r.code)
	}
	if _, err := e.st.DeviceAgent(ctx, 1); err == nil {
		t.Fatal("agent still on the device")
	}
	_ = os.RemoveAll(dir)
}

// enrollWithAddresses enrolls like the agent but names the addresses
// (the test machine's own would not match the device).
func enrollWithAddresses(ctx context.Context, dir string, d agent.EnrollData, addrs []string) (*agent.Config, error) {
	agent.TestAddresses = addrs
	defer func() { agent.TestAddresses = nil }()
	return agent.Enroll(ctx, dir, d)
}

func keys(m map[string]*store.Check) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
