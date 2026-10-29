// Package datasource reads time series from outside WatchZit, like the data
// sources of a dashboard: SQL databases (PostgreSQL, MySQL/MariaDB, SQL
// Server), Prometheus, InfluxDB and any HTTP address that answers JSON.
// A query covers a time range and gives one or more named series.
package datasource

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kinds of data sources.
const (
	Postgres   = "postgres"
	MySQL      = "mysql"
	MSSQL      = "mssql"
	Prometheus = "prometheus"
	InfluxDB   = "influxdb"
	JSON       = "json"
)

// Kinds lists the kinds in the order the console offers them.
var Kinds = []string{Postgres, MySQL, MSSQL, Prometheus, InfluxDB, JSON}

// IsSQL tells the kinds that take SQL.
func IsSQL(kind string) bool { return kind == Postgres || kind == MySQL || kind == MSSQL }

// Config is how to reach a data source.
type Config struct {
	Kind     string
	Address  string // host[:port] for SQL, the base URL otherwise
	Database string // SQL database, InfluxDB database
	User     string
	Secret   string // password or token (plain)
	TLS      string // SQL: "" (if offered), require, verify, disable; HTTP: "" (verify) or insecure
	Timeout  time.Duration
}

// Series is one named line of values.
type Series struct {
	Name string
	T    []int64    // Unix seconds, ascending
	V    []*float64 // nil: no value
}

// Request is one query over a time range.
type Request struct {
	Query    string
	From, To time.Time
	Points   int // wanted points per series (the graph width)
}

// Step is the interval the request asks the source for.
func (r Request) Step() time.Duration {
	n := r.Points
	if n < 10 {
		n = 600
	}
	st := r.To.Sub(r.From) / time.Duration(n)
	if st < time.Second {
		st = time.Second
	}
	return st.Round(time.Second)
}

// Limits of what one query may bring back.
const (
	MaxRows    = 200000
	MaxSeries  = 50
	MaxBody    = 32 << 20
	DefTimeout = 20 * time.Second
)

// ErrKind is an unknown kind of data source.
var ErrKind = errors.New("unknown kind of data source")

// Query runs a query and gives its series, thinned to about Points values.
func Query(ctx context.Context, c Config, r Request) ([]Series, error) {
	if c.Timeout <= 0 {
		c.Timeout = DefTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	if strings.TrimSpace(r.Query) == "" && c.Kind != JSON {
		return nil, errors.New("the query is empty")
	}
	var ss []Series
	var err error
	switch c.Kind {
	case Postgres, MySQL, MSSQL:
		ss, err = querySQL(ctx, c, r)
	case Prometheus:
		ss, err = queryPrometheus(ctx, c, r)
	case InfluxDB:
		ss, err = queryInflux(ctx, c, r)
	case JSON:
		ss, err = queryJSON(ctx, c, r)
	default:
		return nil, ErrKind
	}
	if err != nil {
		return nil, err
	}
	if len(ss) > MaxSeries {
		ss = ss[:MaxSeries]
	}
	for i := range ss {
		ss[i] = thin(ss[i], r.Points)
	}
	return ss, nil
}

// Test checks that the data source answers.
func Test(ctx context.Context, c Config) error {
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	switch c.Kind {
	case Postgres, MySQL, MSSQL:
		return testSQL(ctx, c)
	case Prometheus:
		_, err := getJSON(ctx, c, "/api/v1/query?query=1", nil)
		return err
	case InfluxDB:
		_, err := getJSON(ctx, c, "/query?q="+urlQuery("SHOW DATABASES"), influxHeader(c))
		return err
	case JSON:
		_, err := getJSON(ctx, c, "", nil)
		return err
	}
	return ErrKind
}

// thin sorts a series and averages it down to about n points.
func thin(s Series, n int) Series {
	if len(s.T) > 1 && !sort.SliceIsSorted(s.T, func(i, j int) bool { return s.T[i] < s.T[j] }) {
		idx := make([]int, len(s.T))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return s.T[idx[a]] < s.T[idx[b]] })
		t2, v2 := make([]int64, len(idx)), make([]*float64, len(idx))
		for i, k := range idx {
			t2[i], v2[i] = s.T[k], s.V[k]
		}
		s.T, s.V = t2, v2
	}
	if n < 10 || len(s.T) <= n {
		return s
	}
	from, to := s.T[0], s.T[len(s.T)-1]
	width := float64(to-from+1) / float64(n)
	out := Series{Name: s.Name}
	b, sum, cnt, has := 0, 0.0, 0, false
	flush := func() {
		if !has {
			return
		}
		t := from + int64(float64(b)*width+width/2)
		out.T = append(out.T, t)
		if cnt > 0 {
			v := sum / float64(cnt)
			out.V = append(out.V, &v)
		} else {
			out.V = append(out.V, nil)
		}
		sum, cnt, has = 0, 0, false
	}
	for i, t := range s.T {
		k := int(float64(t-from) / width)
		if k >= n {
			k = n - 1
		}
		if k != b {
			flush()
			b = k
		}
		has = true
		if v := s.V[i]; v != nil {
			sum += *v
			cnt++
		}
	}
	flush()
	return out
}

// num is a value from a database or JSON as a number.
func num(x any) (*float64, bool) {
	var f float64
	switch v := x.(type) {
	case nil:
		return nil, true
	case float64:
		f = v
	case float32:
		f = float64(v)
	case int64:
		f = float64(v)
	case int32:
		f = float64(v)
	case int:
		f = float64(v)
	case int16:
		f = float64(v)
	case int8:
		f = float64(v)
	case uint64:
		f = float64(v)
	case uint32:
		f = float64(v)
	case uint8:
		f = float64(v)
	case bool:
		if v {
			f = 1
		}
	case []byte:
		return num(string(v))
	case string:
		p, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return nil, false
		}
		f = p
	case fmt.Stringer: // decimals of some drivers
		return num(v.String())
	default:
		return nil, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, true
	}
	return &f, true
}

// unixOf is a time value as Unix seconds: a time, or a number of seconds
// or milliseconds.
func unixOf(x any) (int64, bool) {
	switch v := x.(type) {
	case time.Time:
		return v.Unix(), true
	case []byte:
		return unixOf(string(v))
	case string:
		for _, l := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
			if t, err := time.ParseInLocation(l, strings.TrimSpace(v), time.UTC); err == nil {
				return t.Unix(), true
			}
		}
	}
	f, ok := num(x)
	if !ok || f == nil {
		return 0, false
	}
	if math.Abs(*f) > 1e11 { // milliseconds
		return int64(*f / 1000), true
	}
	return int64(*f), true
}

// ---- HTTP sources

func httpClient(c Config) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.TLS == "insecure"} // #nosec G402 -- chosen by the administrator for self-signed sources
	tr.Proxy = nil
	return &http.Client{Transport: tr, Timeout: c.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// getJSON fetches the base URL + path and returns the body; the user and
// secret go as basic sign-in, or the secret alone as a bearer token.
func getJSON(ctx context.Context, c Config, path string, hdr map[string]string) ([]byte, error) {
	base := strings.TrimRight(strings.TrimSpace(c.Address), "/")
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return nil, errors.New("the address must start with http:// or https://")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "WatchZit")
	switch {
	case c.User != "":
		req.SetBasicAuth(c.User, c.Secret)
	case c.Secret != "":
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := httpClient(c).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxBody {
		return nil, errors.New("the answer is too large")
	}
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	return body, nil
}

func urlQuery(s string) string { return url.QueryEscape(s) }
