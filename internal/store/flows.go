package store

import (
	"context"
	"net/netip"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Traffic flows: minutes of NetFlow, IPFIX and sFlow, summed by
// conversation (source, destination, protocol, service port).

// FlowRow is one conversation of one minute (Src and Dst invalid: the
// rest of that minute).
type FlowRow struct {
	At             time.Time
	Exporter       netip.Addr
	DeviceID       int64 // the exporter's device (0: unknown)
	Src, Dst       netip.Addr
	Proto, Port    int
	Bytes, Packets int64
}

func addrArg(a netip.Addr) any {
	if !a.IsValid() {
		return nil
	}
	return a
}

// AddFlows stores the rows of a minute.
func (s *Store) AddFlows(ctx context.Context, rows []FlowRow) error {
	if len(rows) == 0 {
		return nil
	}
	_, err := s.db.CopyFrom(ctx, pgx.Identifier{"flow_minutes"}, []string{"at", "exporter", "device_id", "src", "dst", "proto", "port", "bytes", "packets"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			r := rows[i]
			var dev any
			if r.DeviceID > 0 {
				dev = r.DeviceID
			}
			return []any{r.At, r.Exporter, dev, addrArg(r.Src), addrArg(r.Dst), int16(r.Proto), r.Port, r.Bytes, r.Packets}, nil // #nosec G115 -- a protocol number
		}))
	if isFK(err) {
		// a device was removed meanwhile: store the minute without devices
		for i := range rows {
			rows[i].DeviceID = 0
		}
		return s.AddFlows(ctx, rows)
	}
	return err
}

// PruneFlows removes flows older than before.
func (s *Store) PruneFlows(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM flow_minutes WHERE at < $1`, before)
	return tag.RowsAffected(), err
}

// FlowFilter chooses flows: of one exporter device (0: all) in a time
// range.
type FlowFilter struct {
	DeviceID int64
	From, To time.Time
}

func (f FlowFilter) where() (string, []any) {
	q, args := `at >= $1 AND at < $2`, []any{f.From, f.To}
	if f.DeviceID > 0 {
		q += ` AND device_id = $3`
		args = append(args, f.DeviceID)
	}
	return q, args
}

// FlowTop is a row of a top list.
type FlowTop struct {
	Src, Dst       netip.Addr // by conversation; Src alone for sources, Dst alone for destinations
	Exporter       netip.Addr // exporters
	Proto, Port    int
	DeviceID       int64 // exporters
	Bytes, Packets int64
}

// Flow top lists.
const (
	FlowBySource       = "src"
	FlowByDestination  = "dst"
	FlowByConversation = "conv"
	FlowByService      = "service"
	FlowByExporter     = "exporter"
)

// FlowTops sums the flows by one of the Flow* groupings, the largest first.
func (s *Store) FlowTops(ctx context.Context, f FlowFilter, by string, limit int) ([]FlowTop, error) {
	where, args := f.where()
	var cols string
	switch by {
	case FlowBySource:
		cols, where = `src, NULL::inet, 0, 0, 0, NULL::inet`, where+` AND src IS NOT NULL`
	case FlowByDestination:
		cols, where = `NULL::inet, dst, 0, 0, 0, NULL::inet`, where+` AND dst IS NOT NULL`
	case FlowByConversation:
		cols, where = `src, dst, proto, port, 0, NULL::inet`, where+` AND src IS NOT NULL`
	case FlowByService:
		cols, where = `NULL::inet, NULL::inet, proto, port, 0, NULL::inet`, where+` AND src IS NOT NULL`
	default:
		cols = `NULL::inet, NULL::inet, 0, 0, COALESCE(device_id, 0), exporter`
	}
	args = append(args, limit)
	rows, err := s.db.Query(ctx, `SELECT `+cols+`, sum(bytes)::bigint, sum(packets)::bigint FROM flow_minutes WHERE `+where+
		` GROUP BY 1, 2, 3, 4, 5, 6 ORDER BY 7 DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FlowTop
	for rows.Next() {
		var t FlowTop
		var src, dst, exp *netip.Addr
		if err := rows.Scan(&src, &dst, &t.Proto, &t.Port, &t.DeviceID, &exp, &t.Bytes, &t.Packets); err != nil {
			return nil, err
		}
		if src != nil {
			t.Src = *src
		}
		if dst != nil {
			t.Dst = *dst
		}
		if exp != nil {
			t.Exporter = *exp
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// FlowTotal is all traffic in the range.
func (s *Store) FlowTotal(ctx context.Context, f FlowFilter) (bytes, packets int64, err error) {
	where, args := f.where()
	err = s.db.QueryRow(ctx, `SELECT COALESCE(sum(bytes),0)::bigint, COALESCE(sum(packets),0)::bigint FROM flow_minutes WHERE `+where, args...).Scan(&bytes, &packets)
	return
}

// FlowRate is the traffic per step (bytes), from the start of each step.
func (s *Store) FlowRate(ctx context.Context, f FlowFilter, step time.Duration) (map[int64]int64, error) {
	where, args := f.where()
	args = append(args, int64(step/time.Second))
	rows, err := s.db.Query(ctx, `SELECT (floor(extract(epoch FROM at) / $`+strconv.Itoa(len(args))+`) * $`+strconv.Itoa(len(args))+`)::bigint, sum(bytes)::bigint
		FROM flow_minutes WHERE `+where+` GROUP BY 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var t, b int64
		if err := rows.Scan(&t, &b); err != nil {
			return nil, err
		}
		out[t] = b
	}
	return out, rows.Err()
}

// FlowExporterIDs lists the devices that sent flows since t.
func (s *Store) FlowExporterIDs(ctx context.Context, since time.Time) ([]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT device_id FROM flow_minutes WHERE at >= $1 AND device_id IS NOT NULL`, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// HasFlows tells whether a device sent flows since t.
func (s *Store) HasFlows(ctx context.Context, deviceID int64, since time.Time) bool {
	var ok bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM flow_minutes WHERE device_id=$1 AND at >= $2)`, deviceID, since).Scan(&ok)
	return ok
}
