package web

import (
	"encoding/csv"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// Availability reports: per device over a period, from "device down"
// problems; time in maintenance is planned and does not count against it.

type reportRow struct {
	store.AvailabilityRow
	Pct     float64 // availability in percent
	PingAvg float64 // average ping time in ms (-1 = none)
}

type reportData struct {
	Period   string
	Periods  []string
	GroupID  int64
	Groups   []store.Group
	From, To time.Time
	Rows     []reportRow
	Avg      float64
	Down     time.Duration
	Problems int
	Query    string
}

var reportPeriods = []string{"7d", "30d", "90d", "month", "lastmonth"}

// reportRange turns a period name into its start and end.
func reportRange(period string, now time.Time) (time.Time, time.Time) {
	now = now.Local()
	switch period {
	case "7d":
		return now.AddDate(0, 0, -7), now
	case "90d":
		return now.AddDate(0, 0, -90), now
	case "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()), now
	case "lastmonth":
		end := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		return end.AddDate(0, -1, 0), end
	}
	return now.AddDate(0, 0, -30), now
}

func (s *Server) report(r *http.Request) (*reportData, error) {
	d := &reportData{Period: r.FormValue("period"), Periods: reportPeriods}
	if d.Period == "" {
		d.Period = "30d"
	}
	d.GroupID, _ = strconv.ParseInt(r.FormValue("group"), 10, 64)
	d.From, d.To = reportRange(d.Period, s.now())
	d.Query = "period=" + d.Period + "&group=" + strconv.FormatInt(d.GroupID, 10)
	var err error
	if d.Groups, err = s.store.Groups(r.Context()); err != nil {
		return nil, err
	}
	rows, err := s.store.Availability(r.Context(), d.From, d.To, d.GroupID)
	if err != nil {
		return nil, err
	}
	var sum float64
	for _, a := range rows {
		start := d.From
		if a.CreatedAt.After(start) {
			start = a.CreatedAt
		}
		span := d.To.Sub(start) - a.Planned
		row := reportRow{AvailabilityRow: a, Pct: 100, PingAvg: -1}
		if span > 0 {
			row.Pct = math.Max(0, math.Round((1-float64(a.Down)/float64(span))*100000)/1000)
		}
		if a.PingRTT != 0 {
			if res, err := s.tsdb.Query(r.Context(), a.PingRTT, d.From, d.To, 300); err == nil {
				var tot, n float64
				for _, p := range res.Points {
					if !math.IsNaN(p.Avg) && p.N > 0 {
						tot += p.Avg * p.N
						n += p.N
					}
				}
				if n > 0 {
					row.PingAvg = math.Round(tot/n*100) / 100
				}
			}
		}
		sum += row.Pct
		d.Down += a.Down
		d.Problems += a.Problems
		d.Rows = append(d.Rows, row)
	}
	if len(d.Rows) > 0 {
		d.Avg = math.Round(sum/float64(len(d.Rows))*1000) / 1000
	}
	return d, nil
}

func (s *Server) handleReports(w http.ResponseWriter, r *http.Request) {
	d, err := s.report(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "reports", &Page{Title: "Reports", Nav: "reports", Data: d})
}

// handleReportCSV sends the same report as CSV (for spreadsheets).
func (s *Server) handleReportCSV(w http.ResponseWriter, r *http.Request) {
	d, err := s.report(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="watchzit-availability-`+d.From.Format("20060102")+"-"+d.To.Format("20060102")+`.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"device", "group", "address", "availability_pct", "downtime_s", "maintenance_s", "problems", "ping_avg_ms", "from", "to"})
	for _, x := range d.Rows {
		ping := ""
		if x.PingAvg >= 0 {
			ping = strconv.FormatFloat(x.PingAvg, 'f', 2, 64)
		}
		_ = cw.Write([]string{csvSafe(x.Name), csvSafe(x.Group), csvSafe(x.Address), strconv.FormatFloat(x.Pct, 'f', 3, 64),
			strconv.FormatInt(int64(x.Down.Seconds()), 10), strconv.FormatInt(int64(x.Planned.Seconds()), 10), strconv.Itoa(x.Problems), ping,
			d.From.Format(time.RFC3339), d.To.Format(time.RFC3339)})
	}
	cw.Flush()
}

// csvSafe keeps spreadsheets from reading a cell as a formula.
func csvSafe(s string) string {
	if s != "" && (s[0] == '=' || s[0] == '+' || s[0] == '-' || s[0] == '@' || s[0] == '\t' || s[0] == '\r') {
		return "'" + s
	}
	return s
}
