package web_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/auth"
	"github.com/max-zit/watchzit/internal/poller"
	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/snmpmon"
	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/testdb"
	"github.com/max-zit/watchzit/internal/tsdb"
	"github.com/max-zit/watchzit/internal/web"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

type env struct {
	t     *testing.T
	st    *store.Store
	db    *tsdb.DB
	srv   *web.Server
	h     http.Handler
	now   time.Time
	setup string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.UseSecretKey(bytes.Repeat([]byte{3}, 32)); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db := tsdb.New(st.Pool(), log)
	srv, err := web.New(ctx, st, db, poller.New(st, db, log), log)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, db: db, srv: srv, now: time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC), setup: "setup-secret"}
	srv.SetClock(func() time.Time { return e.now })
	st.SetClock(func() time.Time { return e.now })
	db.SetClock(func() time.Time { return e.now })
	srv.SetSetupToken(e.setup)
	e.h = srv.Handler()
	return e
}

// client is a browser: cookies, a source address, same-origin posts.
type client struct {
	e       *env
	ip      string
	cookies map[string]string
}

func (e *env) client(ip string) *client { return &client{e: e, ip: ip, cookies: map[string]string{}} }

type resp struct {
	code int
	loc  string
	body string
	hdr  http.Header
}

func (c *client) do(method, path string, form url.Values, hdr map[string]string) resp {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, "http://wz.test"+path, body)
	r.RemoteAddr = c.ip + ":50000"
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if method == http.MethodPost {
		r.Header.Set("Origin", "http://wz.test")
	}
	for k, v := range hdr {
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	for k, v := range c.cookies {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	c.e.h.ServeHTTP(w, r)
	for _, ck := range w.Result().Cookies() {
		if ck.MaxAge < 0 {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck.Value
		}
	}
	return resp{code: w.Code, loc: w.Header().Get("Location"), body: w.Body.String(), hdr: w.Header()}
}

func (c *client) get(path string) resp { return c.do(http.MethodGet, path, nil, nil) }
func (c *client) post(path string, kv ...string) resp {
	f := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		f.Add(kv[i], kv[i+1])
	}
	return c.do(http.MethodPost, path, f, nil)
}

var secretRE = regexp.MustCompile(`<code class="secret"[^>]*>([A-Z2-7]+)</code>`)

func (e *env) code(secret string) string {
	c, _ := auth.TOTPCode(secret, e.now.Unix()/30)
	return c
}

// enroll finishes the second-factor setup of a signed-in-by-password client.
func (e *env) enroll(c *client) string {
	e.t.Helper()
	r := c.get("/login/enroll")
	m := secretRE.FindStringSubmatch(r.body)
	if m == nil {
		e.t.Fatalf("no secret on the enrollment page: %d %s", r.code, r.body)
	}
	r = c.post("/login/enroll", "code", e.code(m[1]))
	if r.code != http.StatusOK || !strings.Contains(r.body, `class="codes"`) {
		e.t.Fatalf("enroll: %d %s", r.code, r.body)
	}
	e.now = e.now.Add(31 * time.Second)
	return m[1]
}

// setupAdmin runs the first-run setup and the 2FA enrollment.
func (e *env) setupAdmin(ip string) (*client, string) {
	e.t.Helper()
	c := e.client(ip)
	r := c.post("/setup", "token", e.setup, "username", "admin", "password", "admin password 123", "password2", "admin password 123")
	if r.code != http.StatusSeeOther || r.loc != "/login/enroll" {
		e.t.Fatalf("setup: %d %s %s", r.code, r.loc, r.body)
	}
	return c, e.enroll(c)
}

func TestSetupRequiresTokenAnd2FA(t *testing.T) {
	e := newEnv(t)
	c := e.client("192.168.1.10")
	if r := c.get("/"); r.loc != "/setup" {
		t.Fatalf("fresh server should send to setup, got %d %s", r.code, r.loc)
	}
	r := c.post("/setup", "token", "wrong", "username", "admin", "password", "admin password 123", "password2", "admin password 123")
	if r.code != http.StatusBadRequest {
		t.Fatalf("wrong setup token accepted: %d", r.code)
	}
	c2 := e.client("192.168.1.11")
	r = c2.post("/setup", "token", e.setup, "username", "admin", "password", "admin password 123", "password2", "admin password 123")
	if r.loc != "/login/enroll" {
		t.Fatalf("setup: %d %s", r.code, r.loc)
	}
	if r := c2.get("/devices"); r.code != http.StatusSeeOther || !strings.HasPrefix(r.loc, "/login") {
		t.Fatalf("console open before 2FA: %d %s", r.code, r.loc)
	}
	if r := c.post("/setup", "token", e.setup, "username", "evil", "password", "evil password 123", "password2", "evil password 123"); r.loc != "/login" {
		t.Fatalf("second setup: %d %s", r.code, r.loc)
	}
}

func TestLoginWith2FA(t *testing.T) {
	e := newEnv(t)
	_, secret := e.setupAdmin("192.168.1.10")
	c := e.client("192.168.1.20")
	r := c.post("/login", "username", "admin", "password", "admin password 123")
	if r.loc != "/login/2fa?next=%2F" {
		t.Fatalf("login: %d %s", r.code, r.loc)
	}
	if r := c.get("/"); r.code != http.StatusSeeOther {
		t.Fatal("password alone opened the console")
	}
	pre := c.cookies["wzsession"]
	if r := c.post("/login/2fa", "code", "000000"); r.code != http.StatusUnauthorized {
		t.Fatalf("wrong code: %d", r.code)
	}
	code := e.code(secret)
	if r := c.post("/login/2fa", "code", code, "next", "/devices"); r.loc != "/devices" {
		t.Fatalf("2fa: %d %s %s", r.code, r.loc, r.body)
	}
	if c.cookies["wzsession"] == pre {
		t.Fatal("session token not rotated after the second factor")
	}
	if r := c.get("/devices"); r.code != http.StatusOK {
		t.Fatalf("console after sign-in: %d", r.code)
	}
	c3 := e.client("192.168.1.21")
	c3.post("/login", "username", "admin", "password", "admin password 123")
	if r := c3.post("/login/2fa", "code", code); r.code != http.StatusUnauthorized {
		t.Fatalf("replayed TOTP code accepted: %d", r.code)
	}
	c.post("/logout")
	if r := c.get("/devices"); r.code != http.StatusSeeOther {
		t.Fatalf("still signed in after sign-out: %d", r.code)
	}
}

func TestBruteForceBlock(t *testing.T) {
	e := newEnv(t)
	e.setupAdmin("192.168.1.10")
	c := e.client("10.9.9.9")
	for i := 0; i < 5; i++ {
		c.post("/login", "username", "admin", "password", "wrong password 1")
	}
	if r := c.post("/login", "username", "admin", "password", "admin password 123"); r.code != http.StatusTooManyRequests {
		t.Fatalf("blocked address could sign in: %d", r.code)
	}
	if r := e.client("10.9.9.10").post("/login", "username", "admin", "password", "admin password 123"); r.code != http.StatusSeeOther {
		t.Fatalf("other address blocked too: %d", r.code)
	}
}

func TestSecurityHeadersAndCSRF(t *testing.T) {
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	r := c.get("/")
	if csp := r.hdr.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP: %q", csp)
	}
	if strings.Contains(r.body, "<script>") || strings.Contains(r.body, " style=") {
		t.Fatal("inline script or style in a page")
	}
	f := url.Values{"name": {"x"}, "address": {"10.0.0.1"}}
	if r := c.do(http.MethodPost, "/devices/new", f, map[string]string{"Origin": "https://evil.example"}); r.code != http.StatusForbidden {
		t.Fatalf("cross-site post: %d", r.code)
	}
	if r := c.do(http.MethodPost, "/devices/new", f, map[string]string{"Origin": "", "Referer": ""}); r.code != http.StatusForbidden {
		t.Fatalf("post without origin: %d", r.code)
	}
	// Outside the allowed networks the console refuses.
	if r := e.client("203.0.113.5").get("/login"); r.code != http.StatusForbidden {
		t.Fatalf("public address: %d", r.code)
	}
}

func TestDevicesChecksAndSeries(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	if r := c.post("/groups", "name", "Head office"); r.code != http.StatusSeeOther {
		t.Fatalf("group: %d %s", r.code, r.body)
	}
	gs, _ := e.st.Groups(ctx)
	if len(gs) != 1 {
		t.Fatalf("%d groups", len(gs))
	}
	// Invalid input is refused and shown.
	r := c.post("/devices/new", "name", "bad", "address", "-oProxyCommand=x", "ping", "1")
	if r.code != http.StatusBadRequest || !strings.Contains(r.body, "IP address or a host name") {
		t.Fatalf("bad address: %d", r.code)
	}
	r = c.post("/devices/new", "name", "core", "address", "192.168.1.1", "kind", "router", "group", "1", "ping", "1", "ports", "22, 443", "web_url", "https://192.168.1.1")
	if r.code != http.StatusSeeOther || !strings.HasPrefix(r.loc, "/devices/") {
		t.Fatalf("create: %d %s %s", r.code, r.loc, r.body)
	}
	devURL := r.loc
	r = c.get(devURL)
	for _, want := range []string{"core", "Ping", "TCP 22", "TCP 443", `href="https://192.168.1.1"`, `data-series="`} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("device page lacks %q", want)
		}
	}
	if r := c.post("/devices/new", "name", "CORE", "address", "192.168.1.2"); r.code != http.StatusBadRequest {
		t.Fatalf("duplicate name: %d", r.code)
	}
	cs, err := e.st.AllChecks(ctx)
	if err != nil || len(cs) != 3 {
		t.Fatalf("%v %d checks", err, len(cs))
	}
	// Results: one TCP port down makes the device partly down.
	var res []store.CheckResult
	for _, ch := range cs {
		st := store.StatusUp
		if ch.Params.Port == 443 {
			st = store.StatusDown
		}
		res = append(res, store.CheckResult{CheckID: ch.ID, At: e.now, Status: st, Text: "test"})
	}
	changes, err := e.st.SaveResults(ctx, res)
	if err != nil || len(changes) != 3 {
		t.Fatalf("%v %d changes", err, len(changes))
	}
	d, _ := e.st.DeviceByID(ctx, cs[0].DeviceID)
	if d.Status != store.StatusWarning || d.ChecksDown != 1 {
		t.Fatalf("device %s, %d down", d.Status, d.ChecksDown)
	}
	if r := c.get("/"); !strings.Contains(r.body, "<b>1</b><span>Partly down</span>") {
		t.Fatal("overview does not list the problem")
	}
	// Samples of the ping series come back as JSON.
	var rtt int32
	for _, ch := range cs {
		if ch.Type == store.CheckICMP {
			rtt = ch.Series[0].ID
		}
	}
	for i := 0; i < 30; i++ {
		e.db.Add(tsdb.Sample{Series: rtt, T: e.now.Add(time.Duration(-i) * time.Minute), V: 1.25})
	}
	if err := e.db.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	r = c.get("/series/" + itoa(rtt) + "?range=1h&points=600")
	var js struct {
		Step int64      `json:"step"`
		T    []int64    `json:"t"`
		Avg  []*float64 `json:"avg"`
		Unit string     `json:"unit"`
	}
	if err := json.Unmarshal([]byte(r.body), &js); err != nil || r.code != 200 {
		t.Fatalf("series: %d %v %s", r.code, err, r.body)
	}
	if len(js.T) != 30 || js.Unit != "ms" || *js.Avg[0] != 1.25 {
		t.Fatalf("series: %d points, unit %q", len(js.T), js.Unit)
	}
	if r := c.get("/series/99999"); r.code != http.StatusNotFound {
		t.Fatalf("unknown series: %d", r.code)
	}
	// A viewer sees but cannot change.
	if r := c.post("/users", "username", "watcher", "password", "watcher password 1", "role", "viewer"); r.code != http.StatusSeeOther {
		t.Fatalf("user: %d %s", r.code, r.body)
	}
	v := e.client("192.168.1.30")
	v.post("/login", "username", "watcher", "password", "watcher password 1")
	e.enroll(v)
	v.post("/account/password", "current", "watcher password 1", "password", "new watcher pass 1", "password2", "new watcher pass 1")
	if r := v.get(devURL); r.code != http.StatusOK || strings.Contains(r.body, "Add a check") {
		t.Fatalf("viewer device page: %d", r.code)
	}
	if r := v.post(devURL + "/delete"); r.code != http.StatusForbidden {
		t.Fatalf("viewer deleted a device: %d", r.code)
	}
	if r := v.get("/users"); r.code != http.StatusForbidden {
		t.Fatalf("viewer opened users: %d", r.code)
	}
	// Deleting the device removes checks, series and samples.
	if r := c.post(devURL + "/delete"); r.code != http.StatusSeeOther {
		t.Fatalf("delete: %d", r.code)
	}
	var n int
	_ = e.st.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM checks) + (SELECT count(*) FROM series) + (SELECT count(*) FROM samples_hot)`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows left after delete", n)
	}
	// Everything was audited.
	list, _ := e.st.AuditEntries(ctx, store.AuditFilter{Limit: 100})
	acts := map[string]bool{}
	for _, a := range list {
		acts[a.Action] = true
	}
	for _, a := range []string{"setup", "group.create", "device.create", "device.delete", "user.create"} {
		if !acts[a] {
			t.Fatalf("no audit entry %s", a)
		}
	}
	if chk, _ := e.st.VerifyAudit(ctx); !chk.OK {
		t.Fatal("audit chain broken")
	}
}

// TestPagesRender opens every page of the console in every language.
func TestPagesRender(t *testing.T) {
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/groups", "name", "G")
	c.post("/devices/new", "name", "d1", "address", "10.0.0.1", "group", "1", "ping", "1", "ports", "80", "web_url", "http://10.0.0.1")
	agent, err := snmp.NewTestAgent("pub")
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	snmpmon.LoadTestDevice(agent)
	c.post("/settings/snmp", "name", "P", "version", "2", "port", strconv.Itoa(int(agent.Addr().Port())), "community", "pub")
	c.post("/devices/new", "name", "d2", "address", "127.0.0.1", "snmp", "1")
	c.post("/devices/2/discover", "add", "snmp_if:1:ZXRoZXIx", "add", "snmp:0:")
	c.post("/maps", "name", "M")
	c.post("/agents/tokens", "name", "T", "days", "7", "max_uses", "5")
	c.post("/dashboards", "name", "D")
	c.post("/dashboards/1/widgets", "kind", "problems")
	c.post("/dashboards/1/widgets", "kind", "status")
	c.post("/account/tokens", "name", "T", "days", "0")
	c.post("/settings/notifications/channels", "name", "Mail", "kind", "email", "target", "a@example.com", "min_severity", "warning")
	c.post("/alerts/maintenance", "name", "MW", "from", "2026-10-27T09:00", "to", "2026-10-27T23:00", "device", "1")
	pages := []string{"/", "/devices", "/devices?status=down", "/devices/new", "/devices/1", "/devices/1?range=7d", "/devices/1/edit", "/groups",
		"/users", "/users/1", "/account", "/audit", "/settings/network", "/settings/security", "/settings/data", "/settings/snmp", "/settings/snmp/1", "/devices/2", "/devices/2/discover", "/maps", "/maps/1", "/maps/1?edit=1", "/settings/kiosks", "/agents", "/problems", "/problems?view=history", "/alerts/triggers", "/alerts/triggers/1", "/alerts/maintenance", "/settings/notifications", "/launcher", "/settings/tools", "/settings/tools/1", "/dashboards", "/dashboards/1", "/dashboards/1?edit=1", "/reports", "/reports?period=lastmonth", "/nothing"}
	for _, l := range web.Languages {
		c.post("/lang", "lang", l.Code)
		for _, p := range pages {
			r := c.get(p)
			want := http.StatusOK
			if p == "/nothing" {
				want = http.StatusNotFound
			}
			if r.code != want {
				t.Fatalf("%s %s: %d %s", l.Code, p, r.code, r.body)
			}
		}
	}
	for _, p := range []string{"/login", "/setup"} {
		if r := e.client("192.168.1.99").get(p); r.code != http.StatusOK && r.code != http.StatusSeeOther {
			t.Fatalf("%s: %d", p, r.code)
		}
	}
}

func itoa(n int32) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestSNMPProfilesAndDiscovery: secrets are write-only, discovery lists
// what a device offers and adds the chosen checks once.
func TestSNMPProfilesAndDiscovery(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	agent, err := snmp.NewTestAgent("community-s3cret")
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	snmpmon.LoadTestDevice(agent)
	port := strconv.Itoa(int(agent.Addr().Port()))

	if r := c.post("/settings/snmp", "name", "v3 bad", "version", "3", "user", "u", "auth_proto", "SHA", "auth_pass", "short"); r.code != http.StatusBadRequest || !strings.Contains(r.body, "at least 8 characters") {
		t.Fatalf("short v3 password: %d", r.code)
	}
	if r := c.post("/settings/snmp", "name", "Lab", "version", "2", "port", port, "community", "community-s3cret"); r.code != http.StatusSeeOther {
		t.Fatalf("profile: %d %s", r.code, r.body)
	}
	r := c.get("/settings/snmp/1")
	if r.code != http.StatusOK || strings.Contains(r.body, "community-s3cret") {
		t.Fatalf("profile page shows the community (%d)", r.code)
	}
	// saving without a new community keeps the stored one
	if r := c.post("/settings/snmp/1", "name", "Lab", "version", "2", "port", port, "community", ""); r.code != http.StatusSeeOther {
		t.Fatalf("update: %d %s", r.code, r.body)
	}
	p, _ := e.st.SNMPProfileByID(ctx, 1)
	if cfg, err := e.st.SNMPConfig(p); err != nil || cfg.Community != "community-s3cret" {
		t.Fatalf("community lost: %v", err)
	}
	var raw string
	_ = e.st.Pool().QueryRow(ctx, `SELECT community FROM snmp_profiles WHERE id=1`).Scan(&raw)
	if strings.Contains(raw, "s3cret") {
		t.Fatal("community stored in plain text")
	}

	r = c.post("/devices/new", "name", "router", "address", "127.0.0.1", "kind", "router", "snmp", "1")
	if r.code != http.StatusSeeOther || !strings.HasSuffix(r.loc, "/discover") {
		t.Fatalf("create: %d %s", r.code, r.loc)
	}
	r = c.get(r.loc)
	for _, want := range []string{"test-router", "MikroTik", "ether1", "ether2", "main memory", "system disk"} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("discovery lacks %q: %s", want, r.body)
		}
	}
	keys := regexp.MustCompile(`name="add" value="([^"]+)"[^>]*checked`).FindAllStringSubmatch(r.body, -1)
	if len(keys) != 5 { // SNMP, CPU, memory, disk, ether1 (ether2 has no link)
		t.Fatalf("%d proposals selected", len(keys))
	}
	form := []string{"interval", "30"}
	for _, k := range keys {
		form = append(form, "add", k[1])
	}
	if r := c.post("/devices/1/discover", form...); r.code != http.StatusSeeOther {
		t.Fatalf("add: %d %s", r.code, r.body)
	}
	c.post("/devices/1/discover", form...) // twice: no duplicates
	cs, _ := e.st.DeviceChecks(ctx, 1)
	if len(cs) != 5 || cs[0].IntervalS != 30 {
		t.Fatalf("%d checks", len(cs))
	}
	r = c.get("/devices/1")
	for _, want := range []string{"Ports", "ether1", "uplink", "1 Gbit/s", `data-labels="Traffic in|Traffic out"`} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("device page lacks %q", want)
		}
	}
	// the settings dialog of an interface check keeps the interface
	var ifc *store.Check
	for _, x := range cs {
		if x.Type == store.CheckSNMPIf {
			ifc = x
		}
	}
	if r := c.post("/checks/"+strconv.FormatInt(ifc.ID, 10), "name", "uplink", "interval", "60", "timeout_ms", "3000"); r.code != http.StatusSeeOther {
		t.Fatalf("check update: %d %s", r.code, r.body)
	}
	if x, _ := e.st.CheckByID(ctx, ifc.ID); x.Params.IfIndex != 1 || x.Params.TimeoutMS != 3000 {
		t.Fatalf("%+v", x.Params)
	}
	// custom OID check
	if r := c.post("/devices/1/checks", "type", "snmp_oid", "oid", "1.3.6.1.4.1.14988.1.1.3.10.0", "unit", "°C", "scale", "0,1"); r.code != http.StatusSeeOther {
		t.Fatalf("oid check: %d %s", r.code, r.body)
	}
	if r := c.post("/devices/1/checks", "type", "snmp_oid", "oid", "1.3.x"); r.code != http.StatusBadRequest {
		t.Fatalf("bad oid: %d", r.code)
	}
	// deleting a profile takes SNMP off its devices
	if r := c.post("/settings/snmp/1/delete"); r.code != http.StatusSeeOther {
		t.Fatalf("delete: %d", r.code)
	}
	if d, _ := e.st.DeviceByID(ctx, 1); d.SNMPProfile != 0 {
		t.Fatal("device keeps a deleted profile")
	}
}

// postJSON sends a JSON body like map.js does.
func (c *client) postJSON(path string, v any) resp {
	b, _ := json.Marshal(v)
	r := httptest.NewRequest(http.MethodPost, "http://wz.test"+path, bytes.NewReader(b))
	r.RemoteAddr = c.ip + ":50000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://wz.test")
	for k, v := range c.cookies {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	c.e.h.ServeHTTP(w, r)
	return resp{code: w.Code, body: w.Body.String(), hdr: w.Header()}
}

// TestMaps: create a map, place devices, link them port to port, read the
// live state (status, traffic) and check that viewers cannot edit.
func TestMaps(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "r1", "address", "10.0.0.1")
	c.post("/devices/new", "name", "sw1", "address", "10.0.0.2")
	if err := e.st.SetInterfaces(ctx, 1, []store.Interface{{Index: 2, Name: "ether2", OperUp: true, AdminUp: true, Speed: 1e9}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetInterfaces(ctx, 2, []store.Interface{{Index: 7, Name: "Gi0/7", OperUp: false, AdminUp: true, Speed: 1e9}}); err != nil {
		t.Fatal(err)
	}
	ch, err := e.st.CreateCheck(ctx, 1, store.CheckInput{Type: store.CheckSNMPIf, Params: store.CheckParams{IfIndex: 2, IfName: "ether2"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SaveResults(ctx, []store.CheckResult{{CheckID: ch, At: e.now, Status: store.StatusUp, Text: "up", Values: map[string]float64{"in_bps": 1500, "out_bps": 2e6}}}); err != nil {
		t.Fatal(err)
	}

	r := c.post("/maps", "name", "Head office")
	if r.code != http.StatusSeeOther || r.loc != "/maps/1?edit=1" {
		t.Fatalf("create: %d %s", r.code, r.loc)
	}
	if r := c.get("/maps/1?edit=1"); r.code != http.StatusOK || !strings.Contains(r.body, `src="/static/map.js`) {
		t.Fatalf("map page: %d", r.code)
	}
	layout := store.MapLayout{
		Nodes: []store.MapNode{{ID: -1, Kind: "device", DeviceID: 1, X: 0, Y: 0}, {ID: -2, Kind: "device", DeviceID: 2, X: 200, Y: 0}, {ID: -3, Kind: "label", Label: "Rack A", X: 100, Y: -80}},
		Links: []store.MapLink{{ID: -1, A: -1, B: -2, AIf: 2, BIf: 7, Kind: "wired"}},
	}
	r = c.postJSON("/maps/1/layout", layout)
	if r.code != http.StatusOK {
		t.Fatalf("save: %d %s", r.code, r.body)
	}
	var saved store.MapLayout
	if err := json.Unmarshal([]byte(r.body), &saved); err != nil || len(saved.Nodes) != 3 || saved.Nodes[0].ID <= 0 || saved.Links[0].A != saved.Nodes[0].ID {
		t.Fatalf("%v %+v", err, saved)
	}
	// move a node: ids stay, nothing new in the audit log
	saved.Nodes[1].X = 300
	if r := c.postJSON("/maps/1/layout", saved); r.code != http.StatusOK || !strings.Contains(r.body, `"x":300`) || !strings.Contains(r.body, `"id":`+strconv.FormatInt(saved.Nodes[1].ID, 10)) {
		t.Fatalf("move: %d %s", r.code, r.body)
	}
	var audits int
	_ = e.st.Pool().QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='map.save'`).Scan(&audits)
	if audits != 1 {
		t.Fatalf("%d map.save audit entries", audits)
	}
	// broken input is refused
	for _, bad := range []store.MapLayout{
		{Nodes: []store.MapNode{{ID: -1, Kind: "device", DeviceID: 99}}},
		{Nodes: []store.MapNode{{ID: -1, Kind: "device", DeviceID: 1}}, Links: []store.MapLink{{A: -1, B: -5}}},
		{Nodes: []store.MapNode{{ID: -1, Kind: "device", DeviceID: 1}, {ID: -2, Kind: "device", DeviceID: 1}}},
		{Nodes: []store.MapNode{{ID: -1, Kind: "device", DeviceID: 1, X: 1e9}}},
	} {
		if r := c.postJSON("/maps/1/layout", bad); r.code != http.StatusBadRequest {
			t.Fatalf("bad layout accepted: %d %s", r.code, r.body)
		}
	}
	if r := c.do(http.MethodPost, "/maps/1/layout", url.Values{"x": {"1"}}, nil); r.code != http.StatusUnsupportedMediaType {
		t.Fatalf("form post: %d", r.code)
	}

	r = c.get("/maps/1/live")
	var live store.MapLive
	if err := json.Unmarshal([]byte(r.body), &live); err != nil {
		t.Fatal(err)
	}
	p := live.Ports[1][2]
	if p == nil || p.In == nil || *p.Out != 2e6 || live.Ports[2][7].Up || live.Devices[2].Name != "sw1" {
		t.Fatalf("live: %s", r.body)
	}
	r = c.get("/maps/1/layout")
	if !strings.Contains(r.body, `"can_edit":true`) || !strings.Contains(r.body, `"Add device"`) {
		t.Fatalf("layout json: %s", r.body)
	}
	// submaps: the parent shows the worst state inside
	c.post("/maps", "name", "Branch")
	if r := c.postJSON("/maps/2/layout", store.MapLayout{Nodes: []store.MapNode{{ID: -1, Kind: "device", DeviceID: 2}, {ID: -2, Kind: "submap", SubmapID: 1}}}); r.code != http.StatusOK {
		t.Fatalf("submap: %d %s", r.code, r.body)
	}
	e.st.SaveResults(ctx, nil)
	if _, err := e.st.Pool().Exec(ctx, `UPDATE devices SET status='down' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	ms, _ := e.st.Maps(ctx)
	for _, m := range ms {
		if m.Status != store.StatusDown {
			t.Fatalf("map %s status %q", m.Name, m.Status)
		}
	}
	if r := c.get("/maps"); !strings.Contains(r.body, "Head office") || !strings.Contains(r.body, "Branch") {
		t.Fatal("map list")
	}

	// viewers look but do not edit
	c.post("/users", "username", "watcher", "password", "watcher password 1", "role", "viewer")
	v := e.client("192.168.1.30")
	v.post("/login", "username", "watcher", "password", "watcher password 1")
	e.enroll(v)
	v.post("/account/password", "current", "watcher password 1", "password", "new watcher pass 1", "password2", "new watcher pass 1")
	if r := v.get("/maps/1/layout"); r.code != http.StatusOK || !strings.Contains(r.body, `"can_edit":false`) || strings.Contains(r.body, `"devices":`) {
		t.Fatalf("viewer layout: %d %s", r.code, r.body)
	}
	if r := v.postJSON("/maps/1/layout", saved); r.code != http.StatusForbidden {
		t.Fatalf("viewer saved a map: %d", r.code)
	}
	// deleting a device removes it from the map
	c.post("/devices/2/delete")
	l, _ := e.st.MapLayout(ctx, 1)
	if len(l.Nodes) != 2 || len(l.Links) != 0 {
		t.Fatalf("%+v", l)
	}
	if r := c.post("/maps/1/delete"); r.code != http.StatusSeeOther {
		t.Fatalf("delete: %d", r.code)
	}
}
