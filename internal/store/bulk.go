package store

import (
	"context"
	"errors"
	"slices"
)

// Bulk changes of devices (the device list's "Change selected").

// Bulk fields.
const (
	BulkGroup  = "group"  // value: group ID (0 = none)
	BulkSNMP   = "snmp"   // value: SNMP profile ID (0 = none)
	BulkParent = "parent" // value: device it depends on (0 = none)
	BulkKind   = "kind"   // text: device kind
	BulkPause  = "pause"  // value 1 = paused, 0 = monitored
)

// BulkUpdate changes one field of many devices; it returns how many changed.
func (s *Store) BulkUpdate(ctx context.Context, ids []int64, field string, value int64, text string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if len(ids) > 5000 {
		return 0, errors.New("too many devices at once")
	}
	var q string
	var arg any
	switch field {
	case BulkGroup:
		q, arg = `UPDATE devices SET group_id=$2 WHERE id = ANY($1)`, nullID(value)
	case BulkSNMP:
		q, arg = `UPDATE devices SET snmp_profile_id=$2 WHERE id = ANY($1)`, nullID(value)
	case BulkKind:
		if !slices.Contains(DeviceKinds, text) {
			return 0, errors.New("unknown device type")
		}
		q, arg = `UPDATE devices SET kind=$2 WHERE id = ANY($1)`, text
	case BulkPause:
		q, arg = `UPDATE devices SET paused=$2 WHERE id = ANY($1)`, value == 1
	case BulkParent:
		// each device on its own: none may depend on itself or on a device
		// that depends on it
		n := 0
		for _, id := range ids {
			if value != 0 {
				if err := s.checkParent(ctx, id, value); err != nil {
					return n, err
				}
			}
			tag, err := s.db.Exec(ctx, `UPDATE devices SET parent_id=$2 WHERE id=$1`, id, nullID(value))
			if isFK(err) {
				return n, errors.New("the group, SNMP profile or parent device does not exist")
			}
			if err != nil {
				return n, err
			}
			n += int(tag.RowsAffected())
		}
		return n, nil
	default:
		return 0, errors.New("unknown change")
	}
	tag, err := s.db.Exec(ctx, q, ids, arg)
	if isFK(err) {
		return 0, errors.New("the group, SNMP profile or parent device does not exist")
	}
	if err != nil {
		return 0, err
	}
	if field == BulkPause {
		for _, id := range ids {
			if err := s.RefreshDeviceStatus(ctx, id); err != nil {
				return int(tag.RowsAffected()), err
			}
		}
	}
	return int(tag.RowsAffected()), nil
}
