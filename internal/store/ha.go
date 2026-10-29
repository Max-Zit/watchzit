package store

import (
	"context"
	"time"
)

// HAGroup is a HA group of a map with the devices of its members, for the
// alerts: a member is a device, or the physical server of a hypervisor.
type HAGroup struct {
	ID      int64
	MapID   int64
	Map     string
	Name    string
	Kind    string
	Members []int64 // device IDs, one per member that has a device
	Size    int     // members on the map
}

// HAGroups lists the HA groups of all maps.
func (s *Store) HAGroups(ctx context.Context) ([]HAGroup, error) {
	rows, err := s.db.Query(ctx, `SELECT g.id, g.map_id, m.name, g.name, g.kind, cardinality(g.nodes),
		COALESCE((SELECT array_agg(COALESCE(n.device_id, n.host_device) ORDER BY n.id) FROM map_nodes n
			WHERE n.id = ANY(g.nodes) AND COALESCE(n.device_id, n.host_device) IS NOT NULL), '{}')
		FROM map_ha g JOIN maps m ON m.id = g.map_id ORDER BY g.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HAGroup
	for rows.Next() {
		var g HAGroup
		if err := rows.Scan(&g.ID, &g.MapID, &g.Map, &g.Name, &g.Kind, &g.Size, &g.Members); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// HA roles a device reports.
const (
	HAMaster = "master"
	HABackup = "backup"
)

// SetHAState stores the HA role a device reports ("" when it has none);
// the time changes only with the role.
func (s *Store) SetHAState(ctx context.Context, deviceID int64, state, source string) error {
	if state != HAMaster && state != HABackup {
		state, source = "", ""
	}
	_, err := s.db.Exec(ctx, `UPDATE devices SET ha_changed = CASE WHEN ha_state IS DISTINCT FROM $2 THEN $4 ELSE ha_changed END,
		ha_state=$2, ha_source=$3 WHERE id=$1`, deviceID, state, source, s.now())
	return err
}

// HARole is the HA role a device reports.
type HARole struct {
	State  string // master or backup
	Source string // vrrp (SNMP) or routeros
	Since  *time.Time
}

// DeviceHA returns the HA role of a device (nil when it reports none).
func (s *Store) DeviceHA(ctx context.Context, deviceID int64) (*HARole, error) {
	r := &HARole{}
	if err := s.db.QueryRow(ctx, `SELECT ha_state, ha_source, ha_changed FROM devices WHERE id=$1`, deviceID).Scan(&r.State, &r.Source, &r.Since); err != nil {
		return nil, notFound(err)
	}
	if r.State == "" {
		return nil, nil
	}
	return r, nil
}
