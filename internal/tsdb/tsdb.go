package tsdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sample is one measured value of a series.
type Sample struct {
	Series int32
	T      time.Time
	V      float64
}

// Retention says how long each resolution is kept.
type Retention struct {
	RawDays     int `json:"raw_days"`
	FiveMinDays int `json:"five_min_days"`
	HourDays    int `json:"hour_days"`
}

// DefaultRetention: raw 30 days, 5-minute points 90 days, hourly 2 years.
var DefaultRetention = Retention{RawDays: 30, FiveMinDays: 90, HourDays: 730}

// Validate checks the ranges. Rollups are built from the finer resolution,
// so raw data must outlive a day and 5-minute data a month.
func (r Retention) Validate() error {
	switch {
	case r.RawDays < 2 || r.RawDays > 400:
		return errors.New("raw data must be kept 2–400 days")
	case r.FiveMinDays < 35 || r.FiveMinDays > 1100:
		return errors.New("5-minute data must be kept 35–1100 days")
	case r.HourDays < 35 || r.HourDays > 3700:
		return errors.New("hourly data must be kept 35–3700 days")
	case r.FiveMinDays < r.RawDays || r.HourDays < r.FiveMinDays:
		return errors.New("coarser data must be kept at least as long as finer data")
	}
	return nil
}

// grace is how long after the end of an hour late samples are still
// expected before the hour is packed.
const grace = 2 * time.Minute

// DB writes and reads samples.
type DB struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	now  func() time.Time

	mu  sync.Mutex
	buf []Sample

	retMu sync.Mutex
	ret   Retention
}

// New uses the tables of migration 001 in pool.
func New(pool *pgxpool.Pool, log *slog.Logger) *DB {
	return &DB{pool: pool, log: log, now: time.Now, ret: DefaultRetention}
}

// SetClock replaces the clock (tests).
func (d *DB) SetClock(now func() time.Time) { d.now = now }

// SetRetention changes the retention (Settings).
func (d *DB) SetRetention(r Retention) {
	d.retMu.Lock()
	d.ret = r
	d.retMu.Unlock()
}

func (d *DB) retention() Retention {
	d.retMu.Lock()
	defer d.retMu.Unlock()
	return d.ret
}

// Add queues samples; values that are not finite are dropped.
func (d *DB) Add(s ...Sample) {
	d.mu.Lock()
	for _, x := range s {
		if !math.IsNaN(x.V) && !math.IsInf(x.V, 0) {
			d.buf = append(d.buf, x)
		}
	}
	d.mu.Unlock()
}

// Flush writes the queued samples.
func (d *DB) Flush(ctx context.Context) error {
	d.mu.Lock()
	buf := d.buf
	d.buf = nil
	d.mu.Unlock()
	if len(buf) == 0 {
		return nil
	}
	_, err := d.pool.CopyFrom(ctx, pgx.Identifier{"samples_hot"}, []string{"series_id", "ts", "value"},
		pgx.CopyFromSlice(len(buf), func(i int) ([]any, error) {
			return []any{buf[i].Series, buf[i].T.UTC(), buf[i].V}, nil
		}))
	if err != nil {
		// Keep them for the next attempt (bounded, so a dead database
		// cannot eat the memory).
		d.mu.Lock()
		if len(d.buf)+len(buf) < 2_000_000 {
			d.buf = append(buf, d.buf...)
		}
		d.mu.Unlock()
	}
	return err
}

// Run flushes every two seconds and does the maintenance every minute
// until ctx ends.
func (d *DB) Run(ctx context.Context) {
	flush := time.NewTicker(2 * time.Second)
	defer flush.Stop()
	maint := time.NewTicker(time.Minute)
	defer maint.Stop()
	if err := d.Maintain(ctx); err != nil && ctx.Err() == nil {
		d.log.Error("metric store maintenance", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = d.Flush(c)
			cancel()
			return
		case <-flush.C:
			if err := d.Flush(ctx); err != nil && ctx.Err() == nil {
				d.log.Error("metric store write", "err", err)
			}
		case <-maint.C:
			if err := d.Maintain(ctx); err != nil && ctx.Err() == nil {
				d.log.Error("metric store maintenance", "err", err)
			}
		}
	}
}

// Maintain creates the coming hour partitions, packs closed hours, builds
// rollups and removes data past its retention.
func (d *DB) Maintain(ctx context.Context) error {
	now := d.now().UTC()
	if err := d.ensurePartitions(ctx, now); err != nil {
		return err
	}
	if err := d.compact(ctx, now); err != nil {
		return fmt.Errorf("pack hours: %w", err)
	}
	if err := d.rollup(ctx, now, tier5m); err != nil {
		return fmt.Errorf("5-minute rollup: %w", err)
	}
	if err := d.rollup(ctx, now, tier1h); err != nil {
		return fmt.Errorf("hourly rollup: %w", err)
	}
	return d.expire(ctx, now)
}

func partName(h time.Time) string { return "samples_hot_p" + h.UTC().Format("2006010215") }

func (d *DB) ensurePartitions(ctx context.Context, now time.Time) error {
	h := now.Truncate(time.Hour)
	for i := 0; i < 3; i++ {
		from := h.Add(time.Duration(i) * time.Hour)
		q := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF samples_hot FOR VALUES FROM ('%s') TO ('%s')`,
			partName(from), from.Format(time.RFC3339), from.Add(time.Hour).Format(time.RFC3339))
		if _, err := d.pool.Exec(ctx, q); err != nil {
			// Rows of that hour already in the default partition: they are
			// packed from there, the hour just has no partition of its own.
			d.log.Debug("hour partition not created", "hour", from, "err", err)
		}
	}
	return nil
}

func getMark(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, kind string) (time.Time, bool, error) {
	var t time.Time
	err := q.QueryRow(ctx, `SELECT upto FROM tsdb_marks WHERE kind=$1`, kind).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return t.UTC(), err == nil, err
}

func setMark(ctx context.Context, tx pgx.Tx, kind string, t time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO tsdb_marks(kind, upto) VALUES($1,$2) ON CONFLICT (kind) DO UPDATE SET upto=EXCLUDED.upto`, kind, t)
	return err
}

// compact packs every closed hour of samples_hot into samples_raw.
func (d *DB) compact(ctx context.Context, now time.Time) error {
	limit := now.Add(-grace)
	upto, ok, err := getMark(ctx, d.pool, "raw")
	if err != nil {
		return err
	}
	// Late samples of hours that were packed already.
	for i := 0; ok && i < 48; i++ {
		var late *time.Time
		if err := d.pool.QueryRow(ctx, `SELECT min(ts) FROM samples_hot WHERE ts < $1`, upto).Scan(&late); err != nil {
			return err
		}
		if late == nil {
			break
		}
		if err := d.compactHour(ctx, late.UTC().Truncate(time.Hour), false); err != nil {
			return err
		}
	}
	for i := 0; i < 1000; i++ {
		// Jump over hours without samples (first start, server stopped).
		var first *time.Time
		if err := d.pool.QueryRow(ctx, `SELECT min(ts) FROM samples_hot WHERE ts >= $1`, upto).Scan(&first); err != nil {
			return err
		}
		if first == nil || !first.Before(limit.Truncate(time.Hour)) {
			end := limit.Truncate(time.Hour)
			if !ok || end.After(upto) {
				return d.withTx(ctx, func(tx pgx.Tx) error { return setMark(ctx, tx, "raw", end) })
			}
			return nil
		}
		if h := first.UTC().Truncate(time.Hour); !ok || h.After(upto) {
			upto, ok = h, true
		}
		if upto.Add(time.Hour).After(limit) {
			return nil
		}
		if err := d.compactHour(ctx, upto, true); err != nil {
			return err
		}
		upto = upto.Add(time.Hour)
	}
	return nil
}

// compactHour packs one hour; advance moves the mark past it (not for late
// samples of an hour packed before).
func (d *DB) compactHour(ctx context.Context, h time.Time, advance bool) error {
	return d.withTx(ctx, func(tx pgx.Tx) error {
		end := h.Add(time.Hour)
		rows, err := tx.Query(ctx, `SELECT series_id, ts, value FROM samples_hot WHERE ts >= $1 AND ts < $2 ORDER BY series_id, ts`, h, end)
		if err != nil {
			return err
		}
		type pts struct {
			ts []int64
			v  []float64
		}
		bySeries := map[int32]*pts{}
		var order []int32
		for rows.Next() {
			var id int32
			var t time.Time
			var v float64
			if err := rows.Scan(&id, &t, &v); err != nil {
				rows.Close()
				return err
			}
			p := bySeries[id]
			if p == nil {
				p = &pts{}
				bySeries[id] = p
				order = append(order, id)
			}
			p.ts = append(p.ts, t.Unix())
			p.v = append(p.v, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// Late samples of an hour that was packed before: merge.
		old, err := tx.Query(ctx, `SELECT series_id, block FROM samples_raw WHERE hour=$1 AND series_id = ANY($2)`, h, order)
		if err != nil {
			return err
		}
		var merged []int32
		for old.Next() {
			var id int32
			var b []byte
			if err := old.Scan(&id, &b); err != nil {
				old.Close()
				return err
			}
			ts, cols, err := Decode(b)
			if err != nil {
				continue // a damaged block is replaced
			}
			p := bySeries[id]
			p.ts = append(ts, p.ts...)
			p.v = append(cols[0], p.v...)
			merged = append(merged, id)
		}
		old.Close()
		if err := old.Err(); err != nil {
			return err
		}
		if len(merged) > 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM samples_raw WHERE hour=$1 AND series_id = ANY($2)`, h, merged); err != nil {
				return err
			}
		}
		blocks := make([][]any, 0, len(order))
		for _, id := range order {
			p := bySeries[id]
			ts, v := sortDedupe(p.ts, p.v)
			blocks = append(blocks, []any{id, h, Encode(ts, v)})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"samples_raw"}, []string{"series_id", "hour", "block"}, pgx.CopyFromRows(blocks)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DROP TABLE IF EXISTS `+partName(h)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM samples_hot_default WHERE ts >= $1 AND ts < $2`, h, end); err != nil {
			return err
		}
		if !advance {
			return nil
		}
		return setMark(ctx, tx, "raw", end)
	})
}

// sortDedupe orders samples by time and keeps the last value of a second.
func sortDedupe(ts []int64, v []float64) ([]int64, []float64) {
	idx := make([]int, len(ts))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return ts[idx[a]] < ts[idx[b]] })
	ot, ov := make([]int64, 0, len(ts)), make([]float64, 0, len(ts))
	for _, i := range idx {
		if n := len(ot); n > 0 && ot[n-1] == ts[i] {
			ov[n-1] = v[i]
			continue
		}
		ot, ov = append(ot, ts[i]), append(ov, v[i])
	}
	return ot, ov
}

// ---- rollups

type tier struct {
	name    string // mark and table suffix
	table   string
	keyCol  string
	step    int64
	src     string // table of the finer data
	srcKey  string
	srcMark string
	period  func(t time.Time) time.Time // start of the period containing t
	next    func(t time.Time) time.Time // start of the following period
}

func dayStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

var (
	tier5m = tier{name: "5m", table: "samples_5m", keyCol: "day", step: 300, src: "samples_raw", srcKey: "hour", srcMark: "raw",
		period: dayStart, next: func(t time.Time) time.Time { return dayStart(t).AddDate(0, 0, 1) }}
	tier1h = tier{name: "1h", table: "samples_1h", keyCol: "month", step: 3600, src: "samples_5m", srcKey: "day", srcMark: "5m",
		period: monthStart, next: func(t time.Time) time.Time { return monthStart(t).AddDate(0, 1, 0) }}
)

// rollup builds the summaries of every period whose finer data is complete.
func (d *DB) rollup(ctx context.Context, now time.Time, t tier) error {
	srcUpto, ok, err := getMark(ctx, d.pool, t.srcMark)
	if err != nil || !ok {
		return err
	}
	upto, ok, err := getMark(ctx, d.pool, t.name)
	if err != nil {
		return err
	}
	if !ok {
		var first *time.Time
		if err := d.pool.QueryRow(ctx, `SELECT min(`+t.srcKey+`) FROM `+t.src).Scan(&first); err != nil {
			return err
		}
		upto = t.period(srcUpto)
		if first != nil {
			upto = t.period(*first)
		}
	}
	for i := 0; i < 400; i++ {
		end := t.next(upto)
		if end.After(srcUpto) || ctx.Err() != nil {
			break
		}
		if err := d.rollupPeriod(ctx, t, upto, end); err != nil {
			return err
		}
		upto = end
	}
	return d.withTx(ctx, func(tx pgx.Tx) error { return setMark(ctx, tx, t.name, upto) })
}

func (d *DB) rollupPeriod(ctx context.Context, t tier, from, to time.Time) error {
	return d.withTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT series_id, block FROM `+t.src+` WHERE `+t.srcKey+` >= $1 AND `+t.srcKey+` < $2 ORDER BY series_id, `+t.srcKey, from, to)
		if err != nil {
			return err
		}
		var out [][]any
		var cur int32 = -1
		var pts []Agg
		emit := func() {
			if cur >= 0 && len(pts) > 0 {
				out = append(out, []any{cur, from, encodeAggs(Downsample(pts, t.step))})
			}
			pts = pts[:0]
		}
		for rows.Next() {
			var id int32
			var b []byte
			if err := rows.Scan(&id, &b); err != nil {
				rows.Close()
				return err
			}
			if id != cur {
				emit()
				cur = id
			}
			a, err := decodeAggs(b)
			if err != nil {
				continue
			}
			pts = append(pts, a...)
		}
		emit()
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM `+t.table+` WHERE `+t.keyCol+`=$1`, from); err != nil {
			return err
		}
		_, err = tx.CopyFrom(ctx, pgx.Identifier{t.table}, []string{"series_id", t.keyCol, "block"}, pgx.CopyFromRows(out))
		return err
	})
}

// expire removes data past the retention, but never finer data that was
// not rolled up yet.
func (d *DB) expire(ctx context.Context, now time.Time) error {
	r := d.retention()
	m5, ok5, err := getMark(ctx, d.pool, "5m")
	if err != nil {
		return err
	}
	m1, ok1, err := getMark(ctx, d.pool, "1h")
	if err != nil {
		return err
	}
	if cut := now.AddDate(0, 0, -r.RawDays).Truncate(time.Hour); ok5 {
		if m5.Before(cut) {
			cut = m5
		}
		if _, err := d.pool.Exec(ctx, `DELETE FROM samples_raw WHERE hour < $1`, cut); err != nil {
			return err
		}
	}
	if cut := dayStart(now.AddDate(0, 0, -r.FiveMinDays)); ok1 {
		if m1.Before(cut) {
			cut = m1
		}
		if _, err := d.pool.Exec(ctx, `DELETE FROM samples_5m WHERE day < $1`, cut); err != nil {
			return err
		}
	}
	_, err = d.pool.Exec(ctx, `DELETE FROM samples_1h WHERE month < $1`, monthStart(now.AddDate(0, 0, -r.HourDays)))
	return err
}

// Forget deletes all samples of series (their check was deleted).
func (d *DB) Forget(ctx context.Context, ids []int32) error {
	if len(ids) == 0 {
		return nil
	}
	for _, t := range []string{"samples_hot", "samples_raw", "samples_5m", "samples_1h"} {
		if _, err := d.pool.Exec(ctx, `DELETE FROM `+t+` WHERE series_id = ANY($1)`, ids); err != nil {
			return err
		}
	}
	return nil
}

// ---- reading

// Result is a series between two times: points of Step seconds (a raw
// result has Step 0 and one point per sample).
type Result struct {
	Step   int64 `json:"step"`
	Points []Agg `json:"-"`
}

// Query returns at most about maxPts points of a series between from and
// to, from the coarsest resolution that still gives that many.
func (d *DB) Query(ctx context.Context, id int32, from, to time.Time, maxPts int) (Result, error) {
	from, to = from.UTC(), to.UTC()
	if !to.After(from) {
		return Result{}, errors.New("empty time range")
	}
	if maxPts < 10 {
		maxPts = 10
	}
	span := int64(to.Sub(from) / time.Second)
	step := (span + int64(maxPts) - 1) / int64(maxPts)
	r := d.retention()
	now := d.now().UTC()
	var pts []Agg
	var err error
	switch {
	case step >= 3600 || from.Before(now.AddDate(0, 0, -r.FiveMinDays)):
		pts, err = d.fetch1h(ctx, id, from, to)
		step = max(step, 3600)
	case step >= 300 || from.Before(now.AddDate(0, 0, -r.RawDays)):
		pts, err = d.fetch5m(ctx, id, from, to)
		step = max(step, 300)
	default:
		pts, err = d.fetchRaw(ctx, id, from, to)
		if len(pts) <= maxPts {
			return Result{Points: pts}, err
		}
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Step: step, Points: Downsample(pts, step)}, nil
}

func clip(a []Agg, from, to time.Time) []Agg {
	f, t := from.Unix(), to.Unix()
	out := a[:0]
	for _, p := range a {
		if p.T >= f && p.T < t {
			out = append(out, p)
		}
	}
	return out
}

func (d *DB) blocks(ctx context.Context, table, key string, id int32, from, to time.Time) ([]Agg, error) {
	rows, err := d.pool.Query(ctx, `SELECT block FROM `+table+` WHERE series_id=$1 AND `+key+` >= $2 AND `+key+` < $3 ORDER BY `+key, id, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agg
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		a, err := decodeAggs(b)
		if err != nil {
			continue
		}
		out = append(out, a...)
	}
	return out, rows.Err()
}

func (d *DB) fetchRaw(ctx context.Context, id int32, from, to time.Time) ([]Agg, error) {
	pts, err := d.blocks(ctx, "samples_raw", "hour", id, from.Truncate(time.Hour), to)
	if err != nil {
		return nil, err
	}
	rows, err := d.pool.Query(ctx, `SELECT ts, value FROM samples_hot WHERE series_id=$1 AND ts >= $2 AND ts < $3 ORDER BY ts`, id, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t time.Time
		var v float64
		if err := rows.Scan(&t, &v); err != nil {
			return nil, err
		}
		pts = append(pts, Agg{T: t.Unix(), Min: v, Max: v, Avg: v, N: 1})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(pts, func(a, b int) bool { return pts[a].T < pts[b].T })
	return clip(pts, from, to), nil
}

// fetchTier reads the stored summaries of a tier and computes the part
// that is not rolled up yet from the finer data.
func (d *DB) fetchTier(ctx context.Context, t tier, id int32, from, to time.Time, finer func(context.Context, int32, time.Time, time.Time) ([]Agg, error)) ([]Agg, error) {
	upto, ok, err := getMark(ctx, d.pool, t.name)
	if err != nil {
		return nil, err
	}
	var pts []Agg
	if ok && from.Before(upto) {
		pts, err = d.blocks(ctx, t.table, t.keyCol, id, t.period(from), minTime(to, upto))
		if err != nil {
			return nil, err
		}
		pts = clip(pts, from, to)
	}
	rest := from
	if ok && upto.After(rest) {
		rest = upto
	}
	if rest.Before(to) {
		f, err := finer(ctx, id, rest, to)
		if err != nil {
			return nil, err
		}
		pts = append(pts, Downsample(f, t.step)...)
	}
	return pts, nil
}

func (d *DB) fetch5m(ctx context.Context, id int32, from, to time.Time) ([]Agg, error) {
	return d.fetchTier(ctx, tier5m, id, from, to, d.fetchRaw)
}

func (d *DB) fetch1h(ctx context.Context, id int32, from, to time.Time) ([]Agg, error) {
	return d.fetchTier(ctx, tier1h, id, from, to, d.fetch5m)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// ---- small helpers

func (d *DB) withTx(ctx context.Context, f func(pgx.Tx) error) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Usage describes the stored data (Settings → Metric data).
type Usage struct {
	Series  int64
	Hot     int64 // samples not packed yet
	Bytes   int64 // on disk, with indexes
	Samples int64 // samples written in the last hour
}

// Usage measures the metric tables.
func (d *DB) Usage(ctx context.Context) (Usage, error) {
	var u Usage
	err := d.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM series),
		(SELECT count(*) FROM samples_hot),
		(SELECT COALESCE(sum(pg_total_relation_size(c.oid)),0)::bigint FROM pg_class c WHERE c.relkind IN ('r','p') AND c.relname LIKE 'samples\_%'),
		(SELECT count(*) FROM samples_hot WHERE ts > now() - interval '1 hour')`).Scan(&u.Series, &u.Hot, &u.Bytes, &u.Samples)
	return u, err
}
