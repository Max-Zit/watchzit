package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/datasource"
	"github.com/max-zit/watchzit/internal/store"
)

// ---- Settings → Data sources

// dsKindNames name the kinds of data sources.
var dsKindNames = map[string]string{datasource.Postgres: "PostgreSQL", datasource.MySQL: "MySQL / MariaDB", datasource.MSSQL: "Microsoft SQL Server",
	datasource.Prometheus: "Prometheus", datasource.InfluxDB: "InfluxDB (InfluxQL)", datasource.JSON: "JSON over HTTP"}

type dsKind struct{ ID, Name string }

type datasourcesData struct {
	Sources []*store.DataSource
	Kinds   []dsKind
	Edit    *store.DataSource
	In      store.DataSourceInput
	Tested  bool // "Test connection" was pressed
	TestOK  bool
	TestErr string
}

func dsKinds() []dsKind {
	out := make([]dsKind, len(datasource.Kinds))
	for i, k := range datasource.Kinds {
		out[i] = dsKind{k, dsKindNames[k]}
	}
	return out
}

func (s *Server) showDataSources(w http.ResponseWriter, r *http.Request, status int, d *datasourcesData, errMsg string) {
	var err error
	if d.Sources, err = s.store.DataSources(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Kinds = dsKinds()
	p := &Page{Title: "Data sources", Nav: "settings", Data: d}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "datasources", p)
}

func (s *Server) handleDataSources(w http.ResponseWriter, r *http.Request) {
	s.showDataSources(w, r, http.StatusOK, &datasourcesData{In: store.DataSourceInput{Kind: datasource.Postgres}}, "")
}

func dsInput(r *http.Request) store.DataSourceInput {
	return store.DataSourceInput{Name: r.FormValue("name"), Kind: r.FormValue("kind"), Address: r.FormValue("address"), Database: r.FormValue("database"),
		User: r.FormValue("user"), Secret: r.FormValue("secret"), TLS: r.FormValue("tls"), ClearSecret: r.FormValue("clear") == "1"}
}

// testDataSource tries the form's settings (do=test) without saving them.
func (s *Server) testDataSource(w http.ResponseWriter, r *http.Request, d *datasourcesData) {
	c, err := s.store.ConfigOf(r.Context(), idOf(d.Edit), d.In)
	if err == nil {
		err = datasource.Test(r.Context(), c)
	}
	d.In.Secret = ""
	d.Tested = true
	if err != nil {
		d.TestErr = err.Error()
	} else {
		d.TestOK = true
	}
	s.showDataSources(w, r, http.StatusOK, d, "")
}

func idOf(d *store.DataSource) int64 {
	if d == nil {
		return 0
	}
	return d.ID
}

func (s *Server) handleDataSourceCreate(w http.ResponseWriter, r *http.Request) {
	d := &datasourcesData{In: dsInput(r)}
	if r.FormValue("do") == "test" {
		s.testDataSource(w, r, d)
		return
	}
	id, err := s.store.CreateDataSource(r.Context(), d.In)
	if err != nil {
		d.In.Secret = ""
		s.showDataSources(w, r, http.StatusBadRequest, d, storeErr(err))
		return
	}
	s.audit(r, "datasource.create", "datasource:"+strconv.FormatInt(id, 10), "data source %q (%s) added", strings.TrimSpace(d.In.Name), d.In.Kind)
	s.redirect(w, r, "/settings/datasources", "Data source added.")
}

func (s *Server) loadDataSource(w http.ResponseWriter, r *http.Request) *store.DataSource {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return nil
	}
	d, err := s.store.DataSourceByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	return d
}

func (s *Server) handleDataSource(w http.ResponseWriter, r *http.Request) {
	if d := s.loadDataSource(w, r); d != nil {
		s.showDataSources(w, r, http.StatusOK, &datasourcesData{Edit: d, In: store.DataSourceInput{Name: d.Name, Kind: d.Kind, Address: d.Address, Database: d.Database, User: d.User, TLS: d.TLS}}, "")
	}
}

func (s *Server) handleDataSourceUpdate(w http.ResponseWriter, r *http.Request) {
	ds := s.loadDataSource(w, r)
	if ds == nil {
		return
	}
	d := &datasourcesData{Edit: ds, In: dsInput(r)}
	if r.FormValue("do") == "test" {
		s.testDataSource(w, r, d)
		return
	}
	if err := s.store.UpdateDataSource(r.Context(), ds.ID, d.In); err != nil {
		d.In.Secret = ""
		s.showDataSources(w, r, http.StatusBadRequest, d, storeErr(err))
		return
	}
	s.queries.forget()
	s.audit(r, "datasource.update", "datasource:"+strconv.FormatInt(ds.ID, 10), "data source %q saved", strings.TrimSpace(d.In.Name))
	s.redirect(w, r, "/settings/datasources", "Saved.")
}

func (s *Server) handleDataSourceDelete(w http.ResponseWriter, r *http.Request) {
	ds := s.loadDataSource(w, r)
	if ds == nil {
		return
	}
	if err := s.store.DeleteDataSource(r.Context(), ds.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.queries.forget()
	s.audit(r, "datasource.delete", "datasource:"+strconv.FormatInt(ds.ID, 10), "data source %q deleted", ds.Name)
	s.redirect(w, r, "/settings/datasources", "Data source deleted.")
}

// ---- running queries

// queryCache keeps answers for a short while, so a dashboard on several
// screens does not ask the database again for each of them.
type queryCache struct {
	mu sync.Mutex
	m  map[string]cachedQuery
}

type cachedQuery struct {
	at  time.Time
	ss  []datasource.Series
	err error
}

const queryTTL = 20 * time.Second

func (c *queryCache) get(key string, now time.Time) (cachedQuery, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return v, ok && now.Sub(v.at) < queryTTL
}

func (c *queryCache) put(key string, v cachedQuery) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 500 {
		c.m = map[string]cachedQuery{}
	}
	c.m[key] = v
}

func (c *queryCache) forget() {
	c.mu.Lock()
	c.m = nil
	c.mu.Unlock()
}

// queryRange is the time range and width a graph asks for (as /series).
func (s *Server) queryRange(r *http.Request) (from, to time.Time, points int) {
	to = s.now().Add(time.Second)
	from = to.Add(-rangeDuration(r.FormValue("range")))
	if f, err := strconv.ParseInt(r.FormValue("from"), 10, 64); err == nil {
		if t, err := strconv.ParseInt(r.FormValue("to"), 10, 64); err == nil && t > f && t-f <= 5*366*86400 {
			from, to = time.Unix(f, 0), time.Unix(t, 0)
		}
	}
	points = formInt(r, "points")
	if points < 50 || points > 3000 {
		points = 600
	}
	return from, to, points
}

// runQuery runs a graph query and gives its series as /series does.
func (s *Server) runQuery(ctx context.Context, q store.GraphQuery, from, to time.Time, points int) ([]seriesJSON, error) {
	// the newest minute is asked again; older ranges stay cached
	key := strconv.FormatInt(q.Source, 10) + "\x00" + q.Query + "\x00" + strconv.FormatInt(from.Unix()/10, 10) + "\x00" + strconv.FormatInt(to.Unix()/10, 10) + "\x00" + strconv.Itoa(points)
	v, ok := s.queries.get(key, time.Now())
	if !ok {
		c, err := s.store.DataSourceConfig(ctx, q.Source)
		if errors.Is(err, store.ErrNotFound) {
			return nil, errors.New("the data source was deleted")
		}
		if err != nil {
			return nil, err
		}
		v.ss, v.err = datasource.Query(ctx, c, datasource.Request{Query: q.Query, From: from, To: to, Points: points})
		if ctx.Err() == nil {
			v.at = time.Now()
			s.queries.put(key, v)
		}
	}
	ss := v.ss
	out := []seriesJSON{}
	for _, sr := range ss {
		name := sr.Name
		if q.Label != "" {
			name = q.Label
			if len(ss) > 1 {
				name += " " + sr.Name
			}
		}
		out = append(out, seriesJSON{Label: name, Unit: q.Unit, From: from.Unix(), To: to.Unix(), T: sr.T, Avg: sr.V, Min: []*float64{}, Max: []*float64{}})
	}
	return out, v.err
}

type queryAnswer struct {
	Series []seriesJSON `json:"series"`
	Error  string       `json:"error,omitempty"`
}

func (s *Server) answerQuery(w http.ResponseWriter, r *http.Request, q store.GraphQuery) {
	from, to, points := s.queryRange(r)
	out, err := s.runQuery(r.Context(), q, from, to, points)
	a := queryAnswer{Series: out}
	if a.Series == nil {
		a.Series = []seriesJSON{}
	}
	if err != nil {
		a.Error = err.Error()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, a)
}

// handlePanelQuery answers a query of an arranged device graph.
func (s *Server) handlePanelQuery(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	n, err := strconv.Atoi(r.PathValue("n"))
	if !ok || err != nil {
		s.notFound(w, r)
		return
	}
	l, err := s.store.DeviceGraphLayout(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if l != nil {
		for _, p := range l.Panels {
			if p.ID == r.PathValue("panel") && n >= 0 && n < len(p.Queries) {
				s.answerQuery(w, r, p.Queries[n])
				return
			}
		}
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// handleWidgetQuery answers the query of a dashboard widget.
func (s *Server) handleWidgetQuery(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	n, err := strconv.Atoi(r.PathValue("n"))
	if !ok || err != nil {
		s.notFound(w, r)
		return
	}
	d, err := s.store.DashboardByID(r.Context(), id)
	if err != nil || n < 0 || n >= len(d.Widgets) || d.Widgets[n].Query == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	s.answerQuery(w, r, *d.Widgets[n].Query)
}

// handleQueryTry runs a query typed in an editor (operators): it shows the
// series it gives, or the database's error.
func (s *Server) handleQueryTry(w http.ResponseWriter, r *http.Request) {
	var q store.GraphQuery
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&q); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	from, to, points := s.queryRange(r)
	out, err := s.runQuery(r.Context(), q, from, to, points)
	type sum struct {
		Label  string `json:"label"`
		Points int    `json:"points"`
	}
	res := struct {
		Series []sum  `json:"series"`
		Error  string `json:"error,omitempty"`
	}{Series: []sum{}}
	for _, sr := range out {
		res.Series = append(res.Series, sum{sr.Label, len(sr.T)})
	}
	if err != nil {
		res.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, res)
}
