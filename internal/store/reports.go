package store

import (
	"context"
	"time"
)

// AvailabilityRow is one device in an availability report.
type AvailabilityRow struct {
	DeviceID  int64
	Name      string
	Group     string
	Address   string
	Kind      string
	CreatedAt time.Time
	Down      time.Duration // unplanned: "device down" problems, also when unreachable
	Planned   time.Duration // device down during maintenance
	Problems  int           // problems that started in the period
	PingRTT   int32         // the series of its ping time (0 = none)
}

// Availability measures devices over a period from their "device down"
// problems (without a check; HA-group and log problems do not count).
func (s *Store) Availability(ctx context.Context, from, to time.Time, groupID int64) ([]AvailabilityRow, error) {
	rows, err := s.db.Query(ctx, `SELECT d.id, d.name, COALESCE(g.name,''), d.address, d.kind, d.created_at,
		COALESCE(sum(EXTRACT(EPOCH FROM LEAST(COALESCE(p.resolved_at,$2),$2) - GREATEST(p.started_at,$1))) FILTER (WHERE p.check_id IS NULL AND p.key NOT LIKE '%:ha%' AND p.key NOT LIKE '%:log' AND p.suppressed <> 'maintenance'),0),
		COALESCE(sum(EXTRACT(EPOCH FROM LEAST(COALESCE(p.resolved_at,$2),$2) - GREATEST(p.started_at,$1))) FILTER (WHERE p.check_id IS NULL AND p.key NOT LIKE '%:ha%' AND p.key NOT LIKE '%:log' AND p.suppressed = 'maintenance'),0),
		count(p.id) FILTER (WHERE p.started_at >= $1),
		COALESCE((SELECT s.id FROM series s JOIN checks c ON c.id=s.check_id WHERE c.device_id=d.id AND c.type='icmp' AND s.metric='rtt_ms' ORDER BY c.id LIMIT 1),0)
		FROM devices d LEFT JOIN device_groups g ON g.id=d.group_id
		LEFT JOIN problems p ON p.device_id=d.id AND p.started_at < $2 AND COALESCE(p.resolved_at,$2) > $1
		WHERE ($3 = 0 OR d.group_id = $3) AND d.created_at <= $2
		GROUP BY d.id, g.name ORDER BY lower(d.name)`, from, to, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AvailabilityRow
	for rows.Next() {
		var r AvailabilityRow
		var down, planned float64
		if err := rows.Scan(&r.DeviceID, &r.Name, &r.Group, &r.Address, &r.Kind, &r.CreatedAt, &down, &planned, &r.Problems, &r.PingRTT); err != nil {
			return nil, err
		}
		r.Down, r.Planned = time.Duration(down*float64(time.Second)), time.Duration(planned*float64(time.Second))
		out = append(out, r)
	}
	return out, rows.Err()
}
