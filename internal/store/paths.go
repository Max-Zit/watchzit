package store

import (
	"context"
	"encoding/json"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
)

// Network path checks (traceroute) keep each run's hops for 30 days: the
// device page shows the path now, each hop over the last day and when the
// path changed.

// PathKeep is how long the runs of a path check are kept.
const PathKeep = 30 * 24 * time.Hour

// PathHop is one hop of a traced path. Addr is empty when nothing answered.
type PathHop struct {
	TTL   int     `json:"t"`
	Addr  string  `json:"a,omitempty"`
	Other int     `json:"o,omitempty"` // answers from other addresses (equal-cost paths)
	Sent  int     `json:"s"`
	Recv  int     `json:"r"`
	Best  float64 `json:"b,omitempty"` // ms
	Avg   float64 `json:"v,omitempty"`
	Worst float64 `json:"w,omitempty"`
	Final bool    `json:"f,omitempty"` // the destination
}

// Loss is the share of probes without an answer, in percent.
func (h PathHop) Loss() float64 {
	if h.Sent == 0 {
		return 100
	}
	return float64(h.Sent-h.Recv) / float64(h.Sent) * 100
}

// PathRun is one stored run.
type PathRun struct {
	At      time.Time
	Changed bool
	Hops    []PathHop
}

// CleanPath drops what a path from a collector may not carry.
func CleanPath(hops []PathHop) []PathHop {
	if len(hops) > 64 {
		hops = hops[:64]
	}
	out := make([]PathHop, 0, len(hops))
	for i, h := range hops {
		h.TTL = i + 1
		if h.Addr != "" {
			a, err := netip.ParseAddr(h.Addr)
			if err != nil {
				h.Addr = ""
			} else {
				h.Addr = a.Unmap().String()
			}
		}
		h.Sent, h.Recv, h.Other = min(max(h.Sent, 0), 10), min(max(h.Recv, 0), 10), min(max(h.Other, 0), 10)
		out = append(out, h)
	}
	return out
}

func savePath(ctx context.Context, tx pgx.Tx, r CheckResult) error {
	raw, _ := json.Marshal(r.Path)
	if _, err := tx.Exec(ctx, `INSERT INTO path_runs(check_id, at, changed, hops) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
		r.CheckID, r.At, r.Values["path_changed"] == 1, raw); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM path_runs WHERE check_id=$1 AND at < $2`, r.CheckID, r.At.Add(-PathKeep))
	return err
}

// PathNow is the last run of a path check (nil when none).
func (s *Store) PathNow(ctx context.Context, checkID int64) (*PathRun, error) {
	rs, err := s.pathRuns(ctx, `SELECT at, changed, hops FROM path_runs WHERE check_id=$1 ORDER BY at DESC LIMIT 1`, checkID)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return &rs[0], nil
}

// PathChanges are the last runs where the path changed, newest first, each
// with the run before it.
func (s *Store) PathChanges(ctx context.Context, checkID int64, n int) ([][2]PathRun, error) {
	changed, err := s.pathRuns(ctx, `SELECT at, changed, hops FROM path_runs WHERE check_id=$1 AND changed ORDER BY at DESC LIMIT $2`, checkID, n)
	if err != nil {
		return nil, err
	}
	var out [][2]PathRun
	for _, c := range changed {
		before, err := s.pathRuns(ctx, `SELECT at, changed, hops FROM path_runs WHERE check_id=$1 AND at < $2 ORDER BY at DESC LIMIT 1`, checkID, c.At)
		if err != nil {
			return nil, err
		}
		pair := [2]PathRun{c}
		if len(before) == 1 {
			pair[1] = before[0]
		}
		out = append(out, pair)
	}
	return out, nil
}

// HopStat is one hop over a time range.
type HopStat struct {
	TTL   int
	Addr  string
	Runs  int     // runs that had this address at this hop
	Loss  float64 // percent of its probes
	Avg   float64 // ms, average of the runs' averages
	Worst float64 // ms
}

// PathHops sums up each hop (TTL and address) of the runs since a time.
func (s *Store) PathHops(ctx context.Context, checkID int64, since time.Time) ([]HopStat, error) {
	rows, err := s.db.Query(ctx, `SELECT (h->>'t')::int, COALESCE(h->>'a',''), count(*), sum((h->>'s')::int), sum((h->>'r')::int),
			COALESCE(avg((h->>'v')::float8) FILTER (WHERE (h->>'r')::int > 0), 0), COALESCE(max((h->>'w')::float8), 0)
		FROM path_runs, jsonb_array_elements(hops) h WHERE check_id=$1 AND at >= $2
		GROUP BY 1, 2 ORDER BY 1, 3 DESC LIMIT 200`, checkID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HopStat
	for rows.Next() {
		var h HopStat
		var sent, recv int
		if err := rows.Scan(&h.TTL, &h.Addr, &h.Runs, &sent, &recv, &h.Avg, &h.Worst); err != nil {
			return nil, err
		}
		if sent > 0 {
			h.Loss = float64(sent-recv) / float64(sent) * 100
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) pathRuns(ctx context.Context, q string, args ...any) ([]PathRun, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PathRun
	for rows.Next() {
		var r PathRun
		var raw []byte
		if err := rows.Scan(&r.At, &r.Changed, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &r.Hops)
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeviceRef names a device.
type DeviceRef struct {
	ID   int64
	Name string
}

// DevicesAt are the devices whose address is one of addrs (hops of a path
// that are monitored too).
func (s *Store) DevicesAt(ctx context.Context, addrs []string) (map[string]*DeviceRef, error) {
	out := map[string]*DeviceRef{}
	if len(addrs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT id, name, address FROM devices WHERE address = ANY($1) ORDER BY id`, addrs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r DeviceRef
		var a string
		if err := rows.Scan(&r.ID, &r.Name, &a); err != nil {
			return nil, err
		}
		if _, ok := out[a]; !ok {
			out[a] = &r
		}
	}
	return out, rows.Err()
}
