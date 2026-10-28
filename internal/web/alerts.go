package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// kickAlerts asks the alert engine for an evaluation (after a change that
// opens or closes problems).
func (s *Server) kickAlerts() {
	if s.Alerts != nil {
		s.Alerts.Kick()
	}
}

// ---- problems

type problemsData struct {
	Problems   []*store.Problem
	History    bool
	Severity   string
	Suppressed bool
	Counts     map[string]int
	Now        time.Time
}

func (s *Server) handleProblems(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := &problemsData{History: r.FormValue("view") == "history", Severity: r.FormValue("severity"), Suppressed: r.FormValue("suppressed") == "1", Now: s.now()}
	if store.SeverityRank(d.Severity) < 0 {
		d.Severity = ""
	}
	var err error
	d.Problems, err = s.store.Problems(ctx, store.ProblemFilter{Open: !d.History, Suppressed: d.Suppressed, Severity: d.Severity,
		Since: s.now().AddDate(0, 0, -30), Limit: 500})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Counts, err = s.store.ProblemCounts(ctx); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "problems", &Page{Title: "Problems", Nav: "problems", Data: d})
}

func (s *Server) handleProblemAck(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	p, err := s.store.ProblemByID(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	back := safeBack(r.FormValue("back"), "/problems")
	if err := s.store.AckProblem(r.Context(), id, userFrom(r).Username, r.FormValue("note")); err != nil {
		s.redirectErr(w, r, back, storeErr(err))
		return
	}
	s.audit(r, "problem.ack", "device:"+strconv.FormatInt(p.DeviceID, 10), "problem #%d %q on %s acknowledged: %s", p.ID, p.Title, p.Device, strings.TrimSpace(r.FormValue("note")))
	s.redirect(w, r, back, "Problem acknowledged.")
}

// safeBack allows only local paths as a return address.
func safeBack(back, def string) string {
	if back == "" || !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") || strings.ContainsAny(back, "\\\r\n") {
		return def
	}
	return back
}

// ---- triggers

type triggersData struct {
	Triggers []*store.Trigger
	Edit     *store.Trigger
	In       store.TriggerInput
	ForMin   string
	Groups   []store.Group
	Devices  []*store.Device
	Metrics  []metricChoice
	Types    []string
}

type metricChoice struct {
	Metric string
	Name   string
	Unit   string
	Types  []string
}

// metricChoices are the values a metric trigger can watch.
func metricChoices() []metricChoice {
	var out []metricChoice
	for m, types := range store.TriggerMetrics() {
		if m == "value" {
			continue // SNMP OID values have their own units; a later version
		}
		out = append(out, metricChoice{Metric: m, Name: metricNames[m], Unit: store.MetricUnit(types[0], m, store.CheckParams{}), Types: types})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metric < out[j].Metric })
	return out
}

func (s *Server) handleTriggers(w http.ResponseWriter, r *http.Request) {
	d := &triggersData{In: store.TriggerInput{Kind: store.TriggerMetric, Op: ">", Severity: store.SeverityWarning, Enabled: true, Metric: "cpu_pct", Threshold: 90}, ForMin: "5"}
	if id, ok := pathID(r, "id"); ok {
		t, err := s.store.TriggerByID(r.Context(), id)
		if err != nil {
			s.notFound(w, r)
			return
		}
		d.Edit = t
		d.In = store.TriggerInput{Name: t.Name, Kind: t.Kind, CheckTypes: t.CheckTypes, Metric: t.Metric, Op: t.Op, Threshold: t.Threshold, ForS: t.ForS,
			Severity: t.Severity, GroupID: t.GroupID, DeviceID: t.DeviceID, Enabled: t.Enabled}
		d.ForMin = minutesText(t.ForS)
	}
	s.showTriggers(w, r, http.StatusOK, d, "")
}

func minutesText(sec int) string {
	return strconv.FormatFloat(float64(sec)/60, 'f', -1, 64)
}

func (s *Server) showTriggers(w http.ResponseWriter, r *http.Request, status int, d *triggersData, errMsg string) {
	ctx := r.Context()
	var err error
	if d.Triggers, err = s.store.Triggers(ctx); err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Groups, err = s.store.Groups(ctx); err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Devices, err = s.store.Devices(ctx, store.DeviceFilter{}); err != nil {
		s.serverError(w, r, err)
		return
	}
	sort.Slice(d.Devices, func(i, j int) bool { return strings.ToLower(d.Devices[i].Name) < strings.ToLower(d.Devices[j].Name) })
	d.Metrics = metricChoices()
	d.Types = []string{store.CheckICMP, store.CheckTCP, store.CheckSNMP, store.CheckSNMPIf, store.CheckSNMPCPU, store.CheckSNMPStor, store.CheckSNMPOID,
		store.CheckAgent, store.CheckAgentCPU, store.CheckAgentMem, store.CheckAgentDisk, store.CheckAgentNet, store.CheckAgentService}
	p := &Page{Title: "Triggers", Nav: "problems", Data: d}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "triggers", p)
}

func triggerInput(r *http.Request) (store.TriggerInput, string) {
	_ = r.ParseForm()
	in := store.TriggerInput{Name: r.FormValue("name"), Kind: r.FormValue("kind"), Metric: r.FormValue("metric"), Op: r.FormValue("op"),
		Severity: r.FormValue("severity"), Enabled: r.FormValue("enabled") == "1"}
	in.Threshold, _ = strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(r.FormValue("threshold")), ",", "."), 64)
	forMin := strings.ReplaceAll(strings.TrimSpace(r.FormValue("for")), ",", ".")
	if m, err := strconv.ParseFloat(forMin, 64); err == nil && m >= 0 && m <= 1440 {
		in.ForS = int(m * 60)
	} else if forMin != "" {
		in.ForS = -1
	}
	if scope := r.FormValue("scope"); strings.HasPrefix(scope, "g") {
		in.GroupID, _ = strconv.ParseInt(scope[1:], 10, 64)
	} else if strings.HasPrefix(scope, "d") {
		in.DeviceID, _ = strconv.ParseInt(scope[1:], 10, 64)
	}
	in.CheckTypes = append(in.CheckTypes, r.Form["type"]...)
	return in, forMin
}

func (s *Server) handleTriggerCreate(w http.ResponseWriter, r *http.Request) {
	in, forMin := triggerInput(r)
	id, err := s.store.CreateTrigger(r.Context(), in)
	if err != nil {
		s.showTriggers(w, r, http.StatusBadRequest, &triggersData{In: in, ForMin: forMin}, storeErr(err))
		return
	}
	s.audit(r, "trigger.create", "trigger:"+strconv.FormatInt(id, 10), "trigger %q added (%s, %s)", in.Name, in.Kind, in.Severity)
	s.kickAlerts()
	s.redirect(w, r, "/alerts/triggers", "Trigger added.")
}

func (s *Server) handleTriggerUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	t, err := s.store.TriggerByID(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	in, forMin := triggerInput(r)
	if err := s.store.UpdateTrigger(r.Context(), id, in); err != nil {
		s.showTriggers(w, r, http.StatusBadRequest, &triggersData{Edit: t, In: in, ForMin: forMin}, storeErr(err))
		return
	}
	s.audit(r, "trigger.update", "trigger:"+strconv.FormatInt(id, 10), "trigger %q changed (%s, %s, enabled %v)", in.Name, in.Kind, in.Severity, in.Enabled)
	s.kickAlerts()
	s.redirect(w, r, "/alerts/triggers", "Saved.")
}

func (s *Server) handleTriggerDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	t, err := s.store.TriggerByID(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := s.store.DeleteTrigger(r.Context(), id); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "trigger.delete", "trigger:"+strconv.FormatInt(id, 10), "trigger %q deleted", t.Name)
	s.kickAlerts()
	s.redirect(w, r, "/alerts/triggers", "Trigger deleted.")
}

// ---- maintenance

type maintenanceData struct {
	Windows []*store.Maintenance
	Groups  []store.Group
	Devices []*store.Device
	Names   map[string]string // "d1" / "g2" → name
	In      store.Maintenance
	From    string
	To      string
	Now     time.Time
}

const inputTime = "2006-01-02T15:04"

func (s *Server) handleMaintenance(w http.ResponseWriter, r *http.Request) {
	now := s.now().Local().Truncate(time.Minute)
	d := &maintenanceData{From: now.Format(inputTime), To: now.Add(2 * time.Hour).Format(inputTime)}
	if id, err := strconv.ParseInt(r.FormValue("device"), 10, 64); err == nil {
		d.In.DeviceIDs = []int64{id}
	}
	s.showMaintenance(w, r, http.StatusOK, d, "")
}

func (s *Server) showMaintenance(w http.ResponseWriter, r *http.Request, status int, d *maintenanceData, errMsg string) {
	ctx := r.Context()
	d.Now = s.now()
	var err error
	if d.Windows, err = s.store.Maintenances(ctx, d.Now.AddDate(0, 0, -7)); err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Groups, err = s.store.Groups(ctx); err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Devices, err = s.store.Devices(ctx, store.DeviceFilter{}); err != nil {
		s.serverError(w, r, err)
		return
	}
	sort.Slice(d.Devices, func(i, j int) bool { return strings.ToLower(d.Devices[i].Name) < strings.ToLower(d.Devices[j].Name) })
	d.Names = map[string]string{}
	for _, x := range d.Devices {
		d.Names["d"+strconv.FormatInt(x.ID, 10)] = x.Name
	}
	for _, g := range d.Groups {
		d.Names["g"+strconv.FormatInt(g.ID, 10)] = g.Name
	}
	p := &Page{Title: "Maintenance", Nav: "problems", Data: d}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "maintenance", p)
}

func (s *Server) handleMaintenanceCreate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	d := &maintenanceData{From: r.FormValue("from"), To: r.FormValue("to")}
	d.In.Name = r.FormValue("name")
	d.In.CreatedBy = userFrom(r).Username
	for _, v := range r.Form["device"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && !slices.Contains(d.In.DeviceIDs, id) {
			d.In.DeviceIDs = append(d.In.DeviceIDs, id)
		}
	}
	for _, v := range r.Form["group"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && !slices.Contains(d.In.GroupIDs, id) {
			d.In.GroupIDs = append(d.In.GroupIDs, id)
		}
	}
	from, err1 := time.ParseInLocation(inputTime, d.From, time.Local)
	to, err2 := time.ParseInLocation(inputTime, d.To, time.Local)
	if err1 != nil || err2 != nil {
		s.showMaintenance(w, r, http.StatusBadRequest, d, "Enter the start and the end.")
		return
	}
	d.In.StartsAt, d.In.EndsAt = from, to
	if !to.After(s.now()) {
		s.showMaintenance(w, r, http.StatusBadRequest, d, "The end is already past.")
		return
	}
	id, err := s.store.CreateMaintenance(r.Context(), d.In)
	if err != nil {
		s.showMaintenance(w, r, http.StatusBadRequest, d, storeErr(err))
		return
	}
	s.audit(r, "maintenance.create", "maintenance:"+strconv.FormatInt(id, 10), "maintenance %q %s – %s, devices %v, groups %v", d.In.Name,
		from.Format(time.RFC3339), to.Format(time.RFC3339), d.In.DeviceIDs, d.In.GroupIDs)
	s.kickAlerts()
	s.redirect(w, r, "/alerts/maintenance", "Maintenance planned.")
}

func (s *Server) handleMaintenanceEnd(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	m, err := s.store.EndMaintenance(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.audit(r, "maintenance.end", "maintenance:"+strconv.FormatInt(id, 10), "maintenance %q ended or removed", m.Name)
	s.kickAlerts()
	s.redirect(w, r, "/alerts/maintenance", "Maintenance ended.")
}

// ---- Settings → Notifications

type notificationsData struct {
	N        NotifySettings
	Channels []*store.Channel
	HasPass  bool
	Kind     string
}

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	s.showNotifications(w, r, http.StatusOK, nil, "")
}

func (s *Server) showNotifications(w http.ResponseWriter, r *http.Request, status int, n *NotifySettings, errMsg string) {
	saved := s.notifySettings(r.Context())
	d := &notificationsData{N: saved, HasPass: saved.Email.Password != "", Kind: r.FormValue("kind")}
	d.N.Email.Password = ""
	if n != nil {
		d.N = *n
		d.N.Email.Password = ""
	}
	var err error
	if d.Channels, err = s.store.Channels(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	p := &Page{Title: "Notifications", Nav: "settings", Data: d}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "notifications", p)
}

func (s *Server) handleNotificationsSave(w http.ResponseWriter, r *http.Request) {
	n := NotifySettings{ConsoleURL: strings.TrimSpace(r.FormValue("console_url")), Lang: r.FormValue("lang"),
		Email: EmailSettings{Host: strings.TrimSpace(r.FormValue("smtp_host")), Port: formInt(r, "smtp_port"), Security: r.FormValue("smtp_security"),
			Username: strings.TrimSpace(r.FormValue("smtp_user")), Password: r.FormValue("smtp_password"), From: strings.TrimSpace(r.FormValue("smtp_from"))}}
	if err := s.saveNotifySettings(r.Context(), n); err != nil {
		s.showNotifications(w, r, http.StatusBadRequest, &n, err.Error())
		return
	}
	s.audit(r, "settings.notify", "settings", "notification settings saved (SMTP %s:%d %s, sender %s, console %s, language %s)", n.Email.Host, n.Email.Port, n.Email.Security, n.Email.From, n.ConsoleURL, n.Lang)
	s.redirect(w, r, "/settings/notifications", "Saved.")
}

func (s *Server) handleNotificationsTestMail(w http.ResponseWriter, r *http.Request) {
	to, err := parseRecipients(r.FormValue("to"))
	back := "/settings/notifications"
	if err != nil {
		s.redirectErr(w, r, back, err.Error())
		return
	}
	n := s.notifySettings(r.Context())
	lang := n.Lang
	if lang == "" {
		lang = "en"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	err = sendMail(ctx, n.Email, to, translate(lang, "[WatchZit] Test message"), translate(lang, "WatchZit can send notification emails to this address.")+"\n\n-- \nWatchZit\n")
	s.audit(r, "settings.notify.test", "settings", "test email to %s: %s", strings.Join(to, ", "), errText(err))
	if err != nil {
		s.redirectErr(w, r, back, err.Error())
		return
	}
	s.redirect(w, r, back, "Test email sent.")
}

func errText(err error) string {
	if err != nil {
		return err.Error()
	}
	return "ok"
}

// checkChannelURL accepts only full http(s) addresses without credentials.
func checkChannelURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(raw) > 2000 {
		return errChannelURL
	}
	return nil
}

var errChannelURL = uiErr("enter the full https:// address of the channel")

type uiErr string

func (e uiErr) Error() string { return string(e) }

func (s *Server) channelFromForm(r *http.Request) (store.Channel, string, string, error) {
	c := store.Channel{Name: r.FormValue("name"), Kind: r.FormValue("kind"), MinSeverity: r.FormValue("min_severity"),
		OnResolve: r.FormValue("on_resolve") == "1", Enabled: r.FormValue("enabled") == "1"}
	target := strings.TrimSpace(r.FormValue("target"))
	secret := r.FormValue("secret")
	if target != "" {
		if c.Kind == store.ChannelEmail {
			if _, err := parseRecipients(target); err != nil {
				return c, "", "", err
			}
		} else if err := checkChannelURL(target); err != nil {
			return c, "", "", err
		}
	}
	return c, target, secret, nil
}

func (s *Server) handleChannelCreate(w http.ResponseWriter, r *http.Request) {
	back := "/settings/notifications"
	c, target, secret, err := s.channelFromForm(r)
	if err == nil && target == "" {
		err = uiErr("enter the recipients or the address")
	}
	if err == nil && c.Kind != store.ChannelEmail && c.Kind != store.ChannelWebhook && c.Kind != store.ChannelSlack && c.Kind != store.ChannelTeams {
		err = uiErr("unknown channel type")
	}
	if err != nil {
		s.redirectErr(w, r, back, err.Error())
		return
	}
	c.Enabled = true
	if c.Kind == store.ChannelEmail {
		c.Target = target
	} else {
		c.Target = "pending"
	}
	id, err := s.store.CreateChannel(r.Context(), c)
	if err != nil {
		s.redirectErr(w, r, back, storeErr(err))
		return
	}
	if c.Kind != store.ChannelEmail {
		// the sealing purpose names the channel, so the row id comes first
		c.ID = id
		c.Target = s.store.SealSecret("channel.url."+strconv.FormatInt(id, 10), target)
		if secret != "" && c.Kind == store.ChannelWebhook {
			c.Secret = s.store.SealSecret("channel.secret."+strconv.FormatInt(id, 10), secret)
		}
		if err := s.store.UpdateChannel(r.Context(), c); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.audit(r, "channel.create", "channel:"+strconv.FormatInt(id, 10), "notification channel %q added (%s, from %s, resolved %v)", c.Name, c.Kind, c.MinSeverity, c.OnResolve)
	s.redirect(w, r, back, "Channel added.")
}

func (s *Server) channelByID(r *http.Request) *store.Channel {
	id, ok := pathID(r, "id")
	if !ok {
		return nil
	}
	cs, err := s.store.Channels(r.Context())
	if err != nil {
		return nil
	}
	for _, c := range cs {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (s *Server) handleChannelUpdate(w http.ResponseWriter, r *http.Request) {
	back := "/settings/notifications"
	old := s.channelByID(r)
	if old == nil {
		s.notFound(w, r)
		return
	}
	c, target, secret, err := s.channelFromForm(r)
	if err != nil {
		s.redirectErr(w, r, back, err.Error())
		return
	}
	c.ID, c.Kind = old.ID, old.Kind
	id := strconv.FormatInt(c.ID, 10)
	if target != "" {
		if c.Kind == store.ChannelEmail {
			if _, err := parseRecipients(target); err != nil {
				s.redirectErr(w, r, back, err.Error())
				return
			}
			c.Target = target
		} else {
			c.Target = s.store.SealSecret("channel.url."+id, target)
		}
	}
	if secret != "" && c.Kind == store.ChannelWebhook {
		c.Secret = s.store.SealSecret("channel.secret."+id, secret)
	}
	if err := s.store.UpdateChannel(r.Context(), c); err != nil {
		s.redirectErr(w, r, back, storeErr(err))
		return
	}
	s.audit(r, "channel.update", "channel:"+id, "notification channel %q changed (from %s, resolved %v, enabled %v, new address %v)", c.Name, c.MinSeverity, c.OnResolve, c.Enabled, target != "")
	s.redirect(w, r, back, "Saved.")
}

func (s *Server) handleChannelDelete(w http.ResponseWriter, r *http.Request) {
	c := s.channelByID(r)
	if c == nil {
		s.notFound(w, r)
		return
	}
	if err := s.store.DeleteChannel(r.Context(), c.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "channel.delete", "channel:"+strconv.FormatInt(c.ID, 10), "notification channel %q deleted", c.Name)
	s.redirect(w, r, "/settings/notifications", "Channel deleted.")
}

func (s *Server) handleChannelTest(w http.ResponseWriter, r *http.Request) {
	back := "/settings/notifications"
	c := s.channelByID(r)
	if c == nil {
		s.notFound(w, r)
		return
	}
	n := s.notifySettings(r.Context())
	now := s.now()
	ev := notifyEvent{Event: "test", Severity: store.SeverityWarning, Title: "Test notification", Detail: "WatchZit can send problems to this channel.",
		Device: "test-device", Address: "192.0.2.1", StartedAt: now}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	err := s.deliver(ctx, c, n, []notifyEvent{ev})
	_ = s.store.ChannelResult(r.Context(), c.ID, now, err)
	s.audit(r, "channel.test", "channel:"+strconv.FormatInt(c.ID, 10), "test notification to %q: %s", c.Name, errText(err))
	if err != nil {
		s.redirectErr(w, r, back, err.Error())
		return
	}
	s.redirect(w, r, back, "Test sent.")
}

// deviceProblems are the open problems of a device (device page).
func (s *Server) deviceProblems(ctx context.Context, id int64) []*store.Problem {
	ps, err := s.store.Problems(ctx, store.ProblemFilter{Open: true, Suppressed: true, DeviceID: id, Limit: 50})
	if err != nil {
		s.log.Error("cannot load problems", "device", id, "err", err)
	}
	return ps
}
