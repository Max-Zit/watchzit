package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/alert"
	"github.com/max-zit/watchzit/internal/store"
)

// Notifications: problems go to email recipients, webhooks (JSON, signed
// with HMAC-SHA256), Slack and Microsoft Teams. The SMTP password, the
// channel URLs and webhook keys are stored encrypted. Mail and channel code
// come from RMMZit.

const settingNotify = "notify"

// NotifySettings are the SMTP server, the language of notifications and
// the console address used for links in them.
type NotifySettings struct {
	ConsoleURL string        `json:"console_url,omitempty"`
	Lang       string        `json:"lang,omitempty"`
	Email      EmailSettings `json:"email"`
}

// EmailSettings is the SMTP server for notification emails.
type EmailSettings struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"` // starttls, tls, none
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"` // sealed in the database
	From     string `json:"from"`
}

// Configured reports whether email can be sent.
func (e *EmailSettings) Configured() bool { return e.Host != "" && e.From != "" }

// Validate checks the fields.
func (e *EmailSettings) Validate() error {
	if e.Host == "" {
		return nil // email off
	}
	if strings.ContainsAny(e.Host, " /\\") || len(e.Host) > 255 {
		return errors.New("the SMTP server must be a host name or an IP address")
	}
	if e.Port < 1 || e.Port > 65535 {
		return errors.New("the port must be 1–65535")
	}
	if e.Security != "starttls" && e.Security != "tls" && e.Security != "none" {
		return errors.New("unknown connection security")
	}
	if _, err := mail.ParseAddress(e.From); err != nil {
		return errors.New("the sender must be an email address")
	}
	if e.Username != "" && e.Security == "none" && !isLocalHost(e.Host) {
		return errors.New("a password is sent only over an encrypted connection (STARTTLS or TLS)")
	}
	return nil
}

// Validate checks the settings.
func (n *NotifySettings) Validate() error {
	if n.ConsoleURL != "" {
		u, err := url.Parse(n.ConsoleURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" {
			return errors.New("the console address must look like https://monitor.example.com:8443")
		}
		n.ConsoleURL = strings.TrimRight(n.ConsoleURL, "/")
	}
	if n.Lang != "" && !knownLang(n.Lang) {
		n.Lang = ""
	}
	return n.Email.Validate()
}

func knownLang(code string) bool {
	for _, l := range Languages {
		if l.Code == code {
			return true
		}
	}
	return false
}

func isLocalHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// notifySettings loads the settings with the SMTP password decrypted.
func (s *Server) notifySettings(ctx context.Context) NotifySettings {
	n := NotifySettings{Email: EmailSettings{Port: 587, Security: "starttls"}}
	if err := s.store.GetSetting(ctx, settingNotify, &n); err != nil {
		return n
	}
	if n.Email.Password != "" {
		p, err := s.store.OpenSecret("settings.email.password", n.Email.Password)
		if err != nil {
			s.log.Error("cannot decrypt the SMTP password", "err", err)
			p = ""
		}
		n.Email.Password = p
	}
	return n
}

// saveNotifySettings stores the settings; an empty password keeps the old one.
func (s *Server) saveNotifySettings(ctx context.Context, n NotifySettings) error {
	if err := n.Validate(); err != nil {
		return err
	}
	if n.Email.Password == "" {
		n.Email.Password = s.notifySettings(ctx).Email.Password
	}
	if n.Email.Host == "" {
		n.Email.Password = ""
	}
	if n.Email.Password != "" {
		n.Email.Password = s.store.SealSecret("settings.email.password", n.Email.Password)
	}
	return s.store.SetSetting(ctx, settingNotify, n)
}

// headerSafe removes line breaks from header values.
func headerSafe(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }

// sendMail sends a plain-text message.
func sendMail(ctx context.Context, e EmailSettings, to []string, subject, body string) error {
	return sendMailFile(ctx, e, to, subject, body, "", nil)
}

// sendMailFile sends a plain-text message with an optional attachment.
func sendMailFile(ctx context.Context, e EmailSettings, to []string, subject, body, fileName string, file []byte) error {
	if !e.Configured() {
		return errors.New("email is not set up")
	}
	if len(to) == 0 {
		return errors.New("no recipients")
	}
	addr := net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
	d := net.Dialer{Timeout: 20 * time.Second}
	var conn net.Conn
	var err error
	tc := &tls.Config{ServerName: e.Host, MinVersion: tls.VersionTLS12}
	if e.Security == "tls" {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: tc}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("cannot reach the SMTP server: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	}
	c, err := smtp.NewClient(conn, e.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer c.Close()
	if e.Security == "starttls" {
		if err := c.StartTLS(tc); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if e.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", e.Username, e.Password, e.Host)); err != nil {
			return fmt.Errorf("SMTP sign-in: %w", err)
		}
	}
	from, _ := mail.ParseAddress(e.From)
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	for _, r := range to {
		a, err := mail.ParseAddress(r)
		if err != nil {
			return err
		}
		if err := c.Rcpt(a.Address); err != nil {
			return fmt.Errorf("recipient %s: %w", a.Address, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("From: " + headerSafe(e.From) + "\r\n")
	b.WriteString("To: " + headerSafe(strings.Join(to, ", ")) + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", headerSafe(subject)) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\nAuto-Submitted: auto-generated\r\n")
	text := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	if file == nil {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		b.WriteString(text)
	} else {
		boundary := "wz-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		b.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n")
		b.WriteString("--" + boundary + "\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + text + "\r\n")
		b.WriteString("--" + boundary + "\r\nContent-Type: text/csv; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n")
		b.WriteString("Content-Disposition: attachment; filename=\"" + headerSafe(strings.ReplaceAll(fileName, "\"", "")) + "\"\r\n\r\n")
		enc := base64.StdEncoding.EncodeToString(file)
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n--" + boundary + "--\r\n")
	}
	if _, err := w.Write([]byte(b.String())); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// parseRecipients checks a comma or space separated list of addresses.
func parseRecipients(s string) ([]string, error) {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		a, err := mail.ParseAddress(f)
		if err != nil {
			return nil, fmt.Errorf("not an email address: %s", f)
		}
		out = append(out, a.Address)
	}
	if len(out) == 0 {
		return nil, errors.New("enter at least one email address")
	}
	if len(out) > 50 {
		return nil, errors.New("at most 50 recipients")
	}
	return out, nil
}

// ---- events

// notifyEvent is one problem in a webhook body.
type notifyEvent struct {
	Event      string    `json:"event"` // problem, resolved, test
	ID         int64     `json:"id"`
	Severity   string    `json:"severity"`
	Title      string    `json:"title"`
	Detail     string    `json:"detail"`
	Device     string    `json:"device"`
	DeviceID   int64     `json:"device_id"`
	Address    string    `json:"address"`
	Check      string    `json:"check,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	ResolvedAt time.Time `json:"resolved_at,omitzero"`
	URL        string    `json:"url,omitempty"`
}

// builtinTriggers are the names of the triggers a new server starts with
// (migration 006); shown translated, while names people type stay as typed.
var builtinTriggers = []string{"Device down", "Check down", "High CPU load", "Memory almost full", "Disk almost full", "Disk full", "Packet loss",
	"Certificate expires soon", "Certificate expires", "High temperature"}

func toNotifyEvent(ev alert.Event, base string) notifyEvent {
	p := ev.Problem
	n := notifyEvent{Event: ev.Kind, ID: p.ID, Severity: p.Severity, Title: p.Title, Detail: p.Detail, Device: p.Device, DeviceID: p.DeviceID,
		Address: p.Address, Check: p.Check, StartedAt: p.StartedAt}
	if p.ResolvedAt != nil {
		n.ResolvedAt = *p.ResolvedAt
	}
	if base != "" {
		n.URL = base + "/devices/" + strconv.FormatInt(p.DeviceID, 10)
	}
	return n
}

// line is one problem as a sentence (chat and email).
func (n notifyEvent) line(lang string) string {
	state := map[string]string{store.SeverityCritical: "🔴 " + translate(lang, "CRITICAL"), store.SeverityWarning: "🟠 " + translate(lang, "WARNING"),
		store.SeverityInfo: "🔵 " + translate(lang, "INFO")}[n.Severity]
	if n.Event == alert.EventResolved {
		state = "✅ " + translate(lang, "RESOLVED")
	}
	s := fmt.Sprintf("%s: %s · %s (%s)", state, n.Device, translate(lang, n.Title), n.Address)
	if n.Detail != "" {
		s += " — " + n.Detail
	}
	return s
}

func severityName(sev string) string {
	return map[string]string{store.SeverityInfo: "Information", store.SeverityWarning: "Warning", store.SeverityCritical: "Critical"}[sev]
}

// mailText builds the subject and body of a notification email.
func mailText(lang, consoleURL string, events []notifyEvent) (string, string) {
	var subject string
	if len(events) == 1 {
		e := events[0]
		if e.Event == alert.EventResolved {
			subject = translate(lang, "[WatchZit] Resolved: %s · %s", e.Device, translate(lang, e.Title))
		} else {
			subject = translate(lang, "[WatchZit] %s: %s · %s", translate(lang, severityName(e.Severity)), e.Device, translate(lang, e.Title))
		}
	} else {
		subject = translate(lang, "[WatchZit] %d problems changed", len(events))
	}
	var b strings.Builder
	for _, e := range events {
		b.WriteString(e.line(lang) + "\n")
		b.WriteString("  " + translate(lang, "Started") + ": " + e.StartedAt.Local().Format("2006-01-02 15:04:05 MST") + "\n")
		if !e.ResolvedAt.IsZero() {
			b.WriteString("  " + translate(lang, "Resolved") + ": " + e.ResolvedAt.Local().Format("2006-01-02 15:04:05 MST") +
				" (" + durationText(lang, e.ResolvedAt.Sub(e.StartedAt)) + ")\n")
		}
		if e.URL != "" {
			b.WriteString("  " + e.URL + "\n")
		}
		b.WriteString("\n")
	}
	if consoleURL != "" {
		b.WriteString(translate(lang, "All problems:") + " " + consoleURL + "/problems\n")
	}
	b.WriteString("-- \nWatchZit\n")
	return subject, b.String()
}

// durationText writes a duration like "2 h 5 min".
func durationText(lang string, d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return translate(lang, "%d s", int(d.Seconds()))
	case d < time.Hour:
		return translate(lang, "%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return translate(lang, "%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	}
	return translate(lang, "%d days %d h", int(d.Hours())/24, int(d.Hours())%24)
}

// channelPayload builds the request body for a web channel.
func channelPayload(kind, lang string, events []notifyEvent) ([]byte, error) {
	switch kind {
	case store.ChannelSlack:
		var b strings.Builder
		for _, e := range events {
			b.WriteString(e.line(lang))
			if e.URL != "" {
				b.WriteString(" <" + e.URL + ">")
			}
			b.WriteString("\n")
		}
		return json.Marshal(map[string]string{"text": strings.TrimSpace(b.String())})
	case store.ChannelTeams:
		// A Teams workflow "when a webhook request is received" posts this card.
		var body []map[string]any
		for _, e := range events {
			text := e.line(lang)
			if e.URL != "" {
				text += " [" + translate(lang, "Open") + "](" + e.URL + ")"
			}
			body = append(body, map[string]any{"type": "TextBlock", "text": text, "wrap": true})
		}
		return json.Marshal(map[string]any{"type": "message", "attachments": []map[string]any{{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content":     map[string]any{"type": "AdaptiveCard", "version": "1.4", "$schema": "http://adaptivecards.io/schemas/adaptive-card.json", "body": body},
		}}})
	default:
		return json.Marshal(map[string]any{"source": "WatchZit", "events": events})
	}
}

var channelClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// deliver sends events to one channel.
func (s *Server) deliver(ctx context.Context, c *store.Channel, n NotifySettings, events []notifyEvent) error {
	lang := n.Lang
	if lang == "" {
		lang = "en"
	}
	if handled, err := s.deliverMore(ctx, c, lang, events); handled {
		return err
	}
	if c.Kind == store.ChannelEmail {
		to, err := parseRecipients(c.Target)
		if err != nil {
			return err
		}
		subject, body := mailText(lang, n.ConsoleURL, events)
		return sendMail(ctx, n.Email, to, subject, body)
	}
	u, err := s.store.OpenSecret("channel.url."+strconv.FormatInt(c.ID, 10), c.Target)
	if err != nil {
		return errors.New("the channel address cannot be read; enter it again")
	}
	body, err := channelPayload(c.Kind, lang, events)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "WatchZit/"+s.Version)
	if c.Kind == store.ChannelWebhook && c.Secret != "" {
		if secret, err := s.store.OpenSecret("channel.secret."+strconv.FormatInt(c.ID, 10), c.Secret); err == nil {
			m := hmac.New(sha256.New, []byte(secret))
			m.Write(body)
			req.Header.Set("X-WatchZit-Signature", "sha256="+hex.EncodeToString(m.Sum(nil)))
		}
	}
	resp, err := channelClient.Do(req) // #nosec G704 -- the address is set by an administrator
	if err != nil {
		return fmt.Errorf("cannot reach the channel: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the channel answered %s", resp.Status)
	}
	return nil
}

// Notify sends the events of an alert evaluation to every enabled channel
// that wants them (the alert engine calls it).
func (s *Server) Notify(ctx context.Context, events []alert.Event) {
	chans, err := s.store.Channels(ctx)
	if err != nil {
		s.log.Error("cannot load notification channels", "err", err)
		return
	}
	n := s.notifySettings(ctx)
	sent, _ := s.store.Sent(ctx, s.now())
	now := s.now()
	for _, c := range chans {
		if !c.Enabled {
			continue
		}
		var list []notifyEvent
		for _, e := range events {
			if store.SeverityRank(e.Problem.Severity) < store.SeverityRank(c.MinSeverity) || (e.Kind == alert.EventResolved && !c.OnResolve) {
				continue
			}
			if c.Escalates() {
				// new problems wait for the escalation round (delay, hours of
				// duty); "resolved" goes only where the problem was told
				if e.Kind != alert.EventResolved && (c.DelayMin > 0 || !c.OnDuty(now)) {
					continue
				}
				if _, told := sent[c.ID][e.Problem.ID]; e.Kind == alert.EventResolved && !told {
					continue
				}
			}
			list = append(list, toNotifyEvent(e, n.ConsoleURL))
		}
		s.send(c, n, list)
	}
}

// send delivers events to a channel in the background and records which
// problems it was told about.
func (s *Server) send(c *store.Channel, n NotifySettings, list []notifyEvent) {
	if len(list) == 0 {
		return
	}
	if len(list) > 50 {
		list = list[:50]
	}
	s.wg.Add(1)
	go func() { // #nosec G118 -- delivery outlives the evaluation; own timeout
		defer s.wg.Done()
		cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		err := s.deliver(cctx, c, n, list)
		_ = s.store.ChannelResult(cctx, c.ID, s.now(), err)
		if err != nil {
			s.log.Warn("notification failed", "channel", c.Name, "err", err)
			_ = s.store.Audit(cctx, "-", "server", "notify.failed", "channel:"+strconv.FormatInt(c.ID, 10), "notification to "+c.Name+" failed: "+err.Error())
			return
		}
		var told []int64
		for _, e := range list {
			if e.Event != alert.EventResolved && e.ID > 0 {
				told = append(told, e.ID)
			}
		}
		_ = s.store.MarkSent(cctx, c.ID, told, s.now())
	}()
}

// WaitNotifications waits for deliveries in flight (shutdown, tests).
func (s *Server) WaitNotifications() { s.wg.Wait() }
