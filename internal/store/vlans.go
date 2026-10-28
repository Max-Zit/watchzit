package store

import (
	"context"
	"errors"
	"sort"
	"strconv"
)

// Sources of VLAN information.
const (
	VLANFromSNMP     = "snmp"
	VLANFromRouterOS = "routeros"
	VLANFromAgent    = "agent"
)

// VLANPort is one VLAN on one port of a device (IfIndex 0: the device knows
// the VLAN but no port carries it).
type VLANPort struct {
	VLAN    int    `json:"vlan"`
	Name    string `json:"name,omitempty"`
	IfIndex int    `json:"if_index,omitempty"`
	Tagged  bool   `json:"tagged,omitempty"`
}

// PortVLAN is a VLAN as the map shows it on a port.
type PortVLAN struct {
	ID     int  `json:"id"`
	Tagged bool `json:"tagged,omitempty"`
}

// VLANSummary is a VLAN of a device with the names of its ports.
type VLANSummary struct {
	ID       int
	Name     string
	Tagged   []string
	Untagged []string
}

// SetDeviceVLANs replaces what one source knows about the VLANs of a device.
func (s *Store) SetDeviceVLANs(ctx context.Context, deviceID int64, source string, vs []VLANPort) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM device_vlans WHERE device_id=$1 AND source=$2`, deviceID, source); err != nil {
		return err
	}
	now, seen := s.now(), map[[2]int]bool{}
	for _, v := range vs {
		key := [2]int{v.VLAN, v.IfIndex}
		if v.VLAN < 1 || v.VLAN > 4094 || v.IfIndex < 0 || seen[key] {
			continue
		}
		seen[key] = true
		if _, err := tx.Exec(ctx, `INSERT INTO device_vlans(device_id, source, vlan_id, if_index, name, tagged, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7)`,
			deviceID, source, v.VLAN, v.IfIndex, truncate(v.Name, 100), v.Tagged, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeviceVLANs lists the VLANs of a device with their tagged and untagged
// ports, by VLAN number.
func (s *Store) DeviceVLANs(ctx context.Context, deviceID int64) ([]VLANSummary, error) {
	rows, err := s.db.Query(ctx, `SELECT v.vlan_id, v.name, v.tagged, v.if_index, COALESCE(NULLIF(i.name,''), i.descr, '')
		FROM device_vlans v LEFT JOIN device_interfaces i ON i.device_id=v.device_id AND i.if_index=v.if_index
		WHERE v.device_id=$1 ORDER BY v.vlan_id, v.if_index`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int]*VLANSummary{}
	var out []*VLANSummary
	for rows.Next() {
		var id, idx int
		var name, port string
		var tagged bool
		if err := rows.Scan(&id, &name, &tagged, &idx, &port); err != nil {
			return nil, err
		}
		v := byID[id]
		if v == nil {
			v = &VLANSummary{ID: id}
			byID[id] = v
			out = append(out, v)
		}
		if v.Name == "" {
			v.Name = name
		}
		if idx == 0 {
			continue
		}
		if port == "" {
			port = "#" + strconv.Itoa(idx)
		}
		if tagged {
			v.Tagged = appendNew(v.Tagged, port)
		} else {
			v.Untagged = appendNew(v.Untagged, port)
		}
	}
	res := make([]VLANSummary, len(out))
	for i, v := range out {
		res[i] = *v
	}
	return res, rows.Err()
}

// portVLANs adds the VLANs of the ports to live ports and returns the names
// of all VLANs seen.
func (s *Store) portVLANs(ctx context.Context, ids []int64, ports map[int64]map[int]*LivePort) (map[int]string, error) {
	rows, err := s.db.Query(ctx, `SELECT device_id, vlan_id, if_index, name, tagged FROM device_vlans WHERE device_id = ANY($1) ORDER BY vlan_id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := map[int]string{}
	for rows.Next() {
		var dev int64
		var id, idx int
		var name string
		var tagged bool
		if err := rows.Scan(&dev, &id, &idx, &name, &tagged); err != nil {
			return nil, err
		}
		if names[id] == "" {
			names[id] = name
		}
		if p := ports[dev][idx]; p != nil && idx != 0 {
			dup := false
			for _, x := range p.VLANs {
				dup = dup || x.ID == id
			}
			if !dup {
				p.VLANs = append(p.VLANs, PortVLAN{ID: id, Tagged: tagged})
			}
		}
	}
	for _, m := range ports {
		for _, p := range m {
			sort.Slice(p.VLANs, func(i, j int) bool { return p.VLANs[i].ID < p.VLANs[j].ID })
		}
	}
	return names, rows.Err()
}

func appendNew(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// cleanLinkVLANs checks the VLANs set by hand on a map cable: numbers
// 1–4094, each once, at most one untagged, at most 64.
func cleanLinkVLANs(in []PortVLAN) ([]PortVLAN, error) {
	if len(in) > 64 {
		return nil, errors.New("a cable can have at most 64 VLANs")
	}
	out, seen, untagged := []PortVLAN{}, map[int]bool{}, 0
	for _, v := range in {
		if v.ID < 1 || v.ID > 4094 {
			return nil, errors.New("a VLAN is a number from 1 to 4094")
		}
		if seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		if !v.Tagged {
			untagged++
		}
		out = append(out, v)
	}
	if untagged > 1 {
		return nil, errors.New("a cable carries at most one untagged VLAN")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
