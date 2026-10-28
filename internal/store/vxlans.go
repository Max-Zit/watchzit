package store

import (
	"context"
	"net/netip"
	"sort"
	"strconv"
)

// MaxVNI is the highest VXLAN network identifier (24 bits).
const MaxVNI = 1<<24 - 1

// VXLAN is a VXLAN interface of a device: the device is a VTEP. RouterOS
// and the agents report them (sources VLANFromRouterOS, VLANFromAgent).
type VXLAN struct {
	VNI     int      `json:"vni"`
	Name    string   `json:"name,omitempty"`    // interface
	Local   string   `json:"local,omitempty"`   // own VTEP address
	Remotes []string `json:"remotes,omitempty"` // remote VTEP addresses
	Port    int      `json:"port,omitempty"`    // UDP port, 4789 by default
}

// VXLANInfo is a VXLAN of a device as its page shows it, the remote VTEPs
// named by the devices that have their addresses.
type VXLANInfo struct {
	VXLAN
	Source string
	Peers  []VTEPPeer
}

// VTEPPeer is a remote VTEP, with the device it belongs to (if known).
type VTEPPeer struct {
	Address  string
	DeviceID int64
	Device   string
}

// LiveTunnel is a VXLAN tunnel between two devices on a map.
type LiveTunnel struct {
	A    int64  `json:"a"` // device IDs, A < B
	B    int64  `json:"b"`
	VNI  int    `json:"vni"`
	Name string `json:"name,omitempty"`
}

// PortMode is the VLAN mode of a port: "access" (one untagged VLAN),
// "trunk" (tagged VLANs; the untagged one is the native VLAN) or "hybrid"
// (several untagged VLANs).
type PortMode struct {
	Port   string
	Mode   string
	Native int // the access VLAN, or the native VLAN of a trunk; 0: none
	Tagged []int
}

// ModeOf tells the mode of a port carrying these VLANs.
func ModeOf(vs []PortVLAN) (mode string, native int, tagged []int) {
	var untagged []int
	for _, v := range vs {
		if v.Tagged {
			tagged = append(tagged, v.ID)
		} else {
			untagged = append(untagged, v.ID)
		}
	}
	switch {
	case len(untagged) > 1:
		return "hybrid", 0, tagged
	case len(tagged) > 0:
		if len(untagged) == 1 {
			native = untagged[0]
		}
		return "trunk", native, tagged
	case len(untagged) == 1:
		return "access", untagged[0], nil
	}
	return "", 0, nil
}

// DevicePortModes lists the VLAN mode of each port of a device that carries
// VLANs, in interface order.
func (s *Store) DevicePortModes(ctx context.Context, deviceID int64) ([]PortMode, error) {
	rows, err := s.db.Query(ctx, `SELECT v.if_index, COALESCE(NULLIF(i.name,''), i.descr, ''), v.vlan_id, bool_or(v.tagged)
		FROM device_vlans v LEFT JOIN device_interfaces i ON i.device_id=v.device_id AND i.if_index=v.if_index
		WHERE v.device_id=$1 AND v.if_index > 0 GROUP BY 1, 2, 3 ORDER BY 1, 3`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var order []int
	names := map[int]string{}
	vlans := map[int][]PortVLAN{}
	for rows.Next() {
		var idx, id int
		var name string
		var tagged bool
		if err := rows.Scan(&idx, &name, &id, &tagged); err != nil {
			return nil, err
		}
		if _, ok := vlans[idx]; !ok {
			order = append(order, idx)
		}
		if name == "" {
			name = "#" + strconv.Itoa(idx)
		}
		names[idx] = name
		vlans[idx] = append(vlans[idx], PortVLAN{ID: id, Tagged: tagged})
	}
	out := make([]PortMode, 0, len(order))
	for _, idx := range order {
		m := PortMode{Port: names[idx]}
		m.Mode, m.Native, m.Tagged = ModeOf(vlans[idx])
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetDeviceVXLANs replaces what one source knows about the VXLANs of a
// device.
func (s *Store) SetDeviceVXLANs(ctx context.Context, deviceID int64, source string, vs []VXLAN) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM device_vxlans WHERE device_id=$1 AND source=$2`, deviceID, source); err != nil {
		return err
	}
	now, seen := s.now(), map[string]bool{}
	for i, v := range vs {
		key := strconv.Itoa(v.VNI) + "/" + v.Name
		if i >= 256 || v.VNI < 1 || v.VNI > MaxVNI || seen[key] {
			continue
		}
		seen[key] = true
		remotes := []string{}
		for _, r := range v.Remotes {
			if a, err := netip.ParseAddr(r); err == nil && len(remotes) < 64 {
				remotes = append(remotes, a.String())
			}
		}
		local := ""
		if a, err := netip.ParseAddr(v.Local); err == nil {
			local = a.String()
		}
		port := v.Port
		if port < 0 || port > 65535 {
			port = 0
		}
		if _, err := tx.Exec(ctx, `INSERT INTO device_vxlans(device_id, source, vni, name, local_ip, remotes, port, updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
			deviceID, source, v.VNI, truncate(v.Name, 100), local, remotes, port, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeviceVXLANs lists the VXLANs of a device by VNI, the remote VTEPs named
// by the devices that have their addresses.
func (s *Store) DeviceVXLANs(ctx context.Context, deviceID int64) ([]VXLANInfo, error) {
	rows, err := s.db.Query(ctx, `SELECT vni, name, local_ip, remotes, port, source FROM device_vxlans WHERE device_id=$1 ORDER BY vni, name, source`, deviceID)
	if err != nil {
		return nil, err
	}
	var out []VXLANInfo
	var addrs []string
	for rows.Next() {
		var v VXLANInfo
		if err := rows.Scan(&v.VNI, &v.Name, &v.Local, &v.Remotes, &v.Port, &v.Source); err != nil {
			rows.Close()
			return nil, err
		}
		addrs = append(addrs, v.Remotes...)
		out = append(out, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	owners, err := s.vtepOwners(ctx, addrs)
	if err != nil {
		return nil, err
	}
	for i := range out {
		for _, r := range out[i].Remotes {
			p := VTEPPeer{Address: r}
			if o, ok := owners[r]; ok && o.id != deviceID {
				p.DeviceID, p.Device = o.id, o.name
			}
			out[i].Peers = append(out[i].Peers, p)
		}
	}
	return out, nil
}

type vtepOwner struct {
	id   int64
	name string
}

// vtepOwners finds the devices that have these addresses: as their address
// or as the own address of one of their VXLANs.
func (s *Store) vtepOwners(ctx context.Context, addrs []string) (map[string]vtepOwner, error) {
	out := map[string]vtepOwner{}
	if len(addrs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT d.address, d.id, d.name FROM devices d WHERE d.address = ANY($1)
		UNION SELECT x.local_ip, d.id, d.name FROM device_vxlans x JOIN devices d ON d.id=x.device_id WHERE x.local_ip = ANY($1)`, addrs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		var o vtepOwner
		if err := rows.Scan(&a, &o.id, &o.name); err != nil {
			return nil, err
		}
		if _, ok := out[a]; !ok {
			out[a] = o
		}
	}
	return out, rows.Err()
}

// mapTunnels finds the VXLAN tunnels between the devices of a map: a VXLAN
// of one device whose remote VTEP is another device on the map (by its
// address or the own address of its VXLANs).
func (s *Store) mapTunnels(ctx context.Context, devs map[int64]*LiveDevice) ([]LiveTunnel, error) {
	ids := make([]int64, 0, len(devs))
	owner := map[string]int64{}
	for id, d := range devs {
		ids = append(ids, id)
		if a, err := netip.ParseAddr(d.Address); err == nil {
			owner[a.String()] = id
		}
	}
	rows, err := s.db.Query(ctx, `SELECT device_id, vni, name, local_ip, remotes FROM device_vxlans WHERE device_id = ANY($1) ORDER BY device_id, vni`, ids)
	if err != nil {
		return nil, err
	}
	type row struct {
		dev     int64
		vni     int
		name    string
		remotes []string
	}
	var all []row
	for rows.Next() {
		var r row
		var local string
		if err := rows.Scan(&r.dev, &r.vni, &r.name, &local, &r.remotes); err != nil {
			rows.Close()
			return nil, err
		}
		if local != "" {
			owner[local] = r.dev
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	seen := map[[3]int64]bool{}
	var out []LiveTunnel
	for _, r := range all {
		for _, rem := range r.remotes {
			peer, ok := owner[rem]
			if !ok || peer == r.dev {
				continue
			}
			t := LiveTunnel{A: min(r.dev, peer), B: max(r.dev, peer), VNI: r.vni, Name: r.name}
			key := [3]int64{t.A, t.B, int64(t.VNI)}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		if out[i].B != out[j].B {
			return out[i].B < out[j].B
		}
		return out[i].VNI < out[j].VNI
	})
	return out, nil
}
