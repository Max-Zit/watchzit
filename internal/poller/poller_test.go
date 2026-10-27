package poller

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/snmpmon"
	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/testdb"
	"github.com/max-zit/watchzit/internal/tsdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

// TestPollerRunsChecks polls a TCP listener, a closed port and the
// loopback address and checks status, device state and samples.
func TestPollerRunsChecks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := tsdb.New(st.Pool(), log)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	closedPort := closed.Addr().(*net.TCPAddr).Port
	_ = closed.Close()

	dev, err := st.CreateDevice(ctx, store.DeviceInput{Name: "local", Address: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	open, _ := st.CreateCheck(ctx, dev, store.CheckInput{Type: store.CheckTCP, Params: store.CheckParams{Port: l.Addr().(*net.TCPAddr).Port}})
	shut, _ := st.CreateCheck(ctx, dev, store.CheckInput{Type: store.CheckTCP, Params: store.CheckParams{Port: closedPort}})
	ping, _ := st.CreateCheck(ctx, dev, store.CheckInput{Type: store.CheckICMP, Params: store.CheckParams{Count: 1}})

	var changes []store.StatusChange
	p := New(st, db, log)
	p.OnChange = func(cs []store.StatusChange) { changes = append(changes, cs...) }
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		c, _ := st.CheckByID(ctx, shut)
		o, _ := st.CheckByID(ctx, open)
		if c.LastRun != nil && o.LastRun != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	cancel()
	<-done
	ctx = context.Background()

	o, _ := st.CheckByID(ctx, open)
	c, _ := st.CheckByID(ctx, shut)
	if o.Status != store.StatusUp || !strings.HasPrefix(o.LastText, "open,") {
		t.Fatalf("open port: %s %q", o.Status, o.LastText)
	}
	if c.Status != store.StatusDown || c.LastText != "connection refused" {
		t.Fatalf("closed port: %s %q", c.Status, c.LastText)
	}
	pc, _ := st.CheckByID(ctx, ping)
	pingWorks := pc.Status == store.StatusUp
	if !pingWorks && !strings.Contains(pc.LastText, "not allowed") {
		t.Fatalf("ping of 127.0.0.1: %s %q", pc.Status, pc.LastText)
	}
	d, _ := st.DeviceByID(ctx, dev)
	if d.Status != store.StatusWarning {
		t.Fatalf("device %s", d.Status)
	}
	if len(changes) < 2 {
		t.Fatalf("%d status changes reported", len(changes))
	}
	// The samples reach the store (tsdb.Run flushes every two seconds).
	if err := db.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM samples_hot`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	want := 1 // connect time of the open port
	if pingWorks {
		want += 2 // rtt and loss
	}
	if n < want {
		t.Fatalf("%d samples, want at least %d", n, want)
	}
}

// TestSNMPRates polls an interface twice and checks the traffic rates.
func TestSNMPRates(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UseSecretKey(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	agent, err := snmp.NewTestAgent("pub")
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	snmpmon.LoadTestDevice(agent)

	prof, err := st.CreateSNMPProfile(ctx, store.SNMPProfileInput{Name: "test", Version: 2, Port: int(agent.Addr().Port()), Community: "pub"})
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.CreateDevice(ctx, store.DeviceInput{Name: "router", Address: "127.0.0.1", SNMP: prof})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateCheck(ctx, dev, store.CheckInput{Type: store.CheckSNMPIf, Params: store.CheckParams{IfIndex: 1, IfName: "ether1"}})
	if err != nil {
		t.Fatal(err)
	}
	p := New(st, tsdb.New(st.Pool(), log), log)
	if err := p.load(ctx); err != nil {
		t.Fatal(err)
	}
	c, _ := st.CheckByID(ctx, id)
	t0 := time.Now()
	p.now = func() time.Time { return t0 }
	snmpmon.SetTestTraffic(agent, 1, 1<<40, 1000)
	if o := p.runCheck(ctx, c); o.res.Status != store.StatusUp || len(o.samples) != 0 {
		t.Fatalf("first poll: %+v", o)
	}
	p.now = func() time.Time { return t0.Add(10 * time.Second) }
	snmpmon.SetTestTraffic(agent, 1, 1<<40+12500, 26000)
	o := p.runCheck(ctx, c)
	got := map[string]float64{}
	for _, s := range o.samples {
		for _, x := range c.Series {
			if x.ID == s.Series {
				got[x.Metric] = s.V
			}
		}
	}
	if got["in_bps"] != 10000 || got["out_bps"] != 20000 || got["in_err"] != 0 {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(o.res.Text, "↓ 10 kbit/s") {
		t.Fatal(o.res.Text)
	}

	// Refresh stores facts and interfaces for the map and the port tiles.
	p.Refresh(ctx, dev)
	ifs, _ := st.Interfaces(ctx, dev)
	f, _ := st.DeviceFacts(ctx, dev)
	if len(ifs) != 2 || ifs[0].Name != "ether1" || f.Vendor != "MikroTik" {
		t.Fatalf("%+v %+v", ifs, f)
	}
}

func TestRate(t *testing.T) {
	c := func(v uint64, bits int) snmpmon.Counter { return snmpmon.Counter{V: v, Bits: bits, Mul: 1} }
	if r, ok := rate(c(100, 32), c(300, 32), 10); !ok || r != 20 {
		t.Fatal(r, ok)
	}
	if r, ok := rate(c(1<<32-100, 32), c(100, 32), 10); !ok || r != 20 { // wrapped
		t.Fatal(r, ok)
	}
	if _, ok := rate(c(500, 64), c(100, 64), 10); ok { // reset
		t.Fatal("reset counted")
	}
	if _, ok := rate(c(1, 32), c(2, 32), 0.5); ok {
		t.Fatal("too short interval counted")
	}
}
