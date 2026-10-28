package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// REST API (/api/v1): JSON for scripts and other systems. A request signs
// in with a personal API token (Authorization: Bearer wzat_…), never with
// the console's session cookie, so other web sites cannot use it through a
// browser. The token acts with its user's role; failed tokens count like
// failed sign-ins and block the address.

type ctxAPIKey struct{}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// apiHandler serves /api/.
func (s *Server) apiHandler() http.Handler {
	mux := http.NewServeMux()
	read := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.apiAuth(false, h)) }
	write := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.apiAuth(true, h)) }
	read("GET /api/v1/status", s.apiStatus)
	read("GET /api/v1/devices", s.apiDevices)
	read("GET /api/v1/devices/{id}", s.apiDevice)
	read("GET /api/v1/series/{id}", s.handleSeries)
	read("GET /api/v1/problems", s.apiProblems)
	write("POST /api/v1/problems/{id}/ack", s.apiProblemAck)
	read("GET /api/v1/maintenance", s.apiMaintenance)
	write("POST /api/v1/maintenance", s.apiMaintenanceCreate)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { apiError(w, http.StatusNotFound, "unknown API path") })
	return mux
}

// apiAuth checks the token, the address block and the role.
func (s *Server) apiAuth(needWrite bool, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ip := clientIP(r)
		if until := s.guard.blocked(r.Context(), ip); !until.IsZero() {
			w.Header().Set("Retry-After", strconv.Itoa(int(time.Until(until).Seconds())+1))
			apiError(w, http.StatusTooManyRequests, "too many failed attempts from this address")
			return
		}
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			apiError(w, http.StatusUnauthorized, "send an API token: Authorization: Bearer wzat_…")
			return
		}
		u, t, err := s.store.UserByAPIToken(r.Context(), strings.TrimSpace(tok), ip)
		if errors.Is(err, store.ErrBadToken) {
			s.guard.fail(r.Context(), ip, "", "API token")
			apiError(w, http.StatusUnauthorized, "invalid API token")
			return
		}
		if err != nil {
			s.log.Error("API sign-in", "err", err)
			apiError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if needWrite && !u.CanWrite() {
			apiError(w, http.StatusForbidden, "this token's user may only read")
			return
		}
		ctx := context.WithValue(r.Context(), ctxUser, u)
		ctx = context.WithValue(ctx, ctxAPIKey{}, t)
		next(w, r.WithContext(ctx))
	})
}

// apiAudit records an API change, naming the token.
func (s *Server) apiAudit(r *http.Request, action, target, format string, args ...any) {
	name := "-"
	if u := userFrom(r); u != nil {
		name = u.Username
	}
	if t, ok := r.Context().Value(ctxAPIKey{}).(*store.APIToken); ok {
		name += " (API token " + t.Name + ")"
	}
	s.auditAs(r, name, action, target, format, args...)
}

type apiDeviceJSON struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Address     string    `json:"address"`
	Kind        string    `json:"kind"`
	Group       string    `json:"group,omitempty"`
	Parent      int64     `json:"parent_id,omitempty"`
	Status      string    `json:"status"`
	StatusSince time.Time `json:"status_since"`
	Checks      int       `json:"checks"`
	ChecksDown  int       `json:"checks_down"`
}

func toAPIDevice(d *store.Device) apiDeviceJSON {
	return apiDeviceJSON{ID: d.ID, Name: d.Name, Address: d.Address, Kind: d.Kind, Group: d.Group, Parent: d.Parent, Status: d.Status,
		StatusSince: d.StatusSince, Checks: d.Checks, ChecksDown: d.ChecksDown}
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	dc, err := s.store.StatusCounts(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pc, err := s.store.ProblemCounts(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": s.Version, "devices": dc, "problems": pc, "time": s.now().UTC()})
}

func (s *Server) apiDevices(w http.ResponseWriter, r *http.Request) {
	f := store.DeviceFilter{Query: r.FormValue("q"), Status: r.FormValue("status")}
	f.GroupID, _ = strconv.ParseInt(r.FormValue("group"), 10, 64)
	ds, err := s.store.Devices(r.Context(), f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	out := []apiDeviceJSON{}
	for _, d := range ds {
		out = append(out, toAPIDevice(d))
	}
	writeJSON(w, http.StatusOK, out)
}

type apiSeriesJSON struct {
	ID     int32  `json:"id"`
	Metric string `json:"metric"`
	Unit   string `json:"unit,omitempty"`
}

type apiCheckJSON struct {
	ID          int64           `json:"id"`
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Status      string          `json:"status"`
	StatusSince time.Time       `json:"status_since"`
	LastRun     *time.Time      `json:"last_run,omitempty"`
	LastText    string          `json:"last_text"`
	IntervalS   int             `json:"interval_s"`
	Paused      bool            `json:"paused"`
	Series      []apiSeriesJSON `json:"series"`
}

func (s *Server) apiDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		apiError(w, http.StatusNotFound, "no such device")
		return
	}
	d, err := s.store.DeviceByID(r.Context(), id)
	if err != nil {
		apiError(w, http.StatusNotFound, "no such device")
		return
	}
	cs, err := s.store.DeviceChecks(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	out := struct {
		apiDeviceJSON
		CheckList []apiCheckJSON `json:"check_list"`
	}{apiDeviceJSON: toAPIDevice(d), CheckList: []apiCheckJSON{}}
	for _, c := range cs {
		cj := apiCheckJSON{ID: c.ID, Type: c.Type, Name: c.Name, Status: c.Status, StatusSince: c.StatusSince, LastRun: c.LastRun, LastText: c.LastText,
			IntervalS: c.IntervalS, Paused: c.Paused, Series: []apiSeriesJSON{}}
		for _, sr := range c.Series {
			cj.Series = append(cj.Series, apiSeriesJSON{ID: sr.ID, Metric: sr.Metric, Unit: sr.Unit})
		}
		out.CheckList = append(out.CheckList, cj)
	}
	writeJSON(w, http.StatusOK, out)
}

type apiProblemJSON struct {
	ID         int64      `json:"id"`
	Severity   string     `json:"severity"`
	Title      string     `json:"title"`
	Detail     string     `json:"detail"`
	DeviceID   int64      `json:"device_id"`
	Device     string     `json:"device"`
	CheckID    int64      `json:"check_id,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	Suppressed string     `json:"suppressed,omitempty"`
	AckAt      *time.Time `json:"ack_at,omitempty"`
	AckBy      string     `json:"ack_by,omitempty"`
	AckNote    string     `json:"ack_note,omitempty"`
}

func (s *Server) apiProblems(w http.ResponseWriter, r *http.Request) {
	f := store.ProblemFilter{Open: r.FormValue("view") != "history", Suppressed: r.FormValue("suppressed") == "1", Severity: r.FormValue("severity"),
		Since: s.now().AddDate(0, 0, -30), Limit: 1000}
	f.DeviceID, _ = strconv.ParseInt(r.FormValue("device"), 10, 64)
	if store.SeverityRank(f.Severity) < 0 {
		f.Severity = ""
	}
	ps, err := s.store.Problems(r.Context(), f)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	out := []apiProblemJSON{}
	for _, p := range ps {
		out = append(out, apiProblemJSON{ID: p.ID, Severity: p.Severity, Title: p.Title, Detail: p.Detail, DeviceID: p.DeviceID, Device: p.Device,
			CheckID: p.CheckID, StartedAt: p.StartedAt, ResolvedAt: p.ResolvedAt, Suppressed: p.Suppressed, AckAt: p.AckAt, AckBy: p.AckBy, AckNote: p.AckNote})
	}
	writeJSON(w, http.StatusOK, out)
}

// readJSON decodes a small JSON body.
func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return errors.New("the body is not the expected JSON: " + err.Error())
	}
	return nil
}

func (s *Server) apiProblemAck(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		apiError(w, http.StatusNotFound, "no such problem")
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if err := readJSON(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.store.ProblemByID(r.Context(), id)
	if err != nil {
		apiError(w, http.StatusNotFound, "no such problem")
		return
	}
	if err := s.store.AckProblem(r.Context(), id, userFrom(r).Username, in.Note); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusConflict, "the problem is already resolved")
			return
		}
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.apiAudit(r, "problem.ack", "device:"+strconv.FormatInt(p.DeviceID, 10), "problem #%d %q on %s acknowledged: %s", p.ID, p.Title, p.Device, in.Note)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "acknowledged": true})
}

type apiMaintenanceJSON struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	DeviceIDs []int64   `json:"device_ids"`
	GroupIDs  []int64   `json:"group_ids"`
	CreatedBy string    `json:"created_by,omitempty"`
}

func (s *Server) apiMaintenance(w http.ResponseWriter, r *http.Request) {
	ms, err := s.store.Maintenances(r.Context(), s.now().AddDate(0, 0, -7))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	out := []apiMaintenanceJSON{}
	for _, m := range ms {
		out = append(out, apiMaintenanceJSON{ID: m.ID, Name: m.Name, StartsAt: m.StartsAt, EndsAt: m.EndsAt, DeviceIDs: m.DeviceIDs, GroupIDs: m.GroupIDs, CreatedBy: m.CreatedBy})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiMaintenanceCreate(w http.ResponseWriter, r *http.Request) {
	var in apiMaintenanceJSON
	if err := readJSON(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.StartsAt.IsZero() {
		in.StartsAt = s.now()
	}
	if !in.EndsAt.After(s.now()) {
		apiError(w, http.StatusBadRequest, "ends_at must be in the future (RFC 3339, e.g. 2026-11-01T22:00:00+01:00)")
		return
	}
	id, err := s.store.CreateMaintenance(r.Context(), store.Maintenance{Name: in.Name, StartsAt: in.StartsAt, EndsAt: in.EndsAt,
		DeviceIDs: in.DeviceIDs, GroupIDs: in.GroupIDs, CreatedBy: userFrom(r).Username + " (API)"})
	if err != nil {
		apiError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.apiAudit(r, "maintenance.create", "maintenance:"+strconv.FormatInt(id, 10), "maintenance %q %s – %s, devices %v, groups %v", in.Name,
		in.StartsAt.Format(time.RFC3339), in.EndsAt.Format(time.RFC3339), in.DeviceIDs, in.GroupIDs)
	s.kickAlerts()
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

// ---- tokens on the account page

func (s *Server) handleAPITokenCreate(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id, tok, err := s.store.CreateAPIToken(r.Context(), u.ID, r.FormValue("name"), formInt(r, "days"))
	if err != nil {
		s.showAccount(w, r, http.StatusBadRequest, &accountData{}, storeErr(err))
		return
	}
	s.audit(r, "api.token.create", "user:"+strconv.FormatInt(u.ID, 10), "API token #%d %q created", id, r.FormValue("name"))
	s.showAccount(w, r, http.StatusOK, &accountData{NewToken: tok}, "")
}

func (s *Server) handleAPITokenDelete(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	name, err := s.store.DeleteAPIToken(r.Context(), u.ID, id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.audit(r, "api.token.delete", "user:"+strconv.FormatInt(u.ID, 10), "API token #%d %q revoked", id, name)
	s.redirect(w, r, "/account#api", "API token revoked.")
}
