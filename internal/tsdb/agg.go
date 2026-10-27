package tsdb

import "math"

// Agg is a point of a series: a raw sample (N = 1, Min = Max = Avg) or the
// summary of a period that starts at T.
type Agg struct {
	T             int64 // Unix seconds
	Min, Max, Avg float64
	N             float64 // number of raw samples
}

// rawAggs turns raw samples into points.
func rawAggs(ts []int64, v []float64) []Agg {
	out := make([]Agg, len(ts))
	for i := range ts {
		out[i] = Agg{T: ts[i], Min: v[i], Max: v[i], Avg: v[i], N: 1}
	}
	return out
}

// Downsample merges points (sorted by time) into periods of step seconds
// aligned to the epoch; the average is weighted by the sample counts.
func Downsample(in []Agg, step int64) []Agg {
	if step <= 1 || len(in) == 0 {
		return in
	}
	var out []Agg
	var cur Agg
	var sum float64
	open := false
	for _, p := range in {
		start := floorDiv(p.T, step) * step
		if open && start != cur.T {
			cur.Avg = sum / cur.N
			out = append(out, cur)
			open = false
		}
		if !open {
			cur, sum, open = Agg{T: start, Min: p.Min, Max: p.Max}, 0, true
		}
		cur.Min = math.Min(cur.Min, p.Min)
		cur.Max = math.Max(cur.Max, p.Max)
		cur.N += p.N
		sum += p.Avg * p.N
	}
	cur.Avg = sum / cur.N
	return append(out, cur)
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// encodeAggs packs summary points (columns min, max, avg, count).
func encodeAggs(a []Agg) []byte {
	ts := make([]int64, len(a))
	mn, mx, av, n := make([]float64, len(a)), make([]float64, len(a)), make([]float64, len(a)), make([]float64, len(a))
	for i, p := range a {
		ts[i], mn[i], mx[i], av[i], n[i] = p.T, p.Min, p.Max, p.Avg, p.N
	}
	return Encode(ts, mn, mx, av, n)
}

func decodeAggs(b []byte) ([]Agg, error) {
	ts, cols, err := Decode(b)
	if err != nil {
		return nil, err
	}
	switch len(cols) {
	case 1:
		return rawAggs(ts, cols[0]), nil
	case 4:
		out := make([]Agg, len(ts))
		for i := range ts {
			out[i] = Agg{T: ts[i], Min: cols[0][i], Max: cols[1][i], Avg: cols[2][i], N: cols[3][i]}
		}
		return out, nil
	}
	return nil, errCorrupt
}
