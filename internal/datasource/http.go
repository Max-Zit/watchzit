package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---- Prometheus (and what speaks its API: Thanos, VictoriaMetrics, Mimir)

func queryPrometheus(ctx context.Context, c Config, r Request) ([]Series, error) {
	step := int64(r.Step() / time.Second)
	if n := (r.To.Unix() - r.From.Unix()) / 11000; step < n { // Prometheus answers at most 11,000 points
		step = n + 1
	}
	path := "/api/v1/query_range?query=" + urlQuery(r.Query) + "&start=" + strconv.FormatInt(r.From.Unix(), 10) +
		"&end=" + strconv.FormatInt(r.To.Unix(), 10) + "&step=" + strconv.FormatInt(step, 10)
	body, err := getJSON(ctx, c, path, nil)
	if err != nil {
		return nil, promErr(err)
	}
	var res struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Values [][2]any          `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, errors.New("the answer is not Prometheus JSON")
	}
	if res.Status != "success" {
		return nil, errors.New(res.Error)
	}
	out := []Series{}
	for _, m := range res.Data.Result {
		s := Series{Name: promName(m.Metric)}
		for _, p := range m.Values {
			t, ok := unixOf(p[0])
			if !ok {
				continue
			}
			v, _ := num(p[1])
			s.T = append(s.T, t)
			s.V = append(s.V, v)
		}
		out = append(out, s)
	}
	return out, nil
}

// promErr shows Prometheus' own message of a refused query.
func promErr(err error) error {
	msg := err.Error()
	if i := strings.Index(msg, `"error":"`); i >= 0 {
		rest := msg[i+9:]
		if j := strings.Index(rest, `"`); j > 0 {
			return errors.New(rest[:j])
		}
	}
	return err
}

// promName is a series name as Prometheus writes it: name{label="value"}.
func promName(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "__name__" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + `="` + m[k] + `"`
	}
	n := m["__name__"]
	if len(parts) > 0 {
		n += "{" + strings.Join(parts, ", ") + "}"
	}
	if n == "" {
		n = "value"
	}
	return n
}

// ---- InfluxDB (InfluxQL; 1.x, and 2.x/3.x through their 1.x API)

// ExpandInflux replaces $timeFilter and $__interval (or $interval).
func ExpandInflux(q string, r Request) string {
	step := strconv.FormatInt(int64(r.Step()/time.Second), 10) + "s"
	return strings.NewReplacer(
		"$timeFilter", "time >= "+strconv.FormatInt(r.From.Unix(), 10)+"s AND time <= "+strconv.FormatInt(r.To.Unix(), 10)+"s",
		"$__interval", step, "$interval", step,
	).Replace(q)
}

func influxHeader(c Config) map[string]string {
	if c.User == "" && c.Secret != "" { // an InfluxDB 2 token
		return map[string]string{"Authorization": "Token " + c.Secret}
	}
	return nil
}

func queryInflux(ctx context.Context, c Config, r Request) ([]Series, error) {
	path := "/query?epoch=s&q=" + urlQuery(ExpandInflux(r.Query, r))
	if c.Database != "" {
		path += "&db=" + urlQuery(c.Database)
	}
	body, err := getJSON(ctx, c, path, influxHeader(c))
	if err != nil {
		return nil, err
	}
	var res struct {
		Error   string `json:"error"`
		Results []struct {
			Error  string `json:"error"`
			Series []struct {
				Name    string            `json:"name"`
				Tags    map[string]string `json:"tags"`
				Columns []string          `json:"columns"`
				Values  [][]any           `json:"values"`
			} `json:"series"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, errors.New("the answer is not InfluxDB JSON")
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	out := []Series{}
	for _, st := range res.Results {
		if st.Error != "" {
			return nil, errors.New(st.Error)
		}
		for _, sr := range st.Series {
			base := sr.Name
			if len(sr.Tags) > 0 {
				keys := make([]string, 0, len(sr.Tags))
				for k := range sr.Tags {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				parts := make([]string, len(keys))
				for i, k := range keys {
					parts[i] = k + "=" + sr.Tags[k]
				}
				base += " {" + strings.Join(parts, ", ") + "}"
			}
			tc := -1
			for i, col := range sr.Columns {
				if col == "time" {
					tc = i
				}
			}
			if tc < 0 {
				continue
			}
			for vc, col := range sr.Columns {
				if vc == tc {
					continue
				}
				s := Series{Name: base + "." + col}
				if len(sr.Columns) == 2 {
					s.Name = base
				}
				for _, row := range sr.Values {
					if len(row) != len(sr.Columns) {
						continue
					}
					t, ok := unixOf(row[tc])
					if !ok {
						continue
					}
					v, _ := num(row[vc])
					s.T = append(s.T, t)
					s.V = append(s.V, v)
				}
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// ---- any HTTP address that answers JSON

// queryJSON fetches the address with the query appended (a path or
// parameters) and from, to and step (Unix seconds) added. It reads:
//
//	[{"target": "name", "datapoints": [[value, time], ...]}, ...]
//	{"name": [[time, value], ...], ...}
//	[[time, value], ...]
//
// with times in seconds or milliseconds.
func queryJSON(ctx context.Context, c Config, r Request) ([]Series, error) {
	path := strings.TrimSpace(r.Query)
	if path != "" && !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "?") && !strings.HasPrefix(path, "&") {
		path = "/" + path
	}
	sep := "?"
	if strings.Contains(c.Address+path, "?") {
		sep = "&"
	}
	path += sep + "from=" + strconv.FormatInt(r.From.Unix(), 10) + "&to=" + strconv.FormatInt(r.To.Unix(), 10) + "&step=" + strconv.FormatInt(int64(r.Step()/time.Second), 10)
	body, err := getJSON(ctx, c, path, nil)
	if err != nil {
		return nil, err
	}
	return parseJSONSeries(body)
}

func parseJSONSeries(body []byte) ([]Series, error) {
	pairs := func(name string, ps [][]any, valueFirst bool) Series {
		s := Series{Name: name}
		for _, p := range ps {
			if len(p) < 2 {
				continue
			}
			ti, vi := 0, 1
			if valueFirst {
				ti, vi = 1, 0
			}
			t, ok := unixOf(p[ti])
			if !ok {
				continue
			}
			v, _ := num(p[vi])
			s.T = append(s.T, t)
			s.V = append(s.V, v)
		}
		return s
	}
	var targets []struct {
		Target     string  `json:"target"`
		Datapoints [][]any `json:"datapoints"`
	}
	if err := json.Unmarshal(body, &targets); err == nil && len(targets) > 0 && targets[0].Datapoints != nil {
		out := []Series{}
		for _, t := range targets {
			out = append(out, pairs(t.Target, t.Datapoints, true))
		}
		return out, nil
	}
	var plain [][]any
	if err := json.Unmarshal(body, &plain); err == nil {
		return []Series{pairs("value", plain, false)}, nil
	}
	var named map[string][][]any
	if err := json.Unmarshal(body, &named); err == nil {
		keys := make([]string, 0, len(named))
		for k := range named {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := []Series{}
		for _, k := range keys {
			out = append(out, pairs(k, named[k], false))
		}
		return out, nil
	}
	return nil, errors.New("the answer is not JSON time series")
}
