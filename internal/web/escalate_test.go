package web_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/alert"
	"github.com/max-zit/watchzit/internal/store"
)

// TestEscalation: a first-line channel is told at once; a second-line one
// only when the problem is still open after 15 minutes, again every 30
// minutes, not after an acknowledgement; a night channel not during the
// day. "Resolved" goes only to channels that were told.
func TestEscalation(t *testing.T) {
	e := newEnv(t) // the clock stands on a Tuesday, 10:00 UTC
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "router", "address", "10.0.0.1", "ping", "1")
	cs, _ := e.st.DeviceChecks(ctx, 1)
	hooks := map[string]*hookServer{}
	for _, n := range []string{"first", "second", "night"} {
		h := &hookServer{}
		hs := httptest.NewServer(h)
		defer hs.Close()
		hooks[n] = h
		form := []string{"name", n, "kind", "webhook", "target", hs.URL, "secret", "hook-key", "min_severity", "info", "on_resolve", "1"}
		switch n {
		case "second":
			form = append(form, "delay_min", "15", "repeat_min", "30")
		case "night":
			form = append(form, "duty", "1", "from_hour", "18", "to_hour", "8", "day", "0", "day", "1", "day", "2", "day", "3", "day", "4", "day", "5", "day", "6")
		}
		if r := c.post("/settings/notifications/channels", form...); r.code != 303 {
			t.Fatalf("%s: %d", n, r.code)
		}
	}
	if r := c.post("/settings/notifications/channels", "name", "bad", "kind", "webhook", "target", "https://x.example", "repeat_min", "2"); r.code != 303 {
		t.Fatalf("bad: %d", r.code)
	}
	if chans, _ := e.st.Channels(ctx); len(chans) != 3 {
		t.Fatalf("a repeat of 2 minutes was accepted: %d channels", len(chans))
	}
	eng := alert.New(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	eng.SetClock(func() time.Time { return e.now })
	eng.Notify = e.srv.Notify
	step := func(d time.Duration) {
		t.Helper()
		e.now = e.now.Add(d)
		if err := eng.Evaluate(ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.srv.Escalate(ctx); err != nil {
			t.Fatal(err)
		}
		e.srv.WaitNotifications()
	}
	expect := func(when string, first, second, night int) {
		t.Helper()
		if f, s, n := len(hooks["first"].take()), len(hooks["second"].take()), len(hooks["night"].take()); f != first || s != second || n != night {
			t.Fatalf("%s: first %d second %d night %d, want %d %d %d", when, f, s, n, first, second, night)
		}
	}
	down := func() {
		if _, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: cs[0].ID, At: e.now, Status: store.StatusDown, Text: "down"}}); err != nil {
			t.Fatal(err)
		}
	}
	down()
	step(time.Second)
	down()
	step(61 * time.Second)
	expect("opened", 1, 0, 0)
	step(5 * time.Minute)
	expect("after 5 minutes", 0, 0, 0)
	step(10 * time.Minute)
	expect("after 16 minutes", 0, 1, 0)
	step(10 * time.Minute)
	expect("after 26 minutes", 0, 0, 0)
	step(21 * time.Minute)
	expect("repeated", 0, 1, 0)
	ps, _ := e.st.Problems(ctx, store.ProblemFilter{Open: true})
	if len(ps) != 1 {
		t.Fatalf("problems: %d", len(ps))
	}
	c.post("/problems/"+strconv.FormatInt(ps[0].ID, 10)+"/ack", "note", "on it")
	step(40 * time.Minute)
	expect("acknowledged", 0, 0, 0)
	// resolved: the night channel was never told
	if _, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: cs[0].ID, At: e.now, Status: store.StatusUp, Text: "up"}}); err != nil {
		t.Fatal(err)
	}
	step(time.Second)
	expect("resolved", 1, 1, 0)
}
