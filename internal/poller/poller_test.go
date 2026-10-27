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
