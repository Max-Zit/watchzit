package web_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/alert"
	"github.com/max-zit/watchzit/internal/logrecv"
	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/store"
)

// TestLogs: syslog and traps are received after they are switched on,
// shown under Logs with filters, and a log trigger opens a problem that
// closes when no matching message came for its time.
func TestLogs(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "edge", "address", "127.0.0.1", "ping", "1")
	rcv := logrecv.New(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rcv.SetClock(func() time.Time { return e.now })
	e.srv.Logs = rcv
	defer rcv.Close()
	if r := c.post("/settings/logs", "syslog", "1", "syslog_port", "15514", "traps", "1", "trap_port", "15514", "keep_days", "30"); !strings.Contains(c.get(r.loc).body, "two different ports") {
		t.Fatal("the same port twice was accepted")
	}
	if r := c.post("/settings/logs", "syslog", "1", "syslog_port", "15514", "traps", "1", "trap_port", "15162", "keep_days", "30", "unknown", "1"); r.code != 303 {
		t.Fatalf("save: %d", r.code)
	}
	if r := c.get("/settings/logs"); !strings.Contains(r.body, `value="15514"`) || strings.Contains(r.body, `class="flash err"`) {
		t.Fatal("settings not shown or the ports did not open")
	}
	rcv.Refresh(ctx)
	send := func(port string, b []byte) {
		conn, err := net.Dial("udp", "127.0.0.1:"+port)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = conn.Write(b)
		conn.Close()
	}
	send("15514", []byte("<12>Oct 30 12:00:00 edge sshd[1]: Failed password for root from 203.0.113.9"))
	send("15514", []byte("<14>Oct 30 12:00:01 edge cron[2]: job done"))
	send("15162", snmp.EncodeTrapV2("public", snmp.MustOID("1.3.6.1.6.3.1.1.5.3"), 5))
	deadline := time.Now().Add(5 * time.Second)
	for {
		_ = rcv.Flush(ctx)
		if ls, _ := e.st.Logs(ctx, store.LogFilter{Limit: 10}); len(ls) == 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	r := c.get("/logs")
	for _, want := range []string{"Failed password for root", "job done", "linkDown", `href="/devices/1"`} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("logs page lacks %q", want)
		}
	}
	if r := c.get("/logs?q=failed&level=7"); !strings.Contains(r.body, "Failed password") || strings.Contains(r.body, "job done") {
		t.Fatal("text filter")
	}
	if r := c.get("/logs?kind=trap"); strings.Contains(r.body, "job done") || !strings.Contains(r.body, "linkDown") {
		t.Fatal("kind filter")
	}
	if r := c.get("/logs?level=4"); strings.Contains(r.body, "job done") || !strings.Contains(r.body, "Failed password") {
		t.Fatal("level filter")
	}

	// a trigger on "failed password" at level 4 or worse
	if r := c.post("/alerts/triggers", "name", "SSH attack", "kind", "log", "pattern", "/failed password/", "level", "4", "source", "syslog",
		"for", "10", "severity", "warning", "enabled", "1"); r.code != 303 {
		t.Fatalf("trigger: %d %s", r.code, r.body)
	}
	eng := alert.New(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	eng.SetClock(func() time.Time { return e.now })
	if err := eng.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	ps, _ := e.st.Problems(ctx, store.ProblemFilter{Open: true})
	if len(ps) != 1 || ps[0].Title != "SSH attack" || !strings.Contains(ps[0].Detail, "Failed password") {
		t.Fatalf("problems: %+v", ps)
	}
	e.now = e.now.Add(11 * time.Minute)
	_ = eng.Evaluate(ctx)
	if ps, _ := e.st.Problems(ctx, store.ProblemFilter{Open: true}); len(ps) != 0 {
		t.Fatal("the log problem did not close")
	}
	// a log problem is no downtime
	if rows, _ := e.st.Availability(ctx, e.now.Add(-time.Hour), e.now, 0); len(rows) == 1 && rows[0].Down != 0 {
		t.Fatal("a log problem counted as downtime")
	}
}
