package tsdb

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestEncodeRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	cases := map[string]func(i int) (int64, float64){
		"steady": func(i int) (int64, float64) { return 1_700_000_000 + int64(i)*60, 12.5 },
		"jitter": func(i int) (int64, float64) {
			return 1_700_000_000 + int64(i)*60 + int64(r.IntN(7)) - 3, r.Float64() * 100
		},
		"counters": func(i int) (int64, float64) { return 1_700_000_000 + int64(i)*10, float64(i) * 1234567 },
		"gaps": func(i int) (int64, float64) {
			return 1_700_000_000 + int64(i*i)*97, math.Float64frombits(r.Uint64() &^ (0x7ff << 52))
		},
		"negative": func(i int) (int64, float64) { return -1000 + int64(i)*5000000, -float64(i) / 3 },
	}
	for name, gen := range cases {
		for _, n := range []int{0, 1, 2, 60, 1500} {
			ts := make([]int64, n)
			a, b := make([]float64, n), make([]float64, n)
			for i := range ts {
				ts[i], a[i] = gen(i)
				b[i] = -a[i] * 2
			}
			blk := Encode(ts, a, b)
			gt, gc, err := Decode(blk)
			if err != nil {
				t.Fatalf("%s/%d: %v", name, n, err)
			}
			if len(gt) != n || len(gc) != 2 {
				t.Fatalf("%s/%d: got %d points, %d columns", name, n, len(gt), len(gc))
			}
			for i := range ts {
				if gt[i] != ts[i] || math.Float64bits(gc[0][i]) != math.Float64bits(a[i]) || math.Float64bits(gc[1][i]) != math.Float64bits(b[i]) {
					t.Fatalf("%s/%d: point %d differs", name, n, i)
				}
			}
		}
	}
}

func TestEncodeIsCompact(t *testing.T) {
	ts := make([]int64, 60)
	v := make([]float64, 60)
	for i := range ts {
		ts[i] = 1_700_000_000 + int64(i)*60
		v[i] = 1.2 + float64(i%3)*0.1 // a ping time that moves a little
	}
	if n := len(Encode(ts, v)); n > 150 {
		t.Fatalf("an hour of one-minute samples takes %d bytes", n)
	}
	for i := range v {
		v[i] = 1 + float64(i%7)*0.0123456789 // not decimal: XOR mode
	}
	if n := len(Encode(ts, v)); n > 600 {
		t.Fatalf("an hour of float samples takes %d bytes", n)
	}
	for i := range v {
		v[i] = 1
	}
	if n := len(Encode(ts, v)); n > 40 {
		t.Fatalf("an hour of constant samples takes %d bytes", n)
	}
}

func TestDecodeCorrupt(t *testing.T) {
	ts := []int64{1, 61, 121, 200}
	v := []float64{1, 2.5, 3.25, 1e9}
	blk := Encode(ts, v)
	for i := 0; i < len(blk); i++ {
		_, _, _ = Decode(blk[:i]) // must not panic
		bad := append([]byte(nil), blk...)
		bad[i] ^= 0xff
		_, _, _ = Decode(bad)
	}
	if _, _, err := Decode(nil); err == nil {
		t.Fatal("empty block accepted")
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(Encode([]int64{1, 2, 3}, []float64{1, 2, 3}))
	f.Fuzz(func(t *testing.T, b []byte) { _, _, _ = Decode(b) })
}

func TestDownsample(t *testing.T) {
	in := rawAggs([]int64{0, 60, 120, 300, 360}, []float64{1, 3, 2, 10, 20})
	out := Downsample(in, 300)
	if len(out) != 2 {
		t.Fatalf("%d points", len(out))
	}
	if out[0].T != 0 || out[0].Min != 1 || out[0].Max != 3 || out[0].Avg != 2 || out[0].N != 3 {
		t.Fatalf("first: %+v", out[0])
	}
	if out[1].T != 300 || out[1].Avg != 15 || out[1].N != 2 {
		t.Fatalf("second: %+v", out[1])
	}
	// weighted merge of summaries
	m := Downsample([]Agg{{T: 0, Min: 0, Max: 4, Avg: 2, N: 3}, {T: 300, Min: 5, Max: 5, Avg: 5, N: 1}}, 3600)
	if len(m) != 1 || m[0].Avg != 2.75 || m[0].Min != 0 || m[0].Max != 5 || m[0].N != 4 {
		t.Fatalf("merge: %+v", m)
	}
}
