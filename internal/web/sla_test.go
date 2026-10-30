package web_test

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/alert"
	"github.com/max-zit/watchzit/internal/store"
)

// TestSLAAndReportMail: a group with an SLA target shows met or missed in
// the report; the report goes by email now and on its weekly day, once.
func TestSLAAndReportMail(t *testing.T) {
	e := newEnv(t)
	e.now = time.Now().Truncate(time.Second) // devices are created on the database clock
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/groups", "name", "Branch")
	c.post("/devices/new", "name", "router", "address", "10.0.0.1", "ping", "1", "group", "1")
	if r := c.post("/groups/1", "name", "Branch", "sla", "120"); !strings.Contains(c.get(r.loc).body, "between 50 and 100") {
		t.Fatal("an SLA of 120 % was accepted")
	}
	c.post("/groups/1", "name", "Branch", "sla", "99,9")
	if gs, _ := e.st.Groups(ctx); len(gs) != 1 || gs[0].SLA != 99.9 {
		t.Fatalf("groups: %+v", gs)
	}
	d, _ := e.st.DeviceByID(ctx, 1)
	if d.GroupID == nil {
		c.post("/devices/1/edit", "name", "router", "address", "10.0.0.1", "kind", "router", "group", "1")
	}
	// down for 20 minutes: far below 99.9 % (the device is new)
	cs, _ := e.st.DeviceChecks(ctx, 1)
	eng := alert.New(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	eng.SetClock(func() time.Time { return e.now })
	result := func(status string) {
		_, _ = e.st.SaveResults(ctx, []store.CheckResult{{CheckID: cs[0].ID, At: e.now, Status: status, Text: status}})
		_ = eng.Evaluate(ctx)
	}
	result(store.StatusDown)
	e.now = e.now.Add(61 * time.Second)
	result(store.StatusDown)
	e.now = e.now.Add(20 * time.Minute)
	result(store.StatusUp)
	r := c.get("/reports?period=7d")
	if !strings.Contains(r.body, "SLA by group") || !strings.Contains(r.body, "missed") || !strings.Contains(r.body, "99.9%") {
		t.Fatal("report: SLA missing")
	}

	smtpPort, mails := fakeSMTP(t)
	c.post("/settings/notifications", "smtp_host", "127.0.0.1", "smtp_port", strconv.Itoa(smtpPort), "smtp_security", "none",
		"smtp_from", "WatchZit <watchzit@example.com>", "console_url", "https://monitor.example.com:8443", "lang", "")
	if r := c.post("/reports/mail", "every", "week", "to", "boss@example.com", "group", "0", "do", "now"); r.code != 303 {
		t.Fatalf("send now: %d", r.code)
	}
	got := func() string {
		select {
		case m := <-mails:
			return m
		case <-time.After(10 * time.Second):
			return ""
		}
	}
	m := got()
	for _, want := range []string{"Availability report", "SLA by group", "Branch: ", "missed", "multipart/mixed", "attachment; filename=\"watchzit-availability-"} {
		if !strings.Contains(m, want) {
			t.Fatalf("mail lacks %q:\n%s", want, m)
		}
	}
	// weekly: sent on Monday after 07:00, once
	c.post("/reports/mail", "every", "week", "to", "boss@example.com", "group", "0")
	for e.now.Local().Weekday() != time.Monday {
		e.now = e.now.Add(24 * time.Hour)
	}
	e.now = time.Date(e.now.Year(), e.now.Month(), e.now.Day(), 9, 0, 0, 0, time.Local)
	for i := 0; i < 2; i++ {
		if err := e.srv.SendDueReport(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got() == "" {
		t.Fatal("the weekly report was not sent")
	}
	select {
	case <-mails:
		t.Fatal("the weekly report was sent twice")
	case <-time.After(500 * time.Millisecond):
	}
	var sched struct{ Every, Last string }
	if err := e.st.GetSetting(ctx, "report_mail", &sched); err != nil || sched.Every != "week" || !strings.Contains(sched.Last, "-W") {
		t.Fatalf("schedule: %+v %v", sched, err)
	}
}
