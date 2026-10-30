package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/max-zit/watchzit/internal/alert"
	"github.com/max-zit/watchzit/internal/store"
)

// More notification channels: Discord, Telegram, ntfy, PagerDuty, SMS
// through Twilio and SMS through any gateway with an HTTP address.
//
// What each keeps (target and secret are sealed):
//
//	discord    target: webhook address
//	ntfy       target: topic address;      secret: access token (optional)
//	telegram   target: chat ID;            secret: bot token
//	pagerduty  target: integration key (Events API v2)
//	twilio     target: phone numbers;      secret: "SID|token|from"
//	smsgw      target: address with {text} (GET; the gateway's own parameters)

// API addresses (tests replace them).
var (
	telegramAPI  = "https://api.telegram.org"
	pagerDutyAPI = "https://events.pagerduty.com/v2/enqueue"
	twilioAPI    = "https://api.twilio.com"
)

// moreChannels are the kinds of this file.
var moreChannels = []string{store.ChannelDiscord, store.ChannelTelegram, store.ChannelNtfy, store.ChannelPagerDuty, store.ChannelTwilio, store.ChannelSMSGateway}

var (
	chatIDRe  = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z0-9_]{5,32})$`)
	botRe     = regexp.MustCompile(`^[0-9]{5,15}:[A-Za-z0-9_-]{20,64}$`)
	pdKeyRe   = regexp.MustCompile(`^[A-Za-z0-9]{32}$`)
	phoneRe   = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)
	twSIDRe   = regexp.MustCompile(`^AC[0-9a-fA-F]{32}$`)
	smsTextRe = regexp.MustCompile(`\{text\}`)
)

// checkTarget checks what a channel of kind is sent to.
func checkTarget(kind, target string) error {
	switch kind {
	case store.ChannelEmail:
		_, err := parseRecipients(target)
		return err
	case store.ChannelTelegram:
		if !chatIDRe.MatchString(target) {
			return uiErr("enter the Telegram chat ID (a number, or @channelname)")
		}
	case store.ChannelPagerDuty:
		if !pdKeyRe.MatchString(target) {
			return uiErr("enter the 32-character integration key of a PagerDuty service (Events API v2)")
		}
	case store.ChannelTwilio:
		if _, err := phoneList(target); err != nil {
			return err
		}
	case store.ChannelSMSGateway:
		if err := checkChannelURL(strings.ReplaceAll(target, "{text}", "x")); err != nil || !smsTextRe.MatchString(target) {
			return uiErr("enter the gateway address with {text} where the message goes")
		}
	default:
		return checkChannelURL(target)
	}
	return nil
}

// phoneList reads phone numbers in international form, separated by commas.
func phoneList(s string) ([]string, error) {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		if !phoneRe.MatchString(p) {
			return nil, uiErr("enter phone numbers in international form, e.g. +381601234567, separated by commas")
		}
		out = append(out, p)
	}
	if len(out) == 0 || len(out) > 20 {
		return nil, uiErr("enter one to twenty phone numbers")
	}
	return out, nil
}

// channelSecret builds the secret of a channel from its form, "" when the
// form leaves it as it is.
func channelSecret(kind string, r *http.Request) (string, error) {
	switch kind {
	case store.ChannelTelegram:
		t := strings.TrimSpace(r.FormValue("bot_token"))
		if t != "" && !botRe.MatchString(t) {
			return "", uiErr("enter the bot token as BotFather shows it (123456:ABC…)")
		}
		return t, nil
	case store.ChannelTwilio:
		sid, tok, from := strings.TrimSpace(r.FormValue("tw_sid")), strings.TrimSpace(r.FormValue("tw_token")), strings.TrimSpace(r.FormValue("tw_from"))
		if sid == "" && tok == "" && from == "" {
			return "", nil
		}
		if !twSIDRe.MatchString(sid) || len(tok) < 16 || len(tok) > 64 || !(phoneRe.MatchString(from) || regexp.MustCompile(`^MG[0-9a-fA-F]{32}$`).MatchString(from)) {
			return "", uiErr("enter the Twilio account SID, auth token and the sender number (or messaging service SID)")
		}
		return sid + "|" + tok + "|" + from, nil
	case store.ChannelWebhook, store.ChannelNtfy:
		return r.FormValue("secret"), nil
	}
	return "", nil
}

// needsSecret: kinds that cannot work without one.
func needsSecret(kind string) bool {
	return kind == store.ChannelTelegram || kind == store.ChannelTwilio
}

// eventsText is the events as lines of text.
func eventsText(lang string, events []notifyEvent, links bool) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString(e.line(lang))
		if links && e.URL != "" {
			b.WriteString("\n" + e.URL)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// post sends one request and checks the answer.
func (s *Server) post(req *http.Request) error {
	req.Header.Set("User-Agent", "WatchZit/"+s.Version)
	resp, err := channelClient.Do(req) // #nosec G704 -- addresses set by an administrator or fixed services
	if err != nil {
		return fmt.Errorf("cannot reach the channel: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("the channel answered %s %s", resp.Status, msg)
	}
	return nil
}

func jsonRequest(ctx context.Context, u string, v any) (*http.Request, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, err
}

// deliverMore sends to the channels of this file; handled reports whether
// kind is one of them.
func (s *Server) deliverMore(ctx context.Context, c *store.Channel, lang string, events []notifyEvent) (handled bool, err error) {
	known := false
	for _, k := range moreChannels {
		known = known || k == c.Kind
	}
	if !known {
		return false, nil
	}
	id := strconv.FormatInt(c.ID, 10)
	target, err := s.store.OpenSecret("channel.url."+id, c.Target)
	if err != nil {
		return true, errors.New("the channel address cannot be read; enter it again")
	}
	secret := ""
	if c.Secret != "" {
		if secret, err = s.store.OpenSecret("channel.secret."+id, c.Secret); err != nil {
			return true, errors.New("the channel key cannot be read; enter it again")
		}
	}
	switch c.Kind {
	case store.ChannelDiscord:
		req, err := jsonRequest(ctx, target, map[string]any{"username": "WatchZit", "content": cut(eventsText(lang, events, true), 1900)})
		if err != nil {
			return true, err
		}
		return true, s.post(req)
	case store.ChannelNtfy:
		title, prio, tags := "WatchZit", "default", "warning"
		top := events[0]
		switch {
		case top.Event == alert.EventResolved:
			prio, tags = "low", "white_check_mark"
		case top.Severity == store.SeverityCritical:
			prio, tags = "urgent", "rotating_light"
		case top.Severity == store.SeverityWarning:
			prio = "high"
		}
		if len(events) == 1 {
			title = top.Device + " · " + translate(lang, top.Title)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(cut(eventsText(lang, events, false), 4000)))
		if err != nil {
			return true, err
		}
		req.Header.Set("Title", headerSafe(cut(title, 200)))
		req.Header.Set("Priority", prio)
		req.Header.Set("Tags", tags)
		if top.URL != "" {
			req.Header.Set("Click", top.URL)
		}
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		return true, s.post(req)
	case store.ChannelTelegram:
		req, err := jsonRequest(ctx, telegramAPI+"/bot"+secret+"/sendMessage", map[string]any{"chat_id": target, "text": cut(eventsText(lang, events, true), 4000), "disable_web_page_preview": true})
		if err != nil {
			return true, err
		}
		if err := s.post(req); err != nil {
			// the address holds the bot token: never show it
			return true, errors.New(strings.ReplaceAll(err.Error(), secret, "…"))
		}
		return true, nil
	case store.ChannelPagerDuty:
		for _, e := range events {
			action := "trigger"
			if e.Event == alert.EventResolved {
				action = "resolve"
			}
			sev := map[string]string{store.SeverityCritical: "critical", store.SeverityWarning: "warning", store.SeverityInfo: "info"}[e.Severity]
			if sev == "" {
				sev = "warning"
			}
			ev := map[string]any{"routing_key": target, "event_action": action, "dedup_key": "watchzit-problem-" + strconv.FormatInt(e.ID, 10),
				"payload": map[string]any{"summary": cut(e.line(lang), 1000), "source": e.Device + " (" + e.Address + ")", "severity": sev,
					"component": e.Check, "class": translate(lang, e.Title), "custom_details": map[string]any{"detail": e.Detail, "started_at": e.StartedAt}}}
			if e.URL != "" {
				ev["links"] = []map[string]string{{"href": e.URL, "text": "WatchZit"}}
			}
			req, err := jsonRequest(ctx, pagerDutyAPI, ev)
			if err != nil {
				return true, err
			}
			if err := s.post(req); err != nil {
				return true, err
			}
		}
		return true, nil
	case store.ChannelTwilio:
		parts := strings.SplitN(secret, "|", 3)
		if len(parts) != 3 {
			return true, errors.New("the Twilio account is not set")
		}
		to, err := phoneList(target)
		if err != nil {
			return true, err
		}
		text := cut(eventsText(lang, events, false), 600)
		for _, n := range to {
			form := url.Values{"To": {n}, "Body": {text}}
			if strings.HasPrefix(parts[2], "MG") {
				form.Set("MessagingServiceSid", parts[2])
			} else {
				form.Set("From", parts[2])
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, twilioAPI+"/2010-04-01/Accounts/"+parts[0]+"/Messages.json", strings.NewReader(form.Encode()))
			if err != nil {
				return true, err
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.SetBasicAuth(parts[0], parts[1])
			if err := s.post(req); err != nil {
				return true, err
			}
		}
		return true, nil
	case store.ChannelSMSGateway:
		u := strings.ReplaceAll(target, "{text}", url.QueryEscape(cut(eventsText(lang, events, false), 300)))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return true, err
		}
		return true, s.post(req)
	}
	return true, nil
}
