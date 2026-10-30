package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/max-zit/watchzit/internal/logrecv"
	"github.com/max-zit/watchzit/internal/store"
)

// Syslog messages and SNMP traps: the receiver's settings and the list.

const settingLogs = "logs"

// LogConfig reads the receiver's settings.
func (s *Server) LogConfig(ctx context.Context) logrecv.Config {
	c := logrecv.Defaults()
	_ = s.store.GetSetting(ctx, settingLogs, &c)
	return c
}

type logSettingsData struct {
	C        logrecv.Config
	Received int64
	Dropped  int
	Err      string
	Off      bool // no receiver (development server without it)
}

func (s *Server) handleLogSettings(w http.ResponseWriter, r *http.Request) {
	d := &logSettingsData{C: s.LogConfig(r.Context()), Off: s.Logs == nil}
	if s.Logs != nil {
		_, d.Received, d.Dropped, d.Err = s.Logs.Status()
	}
	s.render(w, r, http.StatusOK, "logsettings", &Page{Title: "Syslog and traps", Nav: "settings", Data: d})
}

func (s *Server) handleLogSettingsSave(w http.ResponseWriter, r *http.Request) {
	const back = "/settings/logs"
	c := logrecv.Config{Syslog: r.FormValue("syslog") == "1", Traps: r.FormValue("traps") == "1", Unknown: r.FormValue("unknown") == "1",
		SyslogPort: formInt(r, "syslog_port"), TrapPort: formInt(r, "trap_port"), KeepDays: formInt(r, "keep_days")}
	for _, x := range strings.FieldsFunc(r.FormValue("communities"), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' }) {
		if len(c.Communities) < 20 && len(x) <= 64 {
			c.Communities = append(c.Communities, x)
		}
	}
	if c.SyslogPort < 1 || c.SyslogPort > 65535 || c.TrapPort < 1 || c.TrapPort > 65535 || c.SyslogPort == c.TrapPort {
		s.redirectErr(w, r, back, "Enter two different ports between 1 and 65535.")
		return
	}
	if c.KeepDays < 1 || c.KeepDays > 730 {
		s.redirectErr(w, r, back, "Keep the messages 1–730 days.")
		return
	}
	if err := s.store.SetSetting(r.Context(), settingLogs, c); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "settings.logs", "settings", "syslog %v (port %d), traps %v (port %d), keep %d days", c.Syslog, c.SyslogPort, c.Traps, c.TrapPort, c.KeepDays)
	if s.Logs != nil {
		if err := s.Logs.Apply(c); err != nil {
			s.redirectErr(w, r, back, translate(s.lang(r, userFrom(r)), "Saved, but: %s", err.Error()))
			return
		}
	}
	s.redirect(w, r, back, "Saved.")
}

type logsData struct {
	Logs     []*store.LogMessage
	F        store.LogFilter
	Devices  []*store.Device
	Query    string // the filter as URL parameters (paging)
	Older    int64  // the ID for the next page
	Received bool   // the receiver is on
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	f := store.LogFilter{Kind: r.FormValue("kind"), Text: strings.TrimSpace(r.FormValue("q")), MaxLevel: 7, Limit: 200}
	f.DeviceID, _ = strconv.ParseInt(r.FormValue("device"), 10, 64)
	f.Before, _ = strconv.ParseInt(r.FormValue("before"), 10, 64)
	if l, err := strconv.Atoi(r.FormValue("level")); err == nil && l >= 0 && l <= 7 {
		f.MaxLevel = l
	}
	if f.Kind != store.LogSyslog && f.Kind != store.LogTrap {
		f.Kind = ""
	}
	if len(f.Text) > 200 {
		f.Text = f.Text[:200]
	}
	logs, err := s.store.Logs(r.Context(), f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	devs, _ := s.store.Devices(r.Context(), store.DeviceFilter{})
	c := s.LogConfig(r.Context())
	d := &logsData{Logs: logs, F: f, Devices: devs, Received: c.Syslog || c.Traps,
		Query: "kind=" + f.Kind + "&q=" + url.QueryEscape(f.Text) + "&level=" + strconv.Itoa(f.MaxLevel) + "&device=" + strconv.FormatInt(f.DeviceID, 10)}
	if len(logs) == f.Limit {
		d.Older = logs[len(logs)-1].ID
	}
	s.render(w, r, http.StatusOK, "logs", &Page{Title: "Logs", Nav: "logs", Data: d})
}
