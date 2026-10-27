package tsdb

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func newDB(t *testing.T) (*DB, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return New(st.Pool(), slog.New(slog.NewTextHandler(io.Discard, nil))), st.Pool()
}

func count(t *testing.T, p *pgxpool.Pool, q string) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestLifecycle writes three days of one-minute samples hour by hour and
// checks packing, rollups, queries at every resolution and retention.
func TestLifecycle(t *testing.T) {
	ctx := context.Background()
	d, pool := newDB(t)
	start := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
	now := start
	d.SetClock(func() time.Time { return now })
	if err := d.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	const days = 3
	for m := 0; m < days*1440; m++ {
		ts := start.Add(time.Duration(m) * time.Minute)
		d.Add(Sample{Series: 1, T: ts, V: float64(m % 60)}, Sample{Series: 2, T: ts, V: 7})
		if m%60 == 59 {
			now = ts.Add(time.Minute)
			if err := d.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			now = now.Add(3 * time.Minute)
			if err := d.Maintain(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n := count(t, pool, `SELECT count(*) FROM samples_hot`); n != 0 {
		t.Fatalf("%d samples left unpacked", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM samples_raw`); n != 2*days*24 {
		t.Fatalf("%d raw blocks", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM samples_5m`); n != 2*days {
		t.Fatalf("%d 5-minute blocks", n)
	}
	// Raw: an hour, every sample.
	r, err := d.Query(ctx, 1, start.Add(time.Hour), start.Add(2*time.Hour), 500)
	if err != nil {
		t.Fatal(err)
	}
	if r.Step != 0 || len(r.Points) != 60 || r.Points[0].Avg != 0 || r.Points[59].Avg != 59 {
		t.Fatalf("raw: step %d, %d points", r.Step, len(r.Points))
	}
	// 5 minutes over the whole time.
	r, err = d.Query(ctx, 1, start, start.AddDate(0, 0, days), days*288)
	if err != nil {
		t.Fatal(err)
	}
	if r.Step != 300 || len(r.Points) != days*288 {
		t.Fatalf("5m: step %d, %d points", r.Step, len(r.Points))
	}
	for _, p := range r.Points {
		if p.N != 5 {
			t.Fatalf("5m point %+v", p)
		}
	}
	// Hourly: 0..59 every hour, average 29.5 (March from 5-minute data,
	// April 1 computed).
	r, err = d.Query(ctx, 1, start, start.AddDate(0, 0, days), 72)
	if err != nil {
		t.Fatal(err)
	}
	if r.Step != 3600 || len(r.Points) != days*24 || r.Points[5].Avg != 29.5 || r.Points[5].Min != 0 || r.Points[5].Max != 59 || r.Points[5].N != 60 {
		t.Fatalf("1h: step %d, %d points, %+v", r.Step, len(r.Points), r.Points[5])
	}
	// 40 days later: March is rolled up hourly, raw data past retention is gone.
	now = start.AddDate(0, 0, 40)
	if err := d.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM samples_1h`); n != 4 { // March and April, 2 series
		t.Fatalf("%d hourly blocks", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM samples_raw`); n != 0 {
		t.Fatalf("%d raw blocks after retention", n)
	}
	r, err = d.Query(ctx, 2, start, start.AddDate(0, 0, days), 72)
	if err != nil || len(r.Points) != days*24 || r.Points[0].Avg != 7 {
		t.Fatalf("1h after retention: %v %d", err, len(r.Points))
	}
	if err := d.Forget(ctx, []int32{1, 2}); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM samples_5m`) + count(t, pool, `SELECT count(*) FROM samples_1h`); n != 0 {
		t.Fatalf("%d blocks after Forget", n)
	}
}

func TestLateSamples(t *testing.T) {
	ctx := context.Background()
	d, _ := newDB(t)
	h := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	now := h
	d.SetClock(func() time.Time { return now })
	_ = d.Maintain(ctx)
	d.Add(Sample{Series: 9, T: h.Add(time.Minute), V: 1})
	if err := d.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	now = h.Add(70 * time.Minute)
	if err := d.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	// A sample of the packed hour arrives late.
	d.Add(Sample{Series: 9, T: h.Add(2 * time.Minute), V: 2})
	if err := d.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := d.Query(ctx, 9, h, h.Add(time.Hour), 100)
	if err != nil || len(r.Points) != 2 || r.Points[1].Avg != 2 {
		t.Fatalf("%v %+v", err, r.Points)
	}
}

func TestRetentionValidate(t *testing.T) {
	if err := DefaultRetention.Validate(); err != nil {
		t.Fatal(err)
	}
	if (Retention{RawDays: 1, FiveMinDays: 90, HourDays: 730}).Validate() == nil {
		t.Fatal("1 day raw accepted")
	}
}
