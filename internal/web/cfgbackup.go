package web

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/cfgbackup"
	"github.com/max-zit/watchzit/internal/store"
)

// Configuration backups of network devices: read over SSH every night (and
// on demand), kept as versions when something changed, compared line by
// line. Configurations hold secrets, so only operators and administrators
// see them.

const settingConfigBackup = "config_backup"

// ConfigBackupSettings: when the nightly backups run and how many versions
// are kept per device.
type ConfigBackupSettings struct {
	Hour int    `json:"hour"`
	Keep int    `json:"keep"`
	Last string `json:"last,omitempty"` // the day of the last nightly run
}

func (s *Server) configBackupSettings(ctx context.Context) ConfigBackupSettings {
	c := ConfigBackupSettings{Hour: 2, Keep: 50}
	_ = s.store.GetSetting(ctx, settingConfigBackup, &c)
	return c
}

// FetchConfig reads configurations (tests replace it).
var FetchConfig = cfgbackup.Fetch

// backupDevice reads the configuration of one device and stores it when it
// changed; changes and failures also go to the device's log.
func (s *Server) backupDevice(ctx context.Context, b *store.DeviceBackup) (changed bool, err error) {
	res, err := FetchConfig(ctx, cfgbackup.Target{Address: b.Address, Port: b.Port, User: b.User, Password: b.Secret, Key: b.Key,
		HostKey: b.HostKey, Method: b.Method, Command: b.Command})
	now := s.now()
	hostKey := ""
	if res != nil {
		hostKey = res.HostKey
	}
	_ = s.store.BackupRan(ctx, b.DeviceID, now, hostKey, err)
	logMsg := func(sev int, text string) {
		_ = s.store.AddLogs(ctx, []store.LogMessage{{At: now, DeviceID: b.DeviceID, Source: netip.IPv4Unspecified(), Kind: store.LogSyslog, Facility: -1, Severity: sev,
			Host: "watchzit", Tag: "config-backup", Message: text}})
	}
	if err != nil {
		logMsg(3, "configuration backup failed: "+err.Error())
		return false, err
	}
	last, err := s.store.LatestConfig(ctx, b.DeviceID)
	if err != nil {
		return false, err
	}
	h := cfgbackup.Hash(res.Text)
	if last != nil && last.Hash == h {
		return false, nil
	}
	v := store.ConfigVersion{DeviceID: b.DeviceID, At: now, Hash: h, Content: res.Text}
	if last != nil {
		_, v.Added, v.Removed = cfgbackup.Diff(last.Content, res.Text)
	}
	if _, err := s.store.AddConfigVersion(ctx, v, s.configBackupSettings(ctx).Keep); err != nil {
		return false, err
	}
	if last != nil {
		logMsg(5, "configuration changed: +"+strconv.Itoa(v.Added)+" −"+strconv.Itoa(v.Removed)+" lines")
	}
	return true, nil
}

// RunConfigBackups backs up all devices once a night at the set hour.
func (s *Server) RunConfigBackups(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.NightlyBackups(ctx)
		}
	}
}

// NightlyBackups runs the backups if it is their hour and they did not run
// today.
func (s *Server) NightlyBackups(ctx context.Context) {
	c := s.configBackupSettings(ctx)
	now := s.now().Local()
	day := now.Format("2006-01-02")
	if now.Hour() < c.Hour || c.Last == day {
		return
	}
	c.Last = day
	_ = s.store.SetSetting(ctx, settingConfigBackup, c)
	bs, err := s.store.DeviceBackups(ctx)
	if err != nil {
		s.log.Error("config backups", "err", err)
		return
	}
	for _, b := range bs {
		if _, err := s.backupDevice(ctx, b); err != nil {
			s.log.Warn("config backup failed", "device", b.Device, "err", err)
		}
	}
}

func (s *Server) handleBackupSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	back := "/devices/" + strconv.FormatInt(id, 10) + "#backup"
	if r.FormValue("do") == "delete" {
		if err := s.store.DeleteDeviceBackup(r.Context(), id); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.audit(r, "backup.delete", "device:"+strconv.FormatInt(id, 10), "configuration backups switched off")
		s.redirect(w, r, back, "Saved.")
		return
	}
	b := store.DeviceBackup{DeviceID: id, Enabled: r.FormValue("enabled") == "1", Method: r.FormValue("method"), Port: formInt(r, "port"),
		User: r.FormValue("user"), Secret: r.FormValue("secret"), Key: strings.TrimSpace(r.FormValue("key")), Command: r.FormValue("command")}
	if err := s.store.SaveDeviceBackup(r.Context(), b, r.FormValue("clear_secret") == "1", r.FormValue("clear_key") == "1"); err != nil {
		s.redirectErr(w, r, back, storeErr(err))
		return
	}
	s.audit(r, "backup.settings", "device:"+strconv.FormatInt(id, 10), "configuration backup: %s as %s on port %d (enabled %v)", b.Method, b.User, b.Port, b.Enabled)
	s.redirect(w, r, back, "Saved.")
}

func (s *Server) handleBackupRun(w http.ResponseWriter, r *http.Request) {
	lang := s.lang(r, userFrom(r))
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	back := "/devices/" + strconv.FormatInt(id, 10) + "#backup"
	if r.FormValue("do") == "forget-key" {
		_ = s.store.ForgetHostKey(r.Context(), id)
		s.audit(r, "backup.hostkey", "device:"+strconv.FormatInt(id, 10), "saved SSH host key forgotten")
		s.redirect(w, r, back, "The next backup saves the host key it sees.")
		return
	}
	b, err := s.store.DeviceBackupOf(r.Context(), id)
	if err != nil {
		s.redirectErr(w, r, back, "Set up the backup first.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	changed, err := s.backupDevice(ctx, b)
	s.audit(r, "backup.run", "device:"+strconv.FormatInt(id, 10), "configuration backup: changed %v, %s", changed, errText(err))
	if err != nil {
		if errors.Is(err, cfgbackup.ErrHostKey) {
			s.redirectErr(w, r, back, "The SSH host key of the device changed. If that was expected (new device or firmware), forget the saved key and back up again.")
			return
		}
		if errors.Is(err, cfgbackup.ErrAuth) {
			s.redirectErr(w, r, back, "The device refused the user, password or key. Check them in the settings below.")
			return
		}
		s.redirectErr(w, r, back, translate(lang, "The backup failed: %s", err.Error()))
		return
	}
	if changed {
		s.redirect(w, r, back, "A new version of the configuration was saved.")
		return
	}
	s.redirect(w, r, back, "The configuration has not changed.")
}

type configData struct {
	D       *store.Device
	V, Prev *store.ConfigVersion
	Diff    []cfgbackup.Line
	Full    bool
}

func (s *Server) handleConfigVersion(w http.ResponseWriter, r *http.Request) {
	dev, ok1 := pathID(r, "id")
	vid, err := strconv.ParseInt(r.PathValue("v"), 10, 64)
	if !ok1 || err != nil {
		s.notFound(w, r)
		return
	}
	d, err := s.store.DeviceByID(r.Context(), dev)
	if err != nil {
		s.notFound(w, r)
		return
	}
	cur, prev, err := s.store.ConfigVersionByID(r.Context(), dev, vid)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if r.FormValue("download") == "1" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeFileName(d.Name)+"-"+cur.At.Format("20060102-1504")+`.txt"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(cur.Content))
		return
	}
	data := &configData{D: d, V: cur, Prev: prev, Full: r.FormValue("full") == "1" || prev == nil}
	if prev != nil {
		lines, _, _ := cfgbackup.Diff(prev.Content, cur.Content)
		if !data.Full {
			lines = cfgbackup.Context(lines, 3)
		}
		data.Diff = lines
	}
	s.audit(r, "backup.view", "device:"+strconv.FormatInt(dev, 10), "configuration version %d viewed", vid)
	s.render(w, r, http.StatusOK, "config", &Page{Title: d.Name, Nav: "devices", Data: data})
}

// safeFileName keeps letters, digits, dots and dashes.
func safeFileName(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_' {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "config"
	}
	return b.String()
}
