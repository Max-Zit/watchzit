package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

//go:embed i18n/*.json
var i18nFS embed.FS

// Language is a console language.
type Language struct{ Code, Name string }

// Languages the console offers, by their own names; English is the source
// language.
var Languages = []Language{
	{"en", "English"}, {"de", "Deutsch"}, {"fr", "Français"}, {"es", "Español"}, {"it", "Italiano"},
	{"pt", "Português"}, {"nl", "Nederlands"}, {"pl", "Polski"}, {"cs", "Čeština"}, {"sr", "Srpski"},
	{"tr", "Türkçe"}, {"ru", "Русский"}, {"zh", "中文（简体）"}, {"ja", "日本語"},
}

// catalogs maps language → English text → translation.
var catalogs = map[string]map[string]string{}

func init() {
	for _, l := range Languages[1:] {
		raw, err := i18nFS.ReadFile("i18n/" + l.Code + ".json")
		if err != nil {
			panic(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(raw, &m); err != nil {
			panic(fmt.Sprintf("i18n/%s.json: %v", l.Code, err))
		}
		catalogs[l.Code] = m
	}
}

func validLang(code string) bool {
	for _, l := range Languages {
		if l.Code == code {
			return true
		}
	}
	return false
}

// translate returns the text in lang; with args it is a format string.
func translate(lang, key string, args ...any) string {
	if t, ok := catalogs[lang][key]; ok && t != "" {
		key = t
	}
	if len(args) > 0 {
		return fmt.Sprintf(key, args...)
	}
	return key
}

// Page is what every template gets.
type Page struct {
	Title    string
	Nav      string
	Lang     string
	User     *store.User
	Flash    string
	FlashErr bool // the flash message reports a failure
	Error    string
	Version  string
	DevNo2FA bool
	Asset    string
	Path     string
	Host     string // the console host as the browser sees it
	Refresh  int    // seconds until the page reloads itself (0 = never)
	// Device is the address of the device the page shows; a password
	// manager extension (PassZit) offers the logins saved for it.
	Device string
	// BackMap is the map this page was opened from (?map=ID): a button
	// leads back to it.
	BackMap *store.Map
	// Problems counts open problems that were sent (sidebar badge);
	// ProblemsCritical is set when one of them is critical.
	Problems         int
	ProblemsCritical bool
	Data             any
}

// T translates a text for the page language.
func (p *Page) T(key string, args ...any) string { return translate(p.Lang, key, args...) }

// Languages lists the console languages (for the language menu).
func (p *Page) Languages() []Language { return Languages }

// Time formats a time in the server's zone.
func (p *Page) Time(t any) string {
	switch v := t.(type) {
	case time.Time:
		if v.IsZero() {
			return "—"
		}
		return v.Local().Format("2006-01-02 15:04")
	case *time.Time:
		if v == nil || v.IsZero() {
			return "—"
		}
		return v.Local().Format("2006-01-02 15:04")
	}
	return ""
}

// Ago is a short relative time ("5 min ago").
func (p *Page) Ago(t any) string {
	var tt time.Time
	switch v := t.(type) {
	case time.Time:
		tt = v
	case *time.Time:
		if v == nil {
			return "—"
		}
		tt = *v
	}
	if tt.IsZero() {
		return "—"
	}
	d := time.Since(tt)
	switch {
	case d < time.Minute:
		return p.T("just now")
	case d < time.Hour:
		return p.T("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return p.T("%d h ago", int(d.Hours()))
	}
	return p.T("%d days ago", int(d.Hours()/24))
}

// Since is a duration up to now, for "down for 3 h 5 min".
func (p *Page) Since(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return p.T("%d s", int(d.Seconds()))
	case d < time.Hour:
		return p.T("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return p.T("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	}
	return p.T("%d d %d h", int(d.Hours())/24, int(d.Hours())%24)
}

// RoleName is the display name of a role.
func (p *Page) RoleName(r string) string {
	switch r {
	case store.RoleAdmin:
		return p.T("Administrator")
	case store.RoleOperator:
		return p.T("Operator")
	case store.RoleViewer:
		return p.T("Viewer")
	}
	return r
}

// StatusName is the display name of a device or check status.
func (p *Page) StatusName(st string) string {
	switch st {
	case store.StatusUp:
		return p.T("Up")
	case store.StatusDown:
		return p.T("Down")
	case store.StatusWarning:
		return p.T("Partly down")
	case store.StatusPaused:
		return p.T("Paused")
	}
	return p.T("Unknown")
}

// KindName is the display name of a device kind.
func (p *Page) KindName(k string) string {
	if n, ok := kindNames[k]; ok {
		return p.T(n)
	}
	return p.T("Other")
}

var kindNames = map[string]string{"router": "Router", "switch": "Switch", "firewall": "Firewall", "server": "Server", "pc": "Computer",
	"printer": "Printer", "ups": "UPS", "accesspoint": "Access point", "camera": "Camera", "other": "Other"}

// Kinds lists the device kinds.
func (p *Page) Kinds() []string { return store.DeviceKinds }

// Zone names the server's time zone, e.g. "CEST, UTC+02:00"; times in the
// console are shown in it.
func (p *Page) Zone() string { return time.Now().Format("MST, UTC-07:00") }

// ClockInfo feeds the sidebar clock.
type ClockInfo struct {
	Text string
	MS   int64
	Off  int
}

// Clock is the server's time; app.js keeps it ticking from this value, so
// a wrong clock of the viewer's PC does not matter.
func (p *Page) Clock() ClockInfo {
	now := time.Now()
	_, off := now.Zone()
	return ClockInfo{Text: now.Format("2006-01-02 15:04:05"), MS: now.UnixMilli(), Off: off}
}

var funcs = template.FuncMap{
	"initial": func(s string) string {
		for _, r := range s {
			return strings.ToUpper(string(r))
		}
		return "?"
	},
	"join":        strings.Join,
	"ranges":      vlanRanges,
	"vlanmode":    func(m string) string { return vlanModeNames[m] },
	"pictureType": func(t string) string { return pictureTypeNames[t] },
	"fval": func(p *float64) string {
		if p == nil {
			return ""
		}
		return strconv.FormatFloat(*p, 'g', -1, 64)
	},
	"deref64": func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	},
}

// loadTemplates parses each page together with the layout.
func loadTemplates() (map[string]*template.Template, error) {
	base, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/_*.html")
	if err != nil {
		return nil, err
	}
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	out := map[string]*template.Template{}
	for _, p := range pages {
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/"), ".html")
		if strings.HasPrefix(name, "_") {
			continue
		}
		t, err := template.Must(base.Clone()).ParseFS(templateFS, p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out[name] = t
	}
	return out, nil
}

// render writes a page; the layout is chosen by the page itself
// ({{template "app" .}} or {{template "guest" .}}).
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, p *Page) {
	t, ok := s.tmpl[name]
	if !ok {
		s.serverError(w, r, fmt.Errorf("no template %q", name))
		return
	}
	if p.User == nil {
		p.User = userFrom(r)
	}
	if p.Lang == "" {
		p.Lang = s.lang(r, p.User) // the user's own language first
	}
	p.Version = s.Version
	p.DevNo2FA = s.DevNo2FA
	p.Asset = assetVersion
	p.Path = r.URL.RequestURI()
	p.Host = r.Host
	if id, err := strconv.ParseInt(r.URL.Query().Get("map"), 10, 64); err == nil && id > 0 && p.User != nil && p.Nav != "maps" {
		if m, err := s.store.MapByID(r.Context(), id); err == nil {
			p.BackMap = m
		}
	}
	if p.User != nil {
		if c, err := s.store.ProblemCounts(r.Context()); err == nil {
			for _, n := range c {
				p.Problems += n
			}
			p.ProblemsCritical = c[store.SeverityCritical] > 0
		}
	}
	if f := s.takeFlash(w, r); f != "" && p.Flash == "" {
		// An error message (redirectErr) starts with "!".
		p.Flash, p.FlashErr = strings.TrimPrefix(f, "!"), strings.HasPrefix(f, "!")
	}
	var b strings.Builder
	if err := t.ExecuteTemplate(&b, name+".html", p); err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(b.String()))
}

// lang picks the console language: the user's choice, the lang cookie,
// then the browser's preference.
func (s *Server) lang(r *http.Request, u *store.User) string {
	if u != nil && validLang(u.Lang) {
		return u.Lang
	}
	if c, err := r.Cookie("lang"); err == nil && validLang(c.Value) {
		return c.Value
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		code := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		code = strings.SplitN(code, "-", 2)[0]
		if code == "sr" || code == "hr" || code == "bs" || code == "sh" {
			return "sr"
		}
		if validLang(code) {
			return code
		}
	}
	return "en"
}

// ---- flash messages: a short-lived cookie carries the message (in
// English, translated when shown) across the redirect after a POST.

const flashCookie = "wzflash"

func (s *Server) flash(w http.ResponseWriter, r *http.Request, msg string) {
	// #nosec G124 -- Secure whenever the request came over TLS; plain HTTP is only the loopback dev listener
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: url.QueryEscape(msg), Path: "/", MaxAge: 60,
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
}

func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return ""
	}
	// #nosec G124 -- Secure whenever the request came over TLS; plain HTTP is only the loopback dev listener
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	msg, _ := url.QueryUnescape(c.Value)
	if len(msg) > 300 {
		return ""
	}
	if strings.HasPrefix(msg, "!") {
		return msg // an error, translated when it was set
	}
	return translate(s.lang(r, userFrom(r)), msg)
}

// redirect answers a POST with 303 See Other and an optional message.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to, msg string) {
	if msg != "" {
		s.flash(w, r, msg)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// redirectErr answers a POST with an error message shown on the next page.
func (s *Server) redirectErr(w http.ResponseWriter, r *http.Request, to, msg string) {
	s.flash(w, r, "!"+translate(s.lang(r, userFrom(r)), msg))
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// assetVersion changes with the static files, so browsers do not keep an
// old app.css or app.js from their cache after an update.
var assetVersion = func() string {
	h := sha256.New()
	for _, f := range []string{"static/app.css", "static/app.js", "static/chart.js", "static/map.js", "static/graphedit.js", "static/pictures.js"} {
		b, _ := staticFS.ReadFile(f)
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

// statusBadge is the argument of the status template.
type statusBadge struct{ Class, Label string }

func init() {
	funcs["statusOf"] = func(p *Page, st string) statusBadge {
		c := map[string]string{store.StatusUp: "ok", store.StatusDown: "bad", store.StatusWarning: "warn"}[st]
		return statusBadge{Class: c, Label: p.StatusName(st)}
	}
	funcs["statuses"] = func() []string {
		return []string{store.StatusDown, store.StatusWarning, store.StatusUnknown, store.StatusUp, store.StatusPaused}
	}
	funcs["checkKind"] = func(t string) string {
		if n, ok := checkKinds[t]; ok {
			return n
		}
		return t
	}
	funcs["portTraffic"] = func(s string) string {
		if _, after, ok := strings.Cut(s, " · "); ok {
			return after
		}
		return ""
	}
	funcs["isAgent"] = store.IsAgent
	funcs["dsKindName"] = func(k string) string { return dsKindNames[k] }
	funcs["withTitle"] = func(title string, g graphCtx) graphCtx { g.Title = title; return g }
	funcs["widgetName"] = func(k string) string { return widgetNames[k] }
	funcs["graphOf"] = func(p *Page, g graphDef, rng string) graphCtx { return graphCtx{P: p, G: g, Range: rng} }
	funcs["periodName"] = func(p string) string { return periodNames[p] }
	funcs["dnsTypes"] = func() []string { return store.DNSTypes }
	funcs["toolIcon"] = func(builtin string) string {
		switch builtin {
		case "ssh", "telnet":
			return "terminal"
		case "rdp", "vnc":
			return "screen"
		case "winbox":
			return "network"
		}
		return "link"
	}
	funcs["severityName"] = severityName
	funcs["list"] = func(xs ...string) []string { return xs }
	funcs["minutes"] = minutesText
	funcs["sevOf"] = func(p *Page, sev string) sevCtx { return sevCtx{P: p, Sev: sev} }
	funcs["severities"] = func() []string { return store.Severities }
	funcs["dur"] = func(p *Page, d time.Duration) string { return durationText(p.Lang, d) }
	funcs["triggerKind"] = func(k string) string { return triggerKinds[k] }
	funcs["channelKind"] = func(k string) string { return channelKinds[k] }
	funcs["inStrings"] = func(xs []string, x string) bool { return slices.Contains(xs, x) }
	funcs["inList"] = func(xs []int64, x int64) bool { return slices.Contains(xs, x) }
	funcs["groupName"] = func(g string) string {
		if n, ok := groupNames[g]; ok {
			return n
		}
		return g
	}
	funcs["speed"] = func(bps int64) string {
		switch {
		case bps <= 0:
			return ""
		case bps >= 1e9 && bps%1e9 == 0:
			return fmt.Sprintf("%d Gbit/s", bps/1e9)
		case bps >= 1e6:
			return fmt.Sprintf("%d Mbit/s", bps/1e6)
		}
		return fmt.Sprintf("%d kbit/s", bps/1e3)
	}
	funcs["metricName"] = func(m string) string {
		if n, ok := metricNames[m]; ok {
			return n
		}
		return m
	}
	funcs["bytes"] = func(v any) string {
		var n int64
		switch x := v.(type) {
		case int64:
			n = x
		case uint64:
			n = int64(min(x, 1<<62)) // #nosec G115 -- capped
		case int:
			n = int64(x)
		}
		const unit = 1024
		if n < unit {
			return fmt.Sprintf("%d B", n)
		}
		div, exp := int64(unit), 0
		for x := n / unit; x >= unit; x /= unit {
			div *= unit
			exp++
		}
		return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
	}
}

// metricNames are the display names of metrics (translated in templates).
var metricNames = map[string]string{"rtt_ms": "Response time", "loss_pct": "Packet loss", "connect_ms": "Connect time",
	"response_ms": "Response time", "uptime_s": "Uptime", "in_bps": "Traffic in", "out_bps": "Traffic out", "in_err": "Errors in", "out_err": "Errors out",
	"cpu_pct": "CPU load", "used_pct": "Used", "used_b": "Used space", "value": "Value",
	"mem_pct": "Memory used", "swap_pct": "Swap used", "mem_used_b": "Memory used (bytes)", "load1": "Load average", "running": "Running",
	"status_code": "Status code", "cert_days": "Certificate valid (days)", "handshake_ms": "Handshake time", "connections": "Connections", "temperature": "Temperature"}

// checkKinds name the check types.
var checkKinds = map[string]string{"icmp": "Ping (ICMP)", "tcp": "TCP port", "snmp": "SNMP availability", "snmp_if": "Interface traffic",
	"snmp_cpu": "CPU load", "snmp_storage": "Memory or disk", "snmp_oid": "SNMP value (OID)",
	"agent": "Agent", "agent_cpu": "CPU load", "agent_mem": "Memory", "agent_disk": "Disk", "agent_net": "Interface traffic", "agent_service": "Service",
	"http": "Web page (HTTP)", "tls": "TLS certificate", "dns": "DNS", "pgsql": "PostgreSQL", "mysql": "MySQL / MariaDB", "routeros": "MikroTik RouterOS (API)", "jsonapi": "JSON API value"}

// groupNames name the kinds of items discovery finds.
var groupNames = map[string]string{"system": "System", "interface": "Interface", "memory": "Memory", "disk": "Disk"}

// rowCtx is a value of a shared template together with its page.
type rowCtx struct {
	P   *Page
	Row string
}

func init() {
	funcs["rowOf"] = func(p *Page, v string) rowCtx { return rowCtx{P: p, Row: v} }
}

// triggerKinds and channelKinds name the kinds (translated in templates).
var triggerKinds = map[string]string{store.TriggerDeviceDown: "Device down", store.TriggerCheckDown: "Check down", store.TriggerMetric: "Value out of range", store.TriggerHADegraded: "HA group without redundancy"}
var channelKinds = map[string]string{store.ChannelEmail: "Email", store.ChannelWebhook: "Webhook (JSON)", store.ChannelSlack: "Slack", store.ChannelTeams: "Microsoft Teams"}

// sevCtx is the argument of the sev template.
type sevCtx struct {
	P   *Page
	Sev string
}

// periodNames name the report periods.
var periodNames = map[string]string{"7d": "Last 7 days", "30d": "Last 30 days", "90d": "Last 90 days", "month": "This month", "lastmonth": "Last month"}

// graphCtx is the argument of the graphfig template; Title replaces the
// check name (dashboards).
type graphCtx struct {
	P     *Page
	G     graphDef
	Range string
	Title string
}

// widgetNames name the dashboard widget kinds.
var widgetNames = map[string]string{store.WidgetGraph: "Graph", store.WidgetValue: "Current value", store.WidgetStatus: "Device states",
	store.WidgetProblems: "Open problems", store.WidgetMap: "Map", store.WidgetQuery: "Graph from a data source"}

// vlanModeNames name the VLAN modes of ports.
var vlanModeNames = map[string]string{"access": "Access", "trunk": "Trunk", "hybrid": "Hybrid"}

// vlanRanges writes sorted VLAN numbers compactly: "1-4, 10, 20-22".
func vlanRanges(ids []int) string {
	var b strings.Builder
	for i := 0; i < len(ids); {
		j := i
		for j+1 < len(ids) && ids[j+1] == ids[j]+1 {
			j++
		}
		if b.Len() > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Itoa(ids[i]))
		if j > i {
			b.WriteString("-" + strconv.Itoa(ids[j]))
		}
		i = j + 1
	}
	return b.String()
}
