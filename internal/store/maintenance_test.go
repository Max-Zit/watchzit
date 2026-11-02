package store

import (
	"testing"
	"time"
)

// TestMaintenanceWindow: a repeating window is on in each of its windows,
// off between them, and over after its last day.
func TestMaintenanceWindow(t *testing.T) {
	loc := time.Local
	start := time.Date(2026, 11, 2, 22, 0, 0, 0, loc)
	m := &Maintenance{StartsAt: start, EndsAt: start.Add(2 * time.Hour), Repeat: "weekly"}
	for _, c := range []struct {
		at     time.Time
		active bool
	}{
		{start.Add(-time.Minute), false},
		{start.Add(time.Hour), true},
		{start.Add(3 * time.Hour), false},
		{start.AddDate(0, 0, 7).Add(30 * time.Minute), true},
		{start.AddDate(0, 0, 8), false},
		{start.AddDate(0, 0, 70).Add(time.Minute), true},
	} {
		if got := m.Active(c.at); got != c.active {
			t.Errorf("%v: active %v", c.at, got)
		}
	}
	if n := m.Next(start.Add(3 * time.Hour)); !n.Equal(start.AddDate(0, 0, 7)) {
		t.Errorf("next %v", n)
	}
	until := start.AddDate(0, 0, 14).Add(time.Hour)
	m.RepeatUntil = &until
	if m.Active(start.AddDate(0, 0, 21).Add(time.Minute)) || !m.Over(start.AddDate(0, 0, 15)) || m.Over(start.AddDate(0, 0, 13)) {
		t.Error("repetition did not end on its last day")
	}
	monthly := &Maintenance{StartsAt: time.Date(2026, 1, 31, 1, 0, 0, 0, loc), EndsAt: time.Date(2026, 1, 31, 3, 0, 0, 0, loc), Repeat: "monthly"}
	if !monthly.Active(time.Date(2026, 3, 31, 2, 0, 0, 0, loc)) {
		t.Error("monthly window missed")
	}
	once := &Maintenance{StartsAt: start, EndsAt: start.Add(time.Hour)}
	if !once.Active(start.Add(time.Minute)) || once.Active(start.AddDate(0, 0, 7)) || !once.Over(start.Add(2*time.Hour)) {
		t.Error("a single window")
	}
}
