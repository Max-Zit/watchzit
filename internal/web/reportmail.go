package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The availability report by email: every Monday (the last 7 days) or on
// the 1st of a month (the last month), at 07:00 server time, with the CSV
// attached.

const settingReportMail = "report_mail"

// ReportMail is when and to whom the report goes.
type ReportMail struct {
	Every   string `json:"every"` // "" (off), week, month
	To      string `json:"to"`
	GroupID int64  `json:"group,omitempty"`
	Last    string `json:"last,omitempty"` // the period last sent, e.g. 2026-W44 or 2026-10
}

// due returns the key and period of the report due at now ("" if none).
func (m ReportMail) due(now time.Time) (key, period string) {
	now = now.Local()
	if now.Hour() < 7 {
		return "", ""
	}
	switch m.Every {
	case "week":
		if now.Weekday() != time.Monday {
			return "", ""
		}
		y, w := now.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w), "7d"
	case "month":
		if now.Day() != 1 {
			return "", ""
		}
		return now.Format("2006-01"), "lastmonth"
	}
	return "", ""
}

// RunReportMail sends the report when it is due (checked every 10 minutes).
func (s *Server) RunReportMail(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.SendDueReport(ctx); err != nil {
				s.log.Error("report email", "err", err)
			}
		}
	}
}

// SendDueReport sends the report if one is due and not sent yet.
func (s *Server) SendDueReport(ctx context.Context) error {
	var m ReportMail
	if err := s.store.GetSetting(ctx, settingReportMail, &m); err != nil || m.Every == "" {
		return nil
	}
	key, period := m.due(s.now())
	if key == "" || key == m.Last {
		return nil
	}
	if err := s.mailReport(ctx, m, period); err != nil {
		_ = s.store.Audit(ctx, "-", "server", "report.mail.failed", "settings", "availability report email failed: "+err.Error())
		return err
	}
	m.Last = key
	_ = s.store.Audit(ctx, "-", "server", "report.mail", "settings", "availability report "+key+" sent to "+m.To)
	return s.store.SetSetting(ctx, settingReportMail, m)
}

// mailReport builds and sends the report of a period.
func (s *Server) mailReport(ctx context.Context, m ReportMail, period string) error {
	n := s.notifySettings(ctx)
	to, err := parseRecipients(m.To)
	if err != nil {
		return err
	}
	d, err := s.buildReport(ctx, period, m.GroupID)
	if err != nil {
		return err
	}
	lang := n.Lang
	if lang == "" {
		lang = "en"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s – %s\n\n", translate(lang, "Availability report"), d.From.Format("2006-01-02"), d.To.Format("2006-01-02"))
	fmt.Fprintf(&b, "%s: %.3f%%\n%s: %s\n%s: %d\n%s: %d\n", translate(lang, "Average availability"), d.Avg, translate(lang, "Downtime"), durationText(lang, d.Down), translate(lang, "Problems"), d.Problems, translate(lang, "Devices"), len(d.Rows))
	if len(d.SLA) > 0 {
		fmt.Fprintf(&b, "\n%s\n", translate(lang, "SLA by group"))
		for _, g := range d.SLA {
			state := translate(lang, "met")
			if !g.Met {
				state = translate(lang, "missed")
			}
			fmt.Fprintf(&b, "  %s: %.3f%% / %.3f%% — %s\n", g.Name, g.Pct, g.Target, state)
		}
	}
	rows := append([]reportRow(nil), d.Rows...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Pct < rows[j].Pct })
	if len(rows) > 0 && rows[0].Pct < 100 {
		fmt.Fprintf(&b, "\n%s\n", translate(lang, "Lowest availability"))
		for i, r := range rows {
			if i == 15 || r.Pct >= 100 {
				break
			}
			fmt.Fprintf(&b, "  %s (%s): %.3f%%, %s\n", r.Name, r.Address, r.Pct, durationText(lang, r.Down))
		}
	}
	if n.ConsoleURL != "" {
		fmt.Fprintf(&b, "\n%s/reports?period=%s&group=%d\n", n.ConsoleURL, d.Period, d.GroupID)
	}
	b.WriteString("\n-- \nWatchZit\n")
	var csv bytes.Buffer
	writeReportCSV(&csv, d)
	return sendMailFile(ctx, n.Email, to, "[WatchZit] "+translate(lang, "Availability report")+" "+d.From.Format("2006-01-02")+" – "+d.To.Format("2006-01-02"), b.String(),
		"watchzit-availability-"+d.From.Format("20060102")+"-"+d.To.Format("20060102")+".csv", csv.Bytes())
}

// handleReportMail saves the email schedule, or sends the report now.
func (s *Server) handleReportMail(w http.ResponseWriter, r *http.Request) {
	const back = "/reports"
	m := ReportMail{Every: r.FormValue("every"), To: strings.TrimSpace(r.FormValue("to"))}
	m.GroupID, _ = strconv.ParseInt(r.FormValue("group"), 10, 64)
	if m.Every != "week" && m.Every != "month" {
		m.Every = ""
	}
	if m.Every != "" || r.FormValue("do") == "now" {
		if _, err := parseRecipients(m.To); err != nil {
			s.redirectErr(w, r, back, err.Error())
			return
		}
	}
	if r.FormValue("do") == "now" {
		period := "7d"
		if m.Every == "month" {
			period = "lastmonth"
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if err := s.mailReport(ctx, m, period); err != nil {
			s.redirectErr(w, r, back, err.Error())
			return
		}
		s.audit(r, "report.mail", "settings", "availability report sent to %s", m.To)
		s.redirect(w, r, back, "Report sent.")
		return
	}
	var old ReportMail
	_ = s.store.GetSetting(r.Context(), settingReportMail, &old)
	m.Last = old.Last
	if err := s.store.SetSetting(r.Context(), settingReportMail, m); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "settings.report.mail", "settings", "availability report email: %q to %s", m.Every, m.To)
	s.redirect(w, r, back, "Saved.")
}
