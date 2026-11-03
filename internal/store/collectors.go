package store

import (
	"context"
	"errors"
)

// Remote collectors: agents that run the checks of devices in their own
// network (see poller.NewCollector).

// SetCollector turns an agent into a remote collector or back. Devices it
// ran go back to the console.
func (s *Store) SetCollector(ctx context.Context, deviceID int64, on bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE devices SET collector=$2 WHERE id=$1 AND agent_key IS NOT NULL`, deviceID, on)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("only a device with the WatchZit agent can be a remote collector")
	}
	if !on {
		if _, err := tx.Exec(ctx, `UPDATE devices SET collector_id=NULL WHERE collector_id=$1`, deviceID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Collectors lists the remote collectors.
func (s *Store) Collectors(ctx context.Context) ([]*Device, error) {
	rows, err := s.db.Query(ctx, `SELECT `+deviceCols+` FROM devices d LEFT JOIN device_groups g ON g.id=d.group_id WHERE d.collector ORDER BY lower(d.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SetDeviceCollector chooses where the checks of devices run: a remote
// collector, or 0 for the console. It returns how many changed.
func (s *Store) SetDeviceCollector(ctx context.Context, ids []int64, collectorID int64) (int, error) {
	if len(ids) > 5000 {
		return 0, errors.New("too many devices at once")
	}
	if collectorID != 0 {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT collector FROM devices WHERE id=$1`, collectorID).Scan(&ok); err != nil || !ok {
			return 0, errors.New("that device is not a remote collector")
		}
	}
	tag, err := s.db.Exec(ctx, `UPDATE devices SET collector_id=$2 WHERE id = ANY($1) AND id <> COALESCE($2, 0)`, ids, nullID(collectorID))
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// CollectorChecks are the checks a remote collector runs.
func (s *Store) CollectorChecks(ctx context.Context, collectorID int64) ([]*Check, error) {
	return s.checks(ctx, `WHERE d.collector_id=$1`, collectorID)
}

// CollectorDevices counts the devices of each collector.
func (s *Store) CollectorDevices(ctx context.Context) (map[int64]int, error) {
	rows, err := s.db.Query(ctx, `SELECT collector_id, count(*) FROM devices WHERE collector_id IS NOT NULL GROUP BY collector_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// RemoteCheckSeries tells which series belong to the checks of a collector
// (results it sends are taken only for these).
func (s *Store) RemoteCheckSeries(ctx context.Context, collectorID int64) (map[int64]map[int32]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id, COALESCE(s.id,0) FROM checks c JOIN devices d ON d.id=c.device_id LEFT JOIN series s ON s.check_id=c.id WHERE d.collector_id=$1`, collectorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[int32]bool{}
	for rows.Next() {
		var cid int64
		var sid int32
		if err := rows.Scan(&cid, &sid); err != nil {
			return nil, err
		}
		if out[cid] == nil {
			out[cid] = map[int32]bool{}
		}
		if sid != 0 {
			out[cid][sid] = true
		}
	}
	return out, rows.Err()
}
