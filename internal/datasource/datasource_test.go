package datasource

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var (
	from = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	to   = from.Add(time.Hour)
	req  = Request{From: from, To: to, Points: 60}
)

func TestExpand(t *testing.T) {
	q := "SELECT $__timeGroup(ts) AS time, avg(v) FROM m WHERE $__timeFilter(ts) AND x > $__unixFrom AND y < $__unixTo GROUP BY 1 -- $__interval_s"
	for kind, want := range map[string][]string{
		Postgres: {"to_timestamp(floor(extract(epoch from ts)/60)*60)", "(ts BETWEEN '2026-10-29 10:00:00+00'::timestamptz AND '2026-10-29 11:00:00+00'::timestamptz)", "x > 1793268000", "y < 1793271600", "-- 60"},
		MySQL:    {"FROM_UNIXTIME(FLOOR(UNIX_TIMESTAMP(ts)/60)*60)", "(ts BETWEEN '2026-10-29 10:00:00' AND '2026-10-29 11:00:00')"},
		MSSQL:    {"DATEDIFF_BIG(second, '1970-01-01', ts)/60)*60", "CAST('2026-10-29T10:00:00' AS datetime2)"},
	} {
		got := Expand(kind, q, req)
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", kind, got, w)
			}
		}
		if strings.Contains(got, "$__") {
			t.Errorf("%s: macro left in %q", kind, got)
		}
	}
	if got := Expand(Postgres, "$__timeGroup(ts, 300)", req); got != "to_timestamp(floor(extract(epoch from ts)/300)*300)" {
		t.Errorf("fixed group: %s", got)
	}
	if got := ExpandInflux(`SELECT mean("v") FROM "cpu" WHERE $timeFilter GROUP BY time($__interval)`, req); !strings.Contains(got, "time >= 1793268000s AND time <= 1793271600s") || !strings.Contains(got, "time(60s)") {
		t.Errorf("influx: %s", got)
	}
}

func TestTableSeries(t *testing.T) {
	t0 := from
	// wide: a time and two numeric columns (one as text, as MySQL gives it)
	ss, err := tableSeries([]string{"ts", "cpu", "mem"}, [][]any{{t0, 1.5, []byte("10")}, {t0.Add(time.Minute), int64(2), []byte("11")}})
	if err != nil || len(ss) != 2 || ss[0].Name != "cpu" || ss[1].Name != "mem" || *ss[1].V[1] != 11 || ss[0].T[1] != t0.Unix()+60 {
		t.Fatalf("wide: %+v %v", ss, err)
	}
	// long: time, a name column and a value
	ss, err = tableSeries([]string{"time", "host", "value"}, [][]any{{t0.Unix(), "a", 1.0}, {t0.Unix(), "b", 2.0}, {t0.Unix() + 60, "a", 3.0}})
	if err != nil || len(ss) != 2 || ss[0].Name != "a" || len(ss[0].T) != 2 || ss[1].Name != "b" {
		t.Fatalf("long: %+v %v", ss, err)
	}
	if _, err := tableSeries([]string{"a", "b"}, [][]any{{"x", "y"}}); err == nil {
		t.Fatal("no time column accepted")
	}
	if ss, err := tableSeries([]string{"time", "v"}, nil); err != nil || len(ss) != 0 {
		t.Fatal("no rows should be no series")
	}
}

func TestThin(t *testing.T) {
	s := Series{Name: "x"}
	for i := 0; i < 1000; i++ {
		v := float64(i)
		s.T = append(s.T, int64(1000-i)) // backwards: sorted by thin
		s.V = append(s.V, &v)
	}
	out := thin(s, 100)
	if len(out.T) < 90 || len(out.T) > 101 || out.T[0] > out.T[1] {
		t.Fatalf("thin: %d points", len(out.T))
	}
}

func TestHTTPSources(t *testing.T) {
	var gotAuth, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotQuery = r.Header.Get("Authorization"), r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/query_range":
			if r.URL.Query().Get("query") == "bad(" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"parse error: unexpected end"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up","job":"node"},"values":[[1793268000,"1"],[1793268060,"0"]]}]}}`))
		case "/api/v1/query":
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"scalar","result":[1,"1"]}}`))
		case "/query":
			_, _ = w.Write([]byte(`{"results":[{"statement_id":0,"series":[{"name":"cpu","tags":{"host":"a"},"columns":["time","mean"],"values":[[1793268000,12.5],[1793268060,null]]}]}]}`))
		case "/grafana":
			_, _ = w.Write([]byte(`[{"target":"temp","datapoints":[[21.5,1793268000000],[22,1793268060000]]}]`))
		case "/plain":
			_, _ = w.Write([]byte(`[[1793268000,5],[1793268060,6]]`))
		case "/named":
			_, _ = w.Write([]byte(`{"b":[[1793268000,1]],"a":[[1793268000,2]]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	prom := Config{Kind: Prometheus, Address: srv.URL + "/", Secret: "tok"}
	ss, err := Query(ctx, prom, Request{Query: "up", From: from, To: to, Points: 60})
	if err != nil || len(ss) != 1 || ss[0].Name != `up{job="node"}` || len(ss[0].T) != 2 || *ss[0].V[1] != 0 || gotAuth != "Bearer tok" || !strings.Contains(gotQuery, "step=60") {
		t.Fatalf("prometheus: %+v %v %q %q", ss, err, gotAuth, gotQuery)
	}
	if _, err := Query(ctx, prom, Request{Query: "bad(", From: from, To: to}); err == nil || err.Error() != "parse error: unexpected end" {
		t.Fatalf("prometheus error: %v", err)
	}
	if err := Test(ctx, prom); err != nil {
		t.Fatal(err)
	}

	influx := Config{Kind: InfluxDB, Address: srv.URL, Database: "telegraf", Secret: "itok"}
	ss, err = Query(ctx, influx, Request{Query: `SELECT mean(usage) FROM cpu WHERE $timeFilter GROUP BY time($__interval), host`, From: from, To: to, Points: 60})
	if err != nil || len(ss) != 1 || ss[0].Name != "cpu {host=a}" || ss[0].V[1] != nil || gotAuth != "Token itok" || !strings.Contains(gotQuery, "db=telegraf") {
		t.Fatalf("influx: %+v %v %q", ss, err, gotAuth)
	}

	js := Config{Kind: JSON, Address: srv.URL, User: "u", Secret: "p"}
	for path, want := range map[string]string{"grafana": "temp", "/plain": "value", "named?x=1": "a"} {
		ss, err = Query(ctx, js, Request{Query: path, From: from, To: to, Points: 60})
		if err != nil || len(ss) == 0 || ss[0].Name != want || ss[0].T[0] != 1793268000 || !strings.HasPrefix(gotAuth, "Basic ") || !strings.Contains(gotQuery, "from=1793268000") {
			t.Fatalf("json %s: %+v %v %q", path, ss, err, gotQuery)
		}
	}
	if _, err := Query(ctx, js, Request{Query: "missing", From: from, To: to}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("json 404: %v", err)
	}
	if _, err := Query(ctx, Config{Kind: JSON, Address: "ftp://x"}, req); err == nil {
		t.Fatal("non-HTTP address accepted")
	}
	if _, err := Query(ctx, Config{Kind: "oracle"}, Request{Query: "x"}); err != ErrKind {
		t.Fatal("unknown kind accepted")
	}
}
