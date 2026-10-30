package web_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/max-zit/watchzit/internal/web"
)

// TestMoreChannels: Discord, ntfy, Telegram, PagerDuty, Twilio and an SMS
// gateway get the test message in their own format; wrong settings are
// refused and tokens are never shown.
func TestMoreChannels(t *testing.T) {
	var mu sync.Mutex
	got := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		u, p, _ := r.BasicAuth()
		mu.Lock()
		got[strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[0]] = r.Method + " " + r.URL.String() + " " + r.Header.Get("Title") + "|" + r.Header.Get("Priority") + "|" + r.Header.Get("Authorization") + "|" + u + ":" + p + " " + string(b)
		mu.Unlock()
		if strings.Contains(r.URL.Path, "bad") {
			w.WriteHeader(401)
		}
	}))
	defer srv.Close()
	web.SetChannelAPIs(srv.URL)
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	add := func(kv ...string) resp {
		return c.post("/settings/notifications/channels", append([]string{"min_severity", "warning", "on_resolve", "1"}, kv...)...)
	}
	sid := "AC" + strings.Repeat("a", 32)
	for _, f := range [][]string{
		{"name", "D", "kind", "discord", "target", srv.URL + "/discord/hook"},
		{"name", "N", "kind", "ntfy", "target", srv.URL + "/ntfy/topic", "secret", "tk_ntfy"},
		{"name", "T", "kind", "telegram", "target", "-1001234567890", "bot_token", "123456789:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"name", "P", "kind", "pagerduty", "target", strings.Repeat("b", 32)},
		{"name", "S", "kind", "twilio", "target", "+381601234567", "tw_sid", sid, "tw_token", "tokentokentoken12", "tw_from", "+15551234567"},
		{"name", "G", "kind", "smsgw", "target", srv.URL + "/smsgw/send?to=%2B381601234567&text={text}"},
	} {
		if r := add(f...); r.code != http.StatusSeeOther {
			t.Fatalf("%s: %d", f[1], r.code)
		}
	}
	// refused: no bot token, a bad chat ID, a bad key, a gateway without {text}
	for _, bad := range [][]string{
		{"name", "x1", "kind", "telegram", "target", "-100123"},
		{"name", "x2", "kind", "telegram", "target", "chat id", "bot_token", "123456789:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		{"name", "x3", "kind", "pagerduty", "target", "short"},
		{"name", "x4", "kind", "twilio", "target", "0601234567", "tw_sid", sid, "tw_token", "tokentokentoken12", "tw_from", "+15551234567"},
		{"name", "x5", "kind", "smsgw", "target", srv.URL + "/smsgw/send"},
	} {
		add(bad...)
	}
	chans, _ := e.st.Channels(context.Background())
	if len(chans) != 6 {
		t.Fatalf("channels: %d", len(chans))
	}
	for _, ch := range chans {
		if strings.Contains(ch.Secret+ch.Target, "AAAAAAAA") || strings.Contains(ch.Secret, "tokentoken") {
			t.Fatalf("%s: secret not sealed", ch.Name)
		}
		if r := c.post("/settings/notifications/channels/" + strconv.FormatInt(ch.ID, 10) + "/test"); r.code != http.StatusSeeOther {
			t.Fatalf("test %s: %d", ch.Name, r.code)
		}
	}
	e.srv.WaitNotifications()
	mu.Lock()
	defer mu.Unlock()
	for k, want := range map[string][]string{
		"discord":   {"POST /discord/hook", `"content":"`, "Test notification"},
		"ntfy":      {"POST /ntfy/topic", "|high|Bearer tk_ntfy|", "Test notification"},
		"telegram":  {"POST /telegram/bot123456789:", "/sendMessage", `"chat_id":"-1001234567890"`},
		"pagerduty": {`"routing_key":"` + strings.Repeat("b", 32), `"event_action":"trigger"`, `"severity":"warning"`},
		"twilio":    {"/Accounts/" + sid + "/Messages.json", sid + ":tokentokentoken12", "To=%2B381601234567", "From=%2B15551234567"},
		"smsgw":     {"GET /smsgw/send?to=%2B381601234567&text=", "Test"},
	} {
		for _, w := range want {
			if !strings.Contains(got[k], w) {
				t.Errorf("%s: %q lacks %q", k, got[k], w)
			}
		}
	}
}
