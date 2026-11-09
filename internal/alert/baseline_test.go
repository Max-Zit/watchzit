package alert

import (
	"math"
	"testing"
	"time"
)

// learnBaseline: a busy working day and a quiet night give different usual
// values; too little history learns nothing; a flat value still has a
// spread, so a tiny change is not "unusual".
func TestLearnBaseline(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC) // a Wednesday
	var pts []Point
	for h := 28 * 24; h > 0; h-- {
		at := now.Add(-time.Duration(h) * time.Hour)
		v := 10.0
		if wd := at.Weekday(); wd >= time.Monday && wd <= time.Friday && at.Hour() >= 9 && at.Hour() < 17 {
			v = 40
		}
		v += float64(h%3) - 1 // a little noise
		pts = append(pts, Point{T: at, V: v})
	}
	b := learnBaseline(pts, now)
	if !b.ok {
		t.Fatal("four weeks learned nothing")
	}
	if d, usual, _, ok := b.deviation(41, now); !ok || math.Abs(usual-40) > 1.5 || math.Abs(d) > 2 {
		t.Fatalf("Wednesday noon: usual %.1f, 41 is %.1f deviations", usual, d)
	}
	night := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	if d, usual, _, _ := b.deviation(40, night); math.Abs(usual-10) > 1.5 || d < 5 {
		t.Fatalf("Wednesday 3:00: usual %.1f, 40 only %.1f deviations", usual, d)
	}
	if short := learnBaseline(pts[len(pts)-5*24:], now); short.ok {
		t.Fatal("five days of history already compared")
	}
	var flat []Point
	for h := 14 * 24; h > 0; h-- {
		flat = append(flat, Point{T: now.Add(-time.Duration(h) * time.Hour), V: 100})
	}
	f := learnBaseline(flat, now)
	if d, _, _, ok := f.deviation(101, now); !ok || math.Abs(d) > 1 {
		t.Fatalf("a flat 100: 101 is %.1f deviations", d)
	}
	if d, _, _, _ := f.deviation(200, now); d < 10 {
		t.Fatalf("a flat 100: 200 only %.1f deviations", d)
	}
}
