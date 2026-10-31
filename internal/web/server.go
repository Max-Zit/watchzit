// Package web is the management console of the WatchZit server (HTML,
// later a JSON API). The agent endpoint gets its own listener in 0.4.
package web

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/alert"
	"github.com/max-zit/watchzit/internal/discovery"
	"github.com/max-zit/watchzit/internal/flow"
	"github.com/max-zit/watchzit/internal/logrecv"
	"github.com/max-zit/watchzit/internal/netacl"
	"github.com/max-zit/watchzit/internal/poller"
	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/tsdb"
)

// Server serves the console.
type Server struct {
	store  *store.Store
	tsdb   *tsdb.DB
	poller *poller.Poller
	log    *slog.Logger
	tmpl   map[string]*template.Template
	now    func() time.Time
	guard  *loginGuard

	// MgmtACL guards the console listener.
	MgmtACL *netacl.List
	// AgentACL guards the agent endpoint; AgentFingerprint is its pinned
	// certificate; RunningAgent its listen address.
	AgentACL         *netacl.List
	AgentFingerprint string
	RunningAgent     string
	hub              *hub
	// RunningMgmt is the listen address the server actually uses.
	RunningMgmt string
	// ListenOverride: the address comes from a command-line option.
	ListenOverride bool
	Version        string
	// SetupTokenFile holds the one-time token the first-run setup asks for.
	SetupTokenFile string
	// DownloadsDir holds the agent installers offered in the console.
	DownloadsDir string
	// DevNo2FA skips the second factor (development server only).
	DevNo2FA   bool
	setupToken string
	// DataDir is the server's data directory (the root helper's requests).
	DataDir string
	// HelperAvailable replaces the check for the root helper (tests).
	HelperAvailable func() bool
	upd             updateState
	// LauncherKey signs the links that open devices with local tools.
	LauncherKey ed25519.PrivateKey
	// Alerts is the alert engine (kicked after changes; may be nil).
	Alerts *alert.Engine
	// Logs receives syslog messages and traps (may be nil).
	Logs *logrecv.Receiver
	// Flows collects NetFlow, IPFIX and sFlow (may be nil).
	Flows          *flow.Collector
	wg             sync.WaitGroup      // notifications in flight
	queries        queryCache          // answers of data source queries
	sso            ssoCache            // identity providers of single sign-on
	consoleBackups consoleBackupRunner // one console backup at a time
	tlsc           *consoleTLS         // console certificates (nil on the development server)
	scans          scans               // network discovery in progress
	// Prober replaces the network prober of discovery (tests).
	Prober *discovery.Prober
}

// New creates the server; settings are loaded from the database.
func New(ctx context.Context, st *store.Store, db *tsdb.DB, p *poller.Poller, log *slog.Logger) (*Server, error) {
	t, err := loadTemplates()
	if err != nil {
		return nil, err
	}
	s := &Server{store: st, tsdb: db, poller: p, log: log, tmpl: t, now: time.Now, Version: "dev"}
	s.guard = newLoginGuard(st, func() time.Time { return s.now() })
	n, err := s.Network(ctx)
	if err != nil {
		return nil, err
	}
	mg, _ := netacl.Parse(n.MgmtAllowed)
	s.MgmtACL = netacl.NewManagement(mg)
	ag, _ := netacl.Parse(n.AgentAllowed)
	s.AgentACL = netacl.NewOptional(ag)
	s.hub = newHub()
	db.SetRetention(s.retention(ctx))
	return s, nil
}

// SetClock replaces the clock (tests).
func (s *Server) SetClock(now func() time.Time) { s.now = now }

// SetSetupToken sets the token the first-run setup requires.
func (s *Server) SetSetupToken(tok string) { s.setupToken = tok }

// ---- network settings

const settingNetwork = "network"

// NetworkSettings are the two listeners and who may reach them.
type NetworkSettings struct {
	// MgmtListen is the console address, e.g. ":8443" or "192.168.1.10:8443".
	MgmtListen string `json:"mgmt_listen"`
	// MgmtAllowed may reach the console (loopback always may).
	MgmtAllowed []string `json:"mgmt_allowed"`
	// AgentListen is the agent endpoint, e.g. ":8444" (a separate port, so a
	// firewall can open it for remote sites and keep the console closed).
	AgentListen string `json:"agent_listen"`
	// AgentAllowed may reach the agent endpoint; empty = everyone.
	AgentAllowed []string `json:"agent_allowed"`
	// PublicAgentURL is how agents reach the server (put into enrollment
	// codes), e.g. "https://monitor.example.com:8444". Empty: derived from
	// the console address.
	PublicAgentURL string `json:"public_agent_url,omitempty"`
}

// defaultNetwork is used until the settings are saved (a fresh copy each
// time: JSON decoding into it must not change the package defaults).
func defaultNetwork() NetworkSettings {
	return NetworkSettings{MgmtListen: ":8443", AgentListen: ":8444", MgmtAllowed: append([]string(nil), netacl.DefaultManagement...)}
}

// Validate checks the listen addresses and the networks.
func (n *NetworkSettings) Validate() error {
	if n.AgentListen == "" {
		n.AgentListen = ":8444" // settings saved before agents existed
	}
	mh, mp, err := splitListen(n.MgmtListen)
	if err != nil {
		return fmt.Errorf("console address: %w", err)
	}
	ah, ap, err := splitListen(n.AgentListen)
	if err != nil {
		return fmt.Errorf("agent address: %w", err)
	}
	if mp == ap && (mh == ah || mh == "" || ah == "") {
		return errors.New("the console and the agent endpoint must use different ports")
	}
	if _, err := netacl.Parse(n.MgmtAllowed); err != nil {
		return err
	}
	if _, err := netacl.Parse(n.AgentAllowed); err != nil {
		return err
	}
	if n.PublicAgentURL != "" {
		u, err := url.Parse(n.PublicAgentURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.User != nil {
			return errors.New("the public agent address must look like https://monitor.example.com:8444")
		}
	}
	return nil
}

func splitListen(a string) (host string, port int, err error) {
	h, p, err := net.SplitHostPort(strings.TrimSpace(a))
	if err != nil {
		return "", 0, errors.New("use host:port or :port, e.g. :8443")
	}
	port, err = strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errors.New("the port must be 1–65535")
	}
	if h != "" {
		if _, err := netip.ParseAddr(h); err != nil {
			return "", 0, errors.New("the host must be an IP address of this server (or empty for all)")
		}
	}
	return h, port, nil
}

// Network loads the network settings.
func (s *Server) Network(ctx context.Context) (NetworkSettings, error) {
	n := defaultNetwork()
	if err := s.store.GetSetting(ctx, settingNetwork, &n); err != nil {
		return n, err
	}
	if n.Validate() != nil {
		s.log.Warn("invalid network settings in the database; using defaults")
		n = defaultNetwork()
	}
	return n, nil
}

// SaveNetwork stores the settings and applies the allow-list at once.
func (s *Server) SaveNetwork(ctx context.Context, n NetworkSettings) error {
	if err := n.Validate(); err != nil {
		return err
	}
	if err := s.store.SetSetting(ctx, settingNetwork, n); err != nil {
		return err
	}
	mg, _ := netacl.Parse(n.MgmtAllowed)
	s.MgmtACL.Set(mg)
	ag, _ := netacl.Parse(n.AgentAllowed)
	s.AgentACL.Set(ag)
	return nil
}

// ResetMgmtAllowList restores the default console networks (CLI, for an
// administrator who locked themselves out).
func ResetMgmtAllowList(ctx context.Context, st *store.Store) error {
	n := defaultNetwork()
	if err := st.GetSetting(ctx, settingNetwork, &n); err != nil {
		return err
	}
	n.MgmtAllowed = append([]string(nil), netacl.DefaultManagement...)
	if n.Validate() != nil {
		n = defaultNetwork()
	}
	return st.SetSetting(ctx, settingNetwork, n)
}

// ---- security settings

const settingSecurity = "security"

// SecuritySettings control sessions and the brute-force protection.
type SecuritySettings struct {
	SessionHours int `json:"session_hours"`
	IdleMinutes  int `json:"idle_minutes"`
	// MaxAttempts failed sign-ins of one address within WindowMinutes
	// block it for BlockMinutes (doubling for repeated blocks in a day).
	MaxAttempts   int `json:"max_attempts"`
	WindowMinutes int `json:"window_minutes"`
	BlockMinutes  int `json:"block_minutes"`
}

// DefaultSecurity: a monitoring console is often left open on a wall
// screen, so sessions last a working day.
var DefaultSecurity = SecuritySettings{SessionHours: 12, IdleMinutes: 60, MaxAttempts: 5, WindowMinutes: 15, BlockMinutes: 30}

// Validate checks the ranges.
func (x SecuritySettings) Validate() error {
	switch {
	case x.SessionHours < 1 || x.SessionHours > 24:
		return errors.New("the session length must be 1–24 hours")
	case x.IdleMinutes < 5 || x.IdleMinutes > 1440:
		return errors.New("the inactivity timeout must be 5–1440 minutes")
	case x.MaxAttempts < 3 || x.MaxAttempts > 50:
		return errors.New("failed attempts must be 3–50")
	case x.WindowMinutes < 1 || x.WindowMinutes > 1440:
		return errors.New("the counting period must be 1–1440 minutes")
	case x.BlockMinutes < 1 || x.BlockMinutes > 10080:
		return errors.New("the block must last 1–10080 minutes")
	}
	return nil
}

func (s *Server) security(ctx context.Context) SecuritySettings {
	x := DefaultSecurity
	if err := s.store.GetSetting(ctx, settingSecurity, &x); err != nil || x.Validate() != nil {
		return DefaultSecurity
	}
	return x
}

// ---- metric retention

const settingRetention = "retention"

func (s *Server) retention(ctx context.Context) tsdb.Retention {
	r := tsdb.DefaultRetention
	if err := s.store.GetSetting(ctx, settingRetention, &r); err != nil || r.Validate() != nil {
		return tsdb.DefaultRetention
	}
	return r
}

// ---- handlers

// Handler is the management console.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(gzipStatic(sub, http.FileServer(http.FS(sub))))))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/favicon.svg", http.StatusMovedPermanently)
	})

	mux.HandleFunc("GET /setup", s.handleSetup)
	mux.HandleFunc("POST /setup", s.handleSetup)
	mux.HandleFunc("GET /login", s.handleLogin)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("GET /login/2fa", s.handleLogin2FA)
	mux.HandleFunc("POST /login/2fa", s.handleLogin2FA)
	mux.HandleFunc("GET /login/enroll", s.handleEnroll)
	mux.HandleFunc("POST /login/enroll", s.handleEnroll)
	mux.HandleFunc("GET /login/sso", s.handleSSOStart)
	mux.HandleFunc("GET /login/sso/callback", s.handleOIDCCallback)
	mux.HandleFunc("POST /login/sso/saml", s.handleSAMLACS)
	mux.HandleFunc("GET /login/sso/metadata", s.handleSAMLMetadata)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("POST /lang", s.handleLang)

	app := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireUser(h)) }
	write := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireUser(s.requireWrite(h))) }
	admin := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireUser(s.requireAdmin(h))) }
	app("GET /{$}", s.handleOverview)
	app("GET /devices", s.handleDevices)
	write("GET /devices/new", s.handleDeviceForm)
	write("POST /devices/new", s.handleDeviceCreate)
	app("GET /devices/{id}", s.handleDevice)
	write("GET /devices/{id}/edit", s.handleDeviceForm)
	write("POST /devices/{id}/edit", s.handleDeviceUpdate)
	write("POST /devices/{id}/delete", s.handleDeviceDelete)
	write("POST /devices/{id}/graphs", s.handleDeviceGraphs)
	write("POST /devices/{id}/backup", s.handleBackupSettings)
	write("POST /devices/{id}/backup/run", s.handleBackupRun)
	write("GET /devices/{id}/configs/{v}", s.handleConfigVersion)
	write("POST /devices/{id}/checks", s.handleCheckCreate)
	write("GET /devices/{id}/discover", s.handleDiscover)
	write("POST /devices/{id}/discover", s.handleDiscoverSave)
	write("POST /checks/{id}", s.handleCheckUpdate)
	write("POST /checks/{id}/delete", s.handleCheckDelete)
	write("POST /checks/{id}/run", s.handleCheckRun)
	app("GET /series/{id}", s.handleSeries)
	app("GET /maps", s.handleMaps)
	write("POST /maps", s.handleMapCreate)
	app("GET /maps/{id}", s.handleMap)
	app("GET /maps/{id}/layout", s.handleMapLayout)
	app("GET /maps/{id}/live", s.handleMapLive)
	write("POST /maps/{id}/layout", s.handleMapSave)
	write("GET /maps/{id}/suggest", s.handleMapSuggest)
	app("GET /assets", s.handleAssets)
	write("POST /assets", s.handleAssetUpload)
	app("GET /assets/{id}", s.handleAsset)
	write("POST /assets/{id}/delete", s.handleAssetDelete)
	write("GET /pictures", s.handlePictures)
	write("POST /pictures/upload", s.handlePictureUpload)
	write("POST /pictures/copy", s.handlePictureCopy)
	write("GET /pictures/builtin", s.handleBuiltinPicture)
	write("POST /pictures/builtin", s.handleBuiltinPictureSave)
	write("GET /pictures/{id}", s.handlePicture)
	write("POST /pictures/{id}", s.handlePictureSave)
	write("POST /pictures/{id}/clone", s.handlePictureClone)
	write("POST /pictures/{id}/delete", s.handlePictureDelete)
	admin("GET /settings/datasources", s.handleDataSources)
	admin("POST /settings/datasources", s.handleDataSourceCreate)
	admin("GET /settings/datasources/{id}", s.handleDataSource)
	admin("POST /settings/datasources/{id}", s.handleDataSourceUpdate)
	admin("POST /settings/datasources/{id}/delete", s.handleDataSourceDelete)
	write("POST /datasources/try", s.handleQueryTry)
	app("GET /devices/{id}/graphs/{panel}/q/{n}", s.handlePanelQuery)
	app("GET /dashboards/{id}/widgets/{n}/data", s.handleWidgetQuery)
	admin("GET /settings/kiosks", s.handleKiosks)
	admin("POST /settings/kiosks", s.handleKioskCreate)
	admin("GET /settings/kiosks/{id}", s.handleKiosk)
	admin("POST /settings/kiosks/{id}", s.handleKioskUpdate)
	admin("POST /settings/kiosks/{id}/delete", s.handleKioskDelete)
	mux.HandleFunc("GET /kiosk/{token}", s.handleKioskPage)
	mux.HandleFunc("GET /kiosk/{token}/layout/{map}", s.handleKioskLayout)
	mux.HandleFunc("GET /kiosk/{token}/live/{map}", s.handleKioskLive)
	mux.HandleFunc("GET /kiosk/{token}/assets/{id}", s.handleKioskAsset)
	write("POST /maps/{id}", s.handleMapUpdate)
	write("POST /maps/{id}/delete", s.handleMapDelete)
	app("GET /agents", s.handleAgents)
	write("POST /agents/tokens", s.handleTokenCreate)
	write("POST /agents/tokens/{id}/revoke", s.handleTokenRevoke)
	app("GET /downloads/{name}", s.handleDownload)
	write("POST /devices/{id}/services", s.handleAgentServices)
	write("POST /devices/{id}/agent/remove", s.handleAgentRemove)
	write("POST /devices/{id}/agent/refresh", s.handleAgentRefresh)
	app("GET /devices/{id}/open/{tool}", s.handleOpen)
	app("GET /devices/{id}/rdp", s.handleRDP)
	app("GET /launcher", s.handleLauncher)
	app("GET /dashboards", s.handleDashboards)
	write("POST /dashboards", s.handleDashboardCreate)
	app("GET /dashboards/{id}", s.handleDashboard)
	write("POST /dashboards/{id}", s.handleDashboardUpdate)
	write("POST /dashboards/{id}/delete", s.handleDashboardDelete)
	write("POST /dashboards/{id}/widgets", s.handleWidgetAdd)
	write("POST /dashboards/{id}/widgets/{n}", s.handleWidgetChange)
	app("GET /docs", s.handleDocs)
	app("GET /docs/search", s.handleDocsSearch)
	app("GET /docs/{page}", s.handleDocs)
	app("GET /reports", s.handleReports)
	app("GET /reports.csv", s.handleReportCSV)
	admin("POST /reports/mail", s.handleReportMail)
	app("GET /problems", s.handleProblems)
	app("GET /logs", s.handleLogs)
	write("GET /discovery", s.handleDiscovery)
	write("POST /discovery/ranges", s.handleDiscoveryRangeAdd)
	write("POST /discovery/ranges/{id}", s.handleDiscoveryRange)
	write("POST /discovery/hosts", s.handleDiscoveryHosts)
	admin("GET /settings/logs", s.handleLogSettings)
	admin("POST /settings/logs", s.handleLogSettingsSave)
	admin("GET /settings/console-backup", s.handleConsoleBackup)
	admin("POST /settings/console-backup", s.handleConsoleBackupSave)
	admin("POST /settings/console-backup/run", s.handleConsoleBackupRun)
	admin("POST /settings/console-backup/test", s.handleConsoleBackupTest)
	admin("GET /settings/console-backup/file/{name}", s.handleConsoleBackupDownload)
	admin("POST /settings/console-backup/delete", s.handleConsoleBackupDelete)
	admin("GET /settings/ldap", s.handleLDAPSettings)
	admin("POST /settings/ldap", s.handleLDAPSave)
	admin("POST /settings/ldap/test", s.handleLDAPTest)
	admin("GET /settings/sso", s.handleSSOSettings)
	admin("POST /settings/sso", s.handleSSOSave)
	admin("GET /settings/flows", s.handleFlowSettings)
	admin("POST /settings/flows", s.handleFlowSettingsSave)
	app("GET /flows", s.handleFlows)
	app("GET /flows/series/{device}", s.handleFlowSeries)
	write("POST /problems/{id}/ack", s.handleProblemAck)
	app("GET /alerts/triggers", s.handleTriggers)
	app("GET /alerts/triggers/{id}", s.handleTriggers)
	write("POST /alerts/triggers", s.handleTriggerCreate)
	write("POST /alerts/triggers/{id}", s.handleTriggerUpdate)
	write("POST /alerts/triggers/{id}/delete", s.handleTriggerDelete)
	app("GET /alerts/maintenance", s.handleMaintenance)
	write("POST /alerts/maintenance", s.handleMaintenanceCreate)
	write("POST /alerts/maintenance/{id}/end", s.handleMaintenanceEnd)
	app("GET /groups", s.handleGroups)
	write("POST /groups", s.handleGroupCreate)
	write("POST /groups/{id}", s.handleGroupUpdate)
	write("POST /groups/{id}/delete", s.handleGroupDelete)
	app("GET /account", s.handleAccount)
	app("POST /account/password", s.handleAccountPassword)
	app("POST /account/recovery", s.handleAccountRecovery)
	app("POST /account/sessions/signout", s.handleAccountSignout)
	app("POST /account/tokens", s.handleAPITokenCreate)
	app("POST /account/tokens/{id}/delete", s.handleAPITokenDelete)
	admin("GET /users", s.handleUsers)
	admin("POST /users", s.handleUserCreate)
	admin("GET /users/{id}", s.handleUser)
	admin("POST /users/{id}", s.handleUserUpdate)
	admin("POST /users/{id}/password", s.handleUserPassword)
	admin("POST /users/{id}/reset-2fa", s.handleUserReset2FA)
	admin("GET /audit", s.handleAudit)
	admin("GET /settings", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/settings/network", http.StatusFound)
	})
	admin("GET /settings/network", s.handleNetwork)
	admin("POST /settings/network", s.handleNetworkSave)
	admin("GET /settings/security", s.handleSecurity)
	admin("POST /settings/security", s.handleSecuritySave)
	admin("POST /settings/security/unblock", s.handleUnblock)
	admin("GET /settings/data", s.handleRetention)
	admin("POST /settings/data", s.handleRetentionSave)
	admin("GET /settings/version", s.handleVersion)
	admin("POST /settings/version/check", s.handleVersionCheck)
	admin("POST /settings/version/install", s.handleVersionInstall)
	admin("POST /settings/version/settings", s.handleVersionSettings)
	admin("POST /settings/timezone", s.handleTimeZone)
	admin("POST /settings/restart", s.handleRestart)
	admin("POST /settings/server-network", s.handleServerNetwork)
	admin("GET /settings/certificate", s.handleCertificate)
	admin("POST /settings/certificate", s.handleCertificateSave)
	admin("POST /settings/certificate/renew", s.handleCertificateRenew)
	admin("POST /settings/os", s.handleOS)
	admin("POST /settings/ssh", s.handleSSH)
	admin("POST /settings/version/upload", s.handleVersionUpload)
	admin("GET /settings/tools", s.handleTools)
	admin("POST /settings/tools", s.handleToolCreate)
	admin("GET /settings/tools/{id}", s.handleTools)
	admin("POST /settings/tools/{id}", s.handleToolUpdate)
	admin("POST /settings/tools/{id}/delete", s.handleToolDelete)
	admin("GET /settings/notifications", s.handleNotifications)
	admin("POST /settings/notifications", s.handleNotificationsSave)
	admin("POST /settings/notifications/test", s.handleNotificationsTestMail)
	admin("POST /settings/notifications/channels", s.handleChannelCreate)
	admin("POST /settings/notifications/channels/{id}", s.handleChannelUpdate)
	admin("POST /settings/notifications/channels/{id}/delete", s.handleChannelDelete)
	admin("POST /settings/notifications/channels/{id}/test", s.handleChannelTest)
	admin("GET /settings/snmp", s.handleSNMPProfiles)
	admin("POST /settings/snmp", s.handleSNMPCreate)
	admin("GET /settings/snmp/{id}", s.handleSNMPProfile)
	admin("POST /settings/snmp/{id}", s.handleSNMPUpdate)
	admin("POST /settings/snmp/{id}/delete", s.handleSNMPDelete)

	// the REST API signs in with tokens only: no session, no CSRF check
	top := http.NewServeMux()
	top.Handle("/api/", s.apiHandler())
	top.Handle("/", s.csrf(s.loadSession(mux)))
	return s.secure(s.aclCheck(s.MgmtACL, top))
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

// secure sets the security headers of every console response.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		// same-origin (not no-referrer): browsers then still send a real
		// Origin header on form posts, which the CSRF check needs.
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Cross-Origin-Embedder-Policy", "require-corp") // everything the console loads is its own
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// aclCheck repeats the listener's address check per request (defense in
// depth, and the dev HTTP listener has no listener check).
func (s *Server) aclCheck(l *netacl.List, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.AllowsAddr(r.RemoteAddr) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrf refuses state-changing requests that do not come from a page of
// this console: the Origin (or, without it, the Referer) must match the
// Host, and Sec-Fetch-Site, where sent, must be same-origin.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		// The identity provider posts the SAML response from its own site; the
		// response is signed and must answer a request of this console.
		if r.Method == http.MethodPost && r.URL.Path == "/login/sso/saml" {
			next.ServeHTTP(w, r)
			return
		}
		if !sameOrigin(r) {
			s.log.Warn("cross-site request refused", "ip", clientIP(r), "path", r.URL.Path, "origin", r.Header.Get("Origin"))
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request) bool {
	if sf := r.Header.Get("Sec-Fetch-Site"); sf != "" && sf != "same-origin" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	want := scheme + "://" + r.Host
	if o := r.Header.Get("Origin"); o != "" {
		return o == want
	}
	ref := r.Header.Get("Referer")
	return ref != "" && (ref == want || strings.HasPrefix(ref, want+"/"))
}

// clientIP is the address of the direct peer (no proxy support yet).
func clientIP(r *http.Request) string {
	if a, ok := netacl.AddrOf(r.RemoteAddr); ok {
		return a.String()
	}
	return r.RemoteAddr
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "notfound", &Page{Title: "Not found"})
}

func (s *Server) forbidden(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusForbidden, "forbidden", &Page{Title: "Not allowed"})
}

func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

// audit records an action of the signed-in user.
func (s *Server) audit(r *http.Request, action, target, format string, args ...any) {
	name := "-"
	if u := userFrom(r); u != nil {
		name = u.Username
	}
	s.auditAs(r, name, action, target, format, args...)
}

func (s *Server) auditAs(r *http.Request, user, action, target, format string, args ...any) {
	detail := format
	if len(args) > 0 {
		detail = fmt.Sprintf(format, args...)
	}
	if err := s.store.Audit(r.Context(), user, clientIP(r), action, target, detail); err != nil {
		s.log.Error("audit log write failed", "action", action, "err", err)
	}
}

// storeErr turns a store error into a message for the user.
func storeErr(err error) string {
	switch {
	case errors.Is(err, store.ErrConflict):
		return "The name is already used."
	case errors.Is(err, store.ErrNotFound):
		return "Not found."
	}
	return err.Error()
}
