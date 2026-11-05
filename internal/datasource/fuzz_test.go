package datasource

import "testing"

// FuzzParseJSONSeries: the body comes from a remote JSON data source; it must
// not panic, and every series it returns must have one value per time.
func FuzzParseJSONSeries(f *testing.F) {
	f.Add([]byte(`[{"name":"a","points":[[1700000000,1.5],[1700000060,null]]}]`))
	f.Add([]byte(`{"series":[{"name":"t","t":[1,2],"v":[1,2]}]}`))
	f.Add([]byte(`[[1700000000, 3]]`))
	f.Add([]byte(`{"a":1e400}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, b []byte) {
		ss, err := parseJSONSeries(b)
		if err != nil {
			return
		}
		for _, s := range ss {
			if len(s.T) != len(s.V) {
				t.Fatalf("series %q: %d times, %d values", s.Name, len(s.T), len(s.V))
			}
		}
	})
}
