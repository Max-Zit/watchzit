package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/testdb"
)

// TestDataSources: an administrator adds a PostgreSQL and a Prometheus data
// source, tests them, and operators show their queries on device graphs and
// dashboards; queries run read-only, secrets stay sealed, viewers cannot
// add sources or run queries of their own.
func TestDataSources(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "srv", "address", "10.0.0.1", "ping", "1")

	pg, err := url.Parse(testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	pass, _ := pg.User.Password()
	db := strings.TrimPrefix(pg.Path, "/")

	// a wrong password: the test says so, nothing is saved
	r := c.post("/settings/datasources", "name", "ERP", "kind", "postgres", "address", pg.Host, "database", db, "user", pg.User.Username(), "secret", "wrong", "tls", "disable", "do", "test")
	if !strings.Contains(r.body, "The data source does not answer") || strings.Contains(r.body, `value="wrong"`) {
		t.Fatalf("test with a wrong password: %d %s", r.code, r.body)
	}
	r = c.post("/settings/datasources", "name", "ERP", "kind", "postgres", "address", pg.Host, "database", db, "user", pg.User.Username(), "secret", pass, "tls", "disable", "do", "test")
	if !strings.Contains(r.body, "The data source answers.") {
		t.Fatalf("test: %s", r.body)
	}
	if r := c.post("/settings/datasources", "name", "ERP", "kind", "postgres", "address", pg.Host, "database", db, "user", pg.User.Username(), "secret", pass, "tls", "disable"); r.code != http.StatusSeeOther {
		t.Fatalf("add: %d %s", r.code, r.body)
	}
	srcs, _ := e.st.DataSources(ctx)
	if len(srcs) != 1 || !srcs[0].HasSecret {
		t.Fatalf("sources: %+v", srcs)
	}
	pgID := srcs[0].ID
	var sealed string
	if err := e.st.Pool().QueryRow(ctx, `SELECT secret FROM datasources WHERE id=$1`, pgID).Scan(&sealed); err != nil || !strings.HasPrefix(sealed, "v1:") || strings.Contains(sealed, pass) {
		t.Fatalf("secret not sealed: %v", err)
	}
	// saving without a password keeps it
	if r := c.post("/settings/datasources/"+strconv.FormatInt(pgID, 10), "name", "ERP", "kind", "postgres", "address", pg.Host, "database", db, "user", pg.User.Username(), "tls", "disable"); r.code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", r.code, r.body)
	}
	if cfg, err := e.st.DataSourceConfig(ctx, pgID); err != nil || cfg.Secret != pass {
		t.Fatal("the saved password was not kept")
	}
	// refused: an unknown kind, an HTTP kind without http://
	if r := c.post("/settings/datasources", "name", "X", "kind", "oracle", "address", "h"); r.code != http.StatusBadRequest {
		t.Fatalf("unknown kind: %d", r.code)
	}
	if r := c.post("/settings/datasources", "name", "X", "kind", "prometheus", "address", "prom:9090"); r.code != http.StatusBadRequest {
		t.Fatalf("address without scheme: %d", r.code)
	}

	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up","job":"node"},"values":[[1793095200,"1"],[1793095260,"1"]]}]}}`))
	}))
	defer prom.Close()
	c.post("/settings/datasources", "name", "Prom", "kind", "prometheus", "address", prom.URL)
	srcs, _ = e.st.DataSources(ctx)
	if len(srcs) != 2 {
		t.Fatalf("sources: %d", len(srcs))
	}
	promID := srcs[1].ID

	// trying queries: series and points, or the database's error
	series := `SELECT t AS time, extract(minute from t) AS minute, 1 AS one FROM generate_series($__timeFrom, $__timeTo, interval '5 minutes') t`
	r = c.postJSON("/datasources/try?range=1h", store.GraphQuery{Source: pgID, Query: series})
	if !strings.Contains(r.body, `"label":"minute","points":13`) || !strings.Contains(r.body, `"label":"one"`) {
		t.Fatalf("try: %s", r.body)
	}
	r = c.postJSON("/datasources/try", store.GraphQuery{Source: pgID, Query: "CREATE TABLE stolen(x int)"})
	if !strings.Contains(r.body, "read-only") {
		t.Fatalf("a query wrote: %s", r.body)
	}
	r = c.postJSON("/datasources/try", store.GraphQuery{Source: pgID, Query: "SELECT 1 AS one"})
	if !strings.Contains(r.body, "no time column") {
		t.Fatalf("no time: %s", r.body)
	}

	// a device graph with a query of each source, next to the ping values
	dev, _ := e.st.DeviceChecks(ctx, 1)
	l := store.GraphLayout{Panels: []store.GraphPanel{{ID: "q1", Title: "Outside", Series: []int32{dev[0].Series[0].ID},
		Queries: []store.GraphQuery{{Source: pgID, Query: series, Label: "Minute", Unit: "min"}, {Source: promID, Query: "up"}},
		Colors:  map[string]string{"q1": "#00aa00", "q7": "#ffffff"}}, {ID: "q2", Queries: []store.GraphQuery{{Source: promID, Query: "up"}}}}}
	if r := c.postJSON("/devices/1/graphs", l); r.code != http.StatusOK {
		t.Fatalf("save graphs: %d %s", r.code, r.body)
	}
	r = c.get("/devices/1")
	for _, want := range []string{`data-queries="/devices/1/graphs/q1/q/0|/devices/1/graphs/q1/q/1"`, `data-qcolors="|#00aa00"`, `data-panel="q2"`, `data-series=""`, "Data source"} {
		if !strings.Contains(r.body, want) {
			t.Fatalf("device page lacks %s", want)
		}
	}
	if !strings.Contains(r.body, `"sources":[{"id":`) {
		t.Fatal("the editor does not know the data sources")
	}
	r = c.get("/devices/1/graphs/q1/q/0?range=1h")
	if !strings.Contains(r.body, `"label":"Minute minute"`) || !strings.Contains(r.body, `"unit":"min"`) {
		t.Fatalf("panel query: %s", r.body)
	}
	if r := c.get("/devices/1/graphs/q1/q/1?range=1h"); !strings.Contains(r.body, `"label":"up{job=\"node\"}"`) {
		t.Fatalf("prometheus query: %s", r.body)
	}
	if r := c.get("/devices/1/graphs/q1/q/5"); r.code != http.StatusNotFound {
		t.Fatalf("missing query: %d", r.code)
	}
	// refused: a source that does not exist
	bad := store.GraphLayout{Panels: []store.GraphPanel{{ID: "x", Queries: []store.GraphQuery{{Source: 999, Query: "up"}}}}}
	if r := c.postJSON("/devices/1/graphs", bad); r.code != http.StatusBadRequest {
		t.Fatalf("unknown source accepted: %d", r.code)
	}

	// a dashboard widget
	c.post("/dashboards", "name", "Business")
	if r := c.post("/dashboards/1/widgets", "kind", "query", "source", strconv.FormatInt(promID, 10), "query", "up", "unit", "x"); r.code != http.StatusSeeOther {
		t.Fatalf("widget: %d %s", r.code, r.body)
	}
	if r := c.get("/dashboards/1"); !strings.Contains(r.body, `data-queries="/dashboards/1/widgets/0/data"`) || !strings.Contains(r.body, "<b>Prom</b>") {
		t.Fatalf("dashboard: %s", r.body)
	}
	if r := c.get("/dashboards/1/widgets/0/data?range=1h"); !strings.Contains(r.body, `"series":[{`) {
		t.Fatalf("widget data: %s", r.body)
	}

	// a deleted source: the graph says so
	c.post("/settings/datasources/" + strconv.FormatInt(promID, 10) + "/delete")
	if r := c.get("/dashboards/1/widgets/0/data?range=1h"); !strings.Contains(r.body, "the data source was deleted") {
		t.Fatalf("deleted source: %s", r.body)
	}

	// viewers see graphs but neither add sources nor run their own queries
	c.post("/users", "username", "watcher", "password", "watcher password 1", "role", "viewer")
	v := e.client("192.168.1.30")
	v.post("/login", "username", "watcher", "password", "watcher password 1")
	e.enroll(v)
	v.post("/account/password", "current", "watcher password 1", "password", "new watcher pass 1", "password2", "new watcher pass 1")
	if r := v.get("/settings/datasources"); r.code != http.StatusForbidden {
		t.Fatalf("viewer saw data sources: %d", r.code)
	}
	if r := v.postJSON("/datasources/try", store.GraphQuery{Source: pgID, Query: series}); r.code != http.StatusForbidden {
		t.Fatalf("viewer ran a query: %d", r.code)
	}
	if r := v.get("/devices/1/graphs/q1/q/0?range=1h"); !strings.Contains(r.body, `"series":[{`) {
		t.Fatalf("viewer graph: %s", r.body)
	}
}
