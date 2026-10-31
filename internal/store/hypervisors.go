package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/max-zit/watchzit/internal/hypervisor"
)

// Hypervisor inventories: the hosts, machines and datastores a hypervisor
// check or a Hyper-V agent read last, one per device.

// HVInventory is the stored inventory of a device.
type HVInventory struct {
	DeviceID int64
	Source   string
	At       time.Time
	hypervisor.Inventory
}

// SetHVInventory stores what a device's hypervisor runs.
func (s *Store) SetHVInventory(ctx context.Context, deviceID int64, at time.Time, inv *hypervisor.Inventory) error {
	data, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO hv_inventory(device_id, source, at, data) VALUES($1,$2,$3,$4)
		ON CONFLICT (device_id) DO UPDATE SET source=EXCLUDED.source, at=EXCLUDED.at, data=EXCLUDED.data`, deviceID, inv.Kind, at, data)
	return err
}

// HVInventoryOf is the inventory of a device (nil when it has none).
func (s *Store) HVInventoryOf(ctx context.Context, deviceID int64) (*HVInventory, error) {
	inv := &HVInventory{DeviceID: deviceID}
	var data []byte
	err := s.db.QueryRow(ctx, `SELECT source, at, data FROM hv_inventory WHERE device_id=$1`, deviceID).Scan(&inv.Source, &inv.At, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &inv.Inventory); err != nil {
		return nil, err
	}
	return inv, nil
}

// DeleteHVInventory forgets the inventory of a device (its check is gone).
func (s *Store) DeleteHVInventory(ctx context.Context, deviceID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM hv_inventory WHERE device_id=$1`, deviceID)
	return err
}

func (s *Store) hvInventories(ctx context.Context) ([]*HVInventory, error) {
	rows, err := s.db.Query(ctx, `SELECT device_id, source, at, data FROM hv_inventory ORDER BY device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*HVInventory
	for rows.Next() {
		inv := &HVInventory{}
		var data []byte
		if err := rows.Scan(&inv.DeviceID, &inv.Source, &inv.At, &data); err != nil {
			return nil, err
		}
		if json.Unmarshal(data, &inv.Inventory) == nil {
			out = append(out, inv)
		}
	}
	return out, rows.Err()
}

// LiveVM is a machine on a map.
type LiveVM struct {
	Name   string  `json:"name"`
	State  string  `json:"state"`
	Kind   string  `json:"kind,omitempty"`
	Pic    string  `json:"pic"`
	IP     string  `json:"ip,omitempty"`
	CPUPct float64 `json:"cpu_pct"`
	MemPct float64 `json:"mem_pct"`
	Host   string  `json:"host,omitempty"`
	Device int64   `json:"device,omitempty"` // the console device with its name or address
}

// sameName compares host names, also the short name with a full one.
func sameName(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSuffix(a, ".")), strings.ToLower(strings.TrimSuffix(b, "."))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	short := func(s string) string {
		if _, err := netip.ParseAddr(s); err == nil {
			return s
		}
		return strings.SplitN(s, ".", 2)[0]
	}
	return short(a) == short(b)
}

// mapVMs finds the machines of each hypervisor host of a map: those of the
// inventory hosts that are one of its physical servers (by address or
// name), or all of an inventory of a single host that is read from one of
// them.
func (s *Store) mapVMs(ctx context.Context, l *MapLayout, devs map[int64]*LiveDevice) (map[int64][]LiveVM, error) {
	var hosts []MapNode
	for _, n := range l.Nodes {
		if n.Kind == NodeHost && n.HostDevice > 0 {
			hosts = append(hosts, n)
		}
	}
	if len(hosts) == 0 {
		return nil, nil
	}
	invs, err := s.hvInventories(ctx)
	if err != nil || len(invs) == 0 {
		return nil, err
	}
	type dev struct {
		id         int64
		name, addr string
	}
	var all []dev
	rows, err := s.db.Query(ctx, `SELECT id, name, address FROM devices`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d dev
		if err := rows.Scan(&d.id, &d.name, &d.addr); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, d)
	}
	rows.Close()
	deviceOf := func(v hypervisor.VM) int64 {
		for _, d := range all {
			for _, ip := range v.IPs {
				if d.addr == ip {
					return d.id
				}
			}
		}
		for _, d := range all {
			if strings.EqualFold(d.name, v.Name) {
				return d.id
			}
		}
		return 0
	}
	out := map[int64][]LiveVM{}
	for _, n := range hosts {
		servers := append([]int64{n.HostDevice}, n.HostDevices...)
		var vms []LiveVM
		for _, inv := range invs {
			mine := map[string]bool{}
			for _, h := range inv.Hosts {
				for _, id := range servers {
					d := devs[id]
					if d == nil {
						continue
					}
					if (len(inv.Hosts) == 1 && inv.DeviceID == id) || (h.Address != "" && h.Address == d.Address) || sameName(h.Name, d.Name) || sameName(h.Name, d.Address) {
						mine[h.Name] = true
					}
				}
			}
			for _, v := range inv.VMs {
				if !mine[v.Host] || v.Template {
					continue
				}
				lv := LiveVM{Name: v.Name, State: v.State, Kind: v.Kind, Pic: hypervisor.Picture(v), CPUPct: v.CPUPct, MemPct: v.MemPct, Device: deviceOf(v)}
				if inv.Kind == hypervisor.Docker {
					lv.Pic = "ct-docker"
				}
				if len(n.HostDevices) > 0 {
					lv.Host = v.Host
				}
				if len(v.IPs) > 0 {
					lv.IP = v.IPs[0]
				}
				vms = append(vms, lv)
			}
		}
		if len(vms) > 0 {
			out[n.ID] = vms
		}
	}
	return out, nil
}
