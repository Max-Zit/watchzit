package web_test

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/alert"
	"github.com/max-zit/watchzit/internal/store"
)

// hookServer records webhook posts and checks their signature.
type hookServer struct {
	mu     sync.Mutex
	events []map[string]any
	badSig bool
}

func (h *hookServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m := hmac.New(sha256.New, []byte("hook-key"))
	m.Write(body)
	var p struct {
		Source string           `json:"source"`
		Events []map[string]any `json:"events"`
	}
	_ = json.Unmarshal(body, &p)
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.Header.Get("X-WatchZit-Signature") != "sha256="+hex.EncodeToString(m.Sum(nil)) || p.Source != "WatchZit" {
		h.badSig = true
	}
	h.events = append(h.events, p.Events...)
}

// take returns "event device title" of the posts so far and forgets them.
func (h *hookServer) take() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.events {
		out = append(out, e["event"].(string)+" "+e["device"].(string)+" "+e["title"].(string))
	}
	h.events = nil
	return out
}

// fakeSMTP accepts messages without TLS or sign-in and keeps their text.
func fakeSMTP(t *testing.T) (port int, mails chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	mails = make(chan string, 20)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				say := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
				say("220 test")
				var data strings.Builder
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						say("250 test")
					case cmd == "DATA":
						say("354 go on")
						for {
							l, err := r.ReadString('\n')
							if err != nil {
								return
							}
							if l == ".\r\n" {
								break
							}
							data.WriteString(l)
						}
						mails <- data.String()
						say("250 queued")
					case cmd == "QUIT":
						say("221 bye")
						return
					default:
						say("250 ok")
					}
				}
			}(c)
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, mails
}

func TestAlerts(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "router", "address", "10.0.0.1", "ping", "1")
	c.post("/devices/new", "name", "pc", "address", "10.0.0.2", "ping", "1")
	// pc depends on router; a loop is refused
	if r := c.post("/devices/2/edit", "name", "pc", "address", "10.0.0.2", "kind", "pc", "parent", "1"); r.code != http.StatusSeeOther {
		t.Fatalf("parent: %d %s", r.code, r.body)
	}
	if r := c.post("/devices/1/edit", "name", "router", "address", "10.0.0.1", "kind", "router", "parent", "2"); r.code != http.StatusBadRequest || !strings.Contains(r.body, "cannot depend") {
		t.Fatalf("dependency loop accepted: %d", r.code)
	}
	if r := c.get("/devices/2"); !strings.Contains(r.body, "Depends on") || !strings.Contains(r.body, `href="/devices/1"`) {
		t.Fatal("device page does not show the dependency")
	}
	ping := func(dev int64) int64 {
		cs, _ := e.st.DeviceChecks(ctx, dev)
		return cs[0].ID
	}
	routerPing, pcPing := ping(1), ping(2)

	// channels: a signed webhook and email
	hook := &hookServer{}
	hs := httptest.NewServer(hook)
	defer hs.Close()
	smtpPort, mails := fakeSMTP(t)
	if r := c.post("/settings/notifications", "smtp_host", "127.0.0.1", "smtp_port", strconv.Itoa(smtpPort), "smtp_security", "none",
		"smtp_from", "WatchZit <watchzit@example.com>", "console_url", "https://monitor.example.com:8443", "lang", ""); r.code != http.StatusSeeOther {
		t.Fatalf("smtp settings: %d %s", r.code, r.body)
	}
	if r := c.post("/settings/notifications", "smtp_host", "smtp.example.com", "smtp_port", "25", "smtp_security", "none", "smtp_user", "u", "smtp_password", "p",
		"smtp_from", "x@example.com"); r.code != http.StatusBadRequest {
		t.Fatalf("a password over plain SMTP was accepted: %d", r.code)
	}
	c.post("/settings/notifications/channels", "name", "Hook", "kind", "webhook", "target", hs.URL, "secret", "hook-key", "min_severity", "info", "on_resolve", "1")
	c.post("/settings/notifications/channels", "name", "NOC mail", "kind", "email", "target", "noc@example.com", "min_severity", "critical")
	if r := c.post("/settings/notifications/channels", "name", "Bad", "kind", "slack", "target", "ftp://x"); !strings.Contains(c.get(r.loc).body, "https:// address") {
		t.Fatal("a bad channel address was accepted")
	}
	chans, _ := e.st.Channels(ctx)
	if len(chans) != 2 {
		t.Fatalf("channels: %+v", chans)
	}
	for _, ch := range chans {
		if ch.Kind == store.ChannelWebhook && strings.Contains(ch.Target, "127.0.0.1") {
			t.Fatal("the webhook address is stored in clear")
		}
	}
	// test buttons
	if r := c.post("/settings/notifications/channels/" + strconv.FormatInt(chans[0].ID, 10) + "/test"); r.code != http.StatusSeeOther {
		t.Fatalf("test: %d", r.code)
	}
	c.post("/settings/notifications/test", "to", "me@example.com")
	select {
	case m := <-mails:
		if !strings.Contains(m, "Test message") {
			t.Fatalf("test mail: %s", m)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no test mail")
	}
	hook.take()
	for len(mails) > 0 {
		<-mails
	}

	eng := alert.New(e.st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	eng.SetClock(func() time.Time { return e.now })
	eng.Notify = e.srv.Notify
	e.srv.Alerts = eng
	eval := func() {
		t.Helper()
		if err := eng.Evaluate(ctx); err != nil {
			t.Fatal(err)
		}
		e.srv.WaitNotifications()
	}
	result := func(check int64, status string, vals map[string]float64) {
		t.Helper()
		if _, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: check, At: e.now, Status: status, Text: status + " text", Values: vals}}); err != nil {
			t.Fatal(err)
		}
	}
	openProblems := func() []*store.Problem {
		ps, _ := e.st.Problems(ctx, store.ProblemFilter{Open: true, Suppressed: true})
		return ps
	}

	// both down: after the delay the router's problem is sent, the pc's is
	// only "unreachable"
	result(routerPing, "down", nil)
	result(pcPing, "down", nil)
	eval()
	if len(openProblems()) != 0 || len(hook.take()) != 0 {
		t.Fatal("a problem opened before the delay")
	}
	e.now = e.now.Add(61 * time.Second)
	eval()
	ps := openProblems()
	if len(ps) != 2 {
		t.Fatalf("problems: %d", len(ps))
	}
	for _, p := range ps {
		if p.Device == "pc" && p.Suppressed != "dependency" || p.Device == "router" && (p.Suppressed != "" || p.Severity != "critical" || p.Title != "Device down") {
			t.Fatalf("problem %+v", p)
		}
	}
	if got := hook.take(); len(got) != 1 || got[0] != "problem router Device down" || hook.badSig {
		t.Fatalf("webhook: %v (bad signature %v)", got, hook.badSig)
	}
	select {
	case m := <-mails:
		for _, want := range []string{"Subject: =?utf-8?q?[WatchZit]_Critical:_router", "To: noc@example.com", "https://monitor.example.com:8443/devices/1", "ping"} {
			if !strings.Contains(strings.ToLower(m), strings.ToLower(want)) {
				t.Fatalf("mail lacks %q:\n%s", want, m)
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no problem mail")
	}
	// the sidebar counts only problems that were sent
	if r := c.get("/problems"); !strings.Contains(r.body, `<span class="navcount">1</span>`) || !strings.Contains(r.body, "router") || strings.Contains(r.body, ">pc<") {
		t.Fatal("problems page")
	}
	if r := c.get("/problems?suppressed=1"); !strings.Contains(r.body, "Unreachable") {
		t.Fatal("suppressed problems not shown on request")
	}

	// router back: its problem resolves (sent), the pc's is sent now
	e.now = e.now.Add(time.Minute)
	result(routerPing, "up", map[string]float64{"rtt_ms": 1, "loss_pct": 0})
	eval()
	got := hook.take()
	if len(got) != 2 || !contains(got, "resolved router Device down") || !contains(got, "problem pc Device down") {
		t.Fatalf("after recovery: %v", got)
	}
	// acknowledge
	ps = openProblems()
	if len(ps) != 1 {
		t.Fatalf("open: %d", len(ps))
	}
	if r := c.post("/problems/"+strconv.FormatInt(ps[0].ID, 10)+"/ack", "note", "on my way", "back", "//evil.example"); r.code != http.StatusSeeOther || r.loc != "/problems" {
		t.Fatalf("ack: %d %s", r.code, r.loc)
	}
	if p, _ := e.st.ProblemByID(ctx, ps[0].ID); p.AckBy != "admin" || p.AckNote != "on my way" {
		t.Fatalf("ack: %+v", p)
	}
	if r := c.get("/"); !strings.Contains(r.body, "Device down") || !strings.Contains(r.body, "admin") {
		t.Fatal("overview lacks the problem")
	}
	result(pcPing, "up", map[string]float64{"rtt_ms": 1, "loss_pct": 0})
	eval()
	if got := hook.take(); len(got) != 1 || got[0] != "resolved pc Device down" {
		t.Fatalf("pc recovery: %v", got)
	}
	if r := c.get("/problems?view=history"); strings.Count(r.body, "Device down") != 2 {
		t.Fatal("history")
	}

	// a value trigger with a delay, inside maintenance: recorded, sent after
	from := e.now.Add(-time.Minute).Local().Format("2006-01-02T15:04")
	to := e.now.Add(time.Hour).Local().Format("2006-01-02T15:04")
	if r := c.post("/alerts/maintenance", "name", "Firmware", "from", from, "to", to, "device", "1"); r.code != http.StatusSeeOther {
		t.Fatalf("maintenance: %d %s", r.code, r.body)
	}
	result(routerPing, "up", map[string]float64{"rtt_ms": 3, "loss_pct": 50})
	eval()
	e.now = e.now.Add(4 * time.Minute)
	eval()
	if len(openProblems()) != 0 {
		t.Fatal("packet loss opened before 5 minutes")
	}
	e.now = e.now.Add(90 * time.Second)
	eval()
	ps = openProblems()
	if len(ps) != 1 || ps[0].Title != "Packet loss" || ps[0].Suppressed != "maintenance" || !strings.Contains(ps[0].Detail, "50% > 20%") {
		t.Fatalf("packet loss: %+v", ps)
	}
	if len(hook.take()) != 0 {
		t.Fatal("sent during maintenance")
	}
	ms, _ := e.st.Maintenances(ctx, e.now.Add(-time.Hour))
	if r := c.post("/alerts/maintenance/" + strconv.FormatInt(ms[0].ID, 10) + "/end"); r.code != http.StatusSeeOther {
		t.Fatalf("end: %d", r.code)
	}
	e.now = e.now.Add(time.Second)
	eval()
	if got := hook.take(); len(got) != 1 || got[0] != "problem router Packet loss" {
		t.Fatalf("after maintenance: %v", got)
	}

	// triggers: bad input refused; disabling the trigger resolves its problem
	if r := c.post("/alerts/triggers", "name", "X", "kind", "metric", "metric", "nope", "op", ">", "threshold", "1", "severity", "warning", "enabled", "1"); r.code != http.StatusBadRequest {
		t.Fatalf("bad trigger: %d", r.code)
	}
	if r := c.post("/alerts/triggers", "name", "Slow ping", "kind", "metric", "metric", "rtt_ms", "op", ">", "threshold", "2,5", "for", "0", "severity", "info",
		"scope", "d1", "type", "icmp", "enabled", "1"); r.code != http.StatusSeeOther {
		t.Fatalf("trigger: %d %s", r.code, r.body)
	}
	eval()
	if got := hook.take(); len(got) != 1 || got[0] != "problem router Slow ping" {
		t.Fatalf("slow ping: %v", got)
	}
	ts, _ := e.st.Triggers(ctx)
	var loss *store.Trigger
	for _, x := range ts {
		if x.Name == "Packet loss" {
			loss = x
		}
	}
	if r := c.post("/alerts/triggers/"+strconv.FormatInt(loss.ID, 10), "name", "Packet loss", "kind", "metric", "metric", "loss_pct", "op", ">",
		"threshold", "20", "for", "5", "severity", "warning", "type", "icmp"); r.code != http.StatusSeeOther {
		t.Fatalf("disable: %d %s", r.code, r.body)
	}
	eval()
	if got := hook.take(); len(got) != 1 || got[0] != "resolved router Packet loss" {
		t.Fatalf("disabled trigger: %v", got)
	}
	for _, p := range []string{"/problems", "/problems?view=history", "/alerts/triggers", "/alerts/triggers/" + strconv.FormatInt(loss.ID, 10), "/alerts/maintenance", "/settings/notifications", "/devices/1"} {
		if r := c.get(p); r.code != http.StatusOK {
			t.Fatalf("%s: %d", p, r.code)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
