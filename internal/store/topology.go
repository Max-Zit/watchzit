package store

import (
	"context"
	"encoding/json"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Topology: what the devices report about their neighbours (LLDP, CDP,
// MikroTik), the MAC addresses their switch ports learnt and the ARP
// tables of routers. Each read replaces what the device reported before.

// TopoNeighbour is a device a port sees over a neighbour protocol.
type TopoNeighbour struct {
	DeviceID   int64
	IfIndex    int
	LocalPort  string
	Name       string
	RemotePort string
	RemoteDesc string
	Address    netip.Addr
	MAC        string
	Via        string
}

// TopoFDB is a MAC address a switch learnt on a port.
type TopoFDB struct {
	DeviceID int64
	IfIndex  int
	MAC      string
	VLAN     int
}

// TopoARP is an address with its MAC as a router knows it.
type TopoARP struct {
	DeviceID int64
	Address  netip.Addr
	MAC      string
	IfIndex  int
}

// NormalizeMAC writes a MAC as aa:bb:cc:dd:ee:ff ("" when it is none).
func NormalizeMAC(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	h := strings.NewReplacer(":", "", "-", "", ".", "").Replace(s)
	if len(h) != 12 || strings.Trim(h, "0123456789abcdef") != "" || h == "000000000000" {
		return ""
	}
	return h[0:2] + ":" + h[2:4] + ":" + h[4:6] + ":" + h[6:8] + ":" + h[8:10] + ":" + h[10:12]
}

// maxTopoRows bounds what one device may store per table.
const maxTopoRows = 50000

// SetTopology replaces the tables a device reported. A nil slice keeps
// that table as it was (it was not read).
func (s *Store) SetTopology(ctx context.Context, deviceID int64, at time.Time, nb []TopoNeighbour, fdb []TopoFDB, arp []TopoARP) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if nb != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM topo_neighbours WHERE device_id=$1`, deviceID); err != nil {
			return err
		}
		rows := make([][]any, 0, len(nb))
		for i, n := range nb {
			if i >= maxTopoRows {
				break
			}
			var a any
			if n.Address.IsValid() {
				a = n.Address
			}
			rows = append(rows, []any{deviceID, n.IfIndex, truncate(n.LocalPort, 200), truncate(n.Name, 200), truncate(n.RemotePort, 200), truncate(n.RemoteDesc, 200), a, NormalizeMAC(n.MAC), n.Via})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"topo_neighbours"}, []string{"device_id", "if_index", "local_port", "name", "remote_port", "remote_desc", "address", "mac", "via"}, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
	}
	if fdb != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM topo_fdb WHERE device_id=$1`, deviceID); err != nil {
			return err
		}
		seen := map[TopoFDB]bool{}
		rows := make([][]any, 0, len(fdb))
		for _, f := range fdb {
			f.DeviceID, f.MAC = 0, NormalizeMAC(f.MAC)
			if f.MAC == "" || f.IfIndex <= 0 || seen[f] || len(rows) >= maxTopoRows {
				continue
			}
			seen[f] = true
			rows = append(rows, []any{deviceID, f.IfIndex, f.MAC, f.VLAN})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"topo_fdb"}, []string{"device_id", "if_index", "mac", "vlan"}, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
	}
	if arp != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM topo_arp WHERE device_id=$1`, deviceID); err != nil {
			return err
		}
		seen := map[netip.Addr]bool{}
		rows := make([][]any, 0, len(arp))
		for _, a := range arp {
			m := NormalizeMAC(a.MAC)
			if m == "" || !a.Address.IsValid() || seen[a.Address] || len(rows) >= maxTopoRows {
				continue
			}
			seen[a.Address] = true
			rows = append(rows, []any{deviceID, a.Address, m, a.IfIndex})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"topo_arp"}, []string{"device_id", "address", "mac", "if_index"}, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO topo_read(device_id, at, neighbours, fdb, arp) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (device_id) DO UPDATE SET at=EXCLUDED.at,
		neighbours=CASE WHEN $6 THEN EXCLUDED.neighbours ELSE topo_read.neighbours END,
		fdb=CASE WHEN $7 THEN EXCLUDED.fdb ELSE topo_read.fdb END,
		arp=CASE WHEN $8 THEN EXCLUDED.arp ELSE topo_read.arp END`,
		deviceID, at, len(nb), len(fdb), len(arp), nb != nil, fdb != nil, arp != nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Topology is everything the resolver needs.
type Topology struct {
	Neighbours []TopoNeighbour
	FDB        []TopoFDB
	ARP        []TopoARP
}

// LoadTopology reads the tables (of all devices).
func (s *Store) LoadTopology(ctx context.Context) (*Topology, error) {
	t := &Topology{}
	rows, err := s.db.Query(ctx, `SELECT device_id, if_index, local_port, name, remote_port, remote_desc, address, mac, via FROM topo_neighbours`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n TopoNeighbour
		var a *netip.Addr
		if err := rows.Scan(&n.DeviceID, &n.IfIndex, &n.LocalPort, &n.Name, &n.RemotePort, &n.RemoteDesc, &a, &n.MAC, &n.Via); err != nil {
			rows.Close()
			return nil, err
		}
		if a != nil {
			n.Address = *a
		}
		t.Neighbours = append(t.Neighbours, n)
	}
	rows.Close()
	rows, err = s.db.Query(ctx, `SELECT device_id, if_index, mac, vlan FROM topo_fdb`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var f TopoFDB
		if err := rows.Scan(&f.DeviceID, &f.IfIndex, &f.MAC, &f.VLAN); err != nil {
			rows.Close()
			return nil, err
		}
		t.FDB = append(t.FDB, f)
	}
	rows.Close()
	rows, err = s.db.Query(ctx, `SELECT device_id, address, mac, if_index FROM topo_arp`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var a TopoARP
		if err := rows.Scan(&a.DeviceID, &a.Address, &a.MAC, &a.IfIndex); err != nil {
			rows.Close()
			return nil, err
		}
		t.ARP = append(t.ARP, a)
	}
	rows.Close()
	return t, rows.Err()
}

// TopoReadAt is when the topology of each device was read last.
func (s *Store) TopoReadAt(ctx context.Context) (map[int64]time.Time, error) {
	rows, err := s.db.Query(ctx, `SELECT device_id, at FROM topo_read`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id int64
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}

// AllInterfaces lists the interfaces of every device (for MAC matching).
func (s *Store) AllInterfaces(ctx context.Context) (map[int64][]Interface, error) {
	rows, err := s.db.Query(ctx, `SELECT device_id, if_index, name, alias, descr, mac FROM device_interfaces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Interface{}
	for rows.Next() {
		var id int64
		var i Interface
		if err := rows.Scan(&id, &i.Index, &i.Name, &i.Alias, &i.Descr, &i.MAC); err != nil {
			return nil, err
		}
		out[id] = append(out[id], i)
	}
	return out, rows.Err()
}

// TopoLinkKey names a link between two device ports the same way from both
// ends (for the list of links a map must not add again).
func TopoLinkKey(a int64, aIf int, b int64, bIf int) string {
	x, y := strconv.FormatInt(a, 10)+":"+strconv.Itoa(aIf), strconv.FormatInt(b, 10)+":"+strconv.Itoa(bIf)
	if x > y {
		x, y = y, x
	}
	return "L" + x + "-" + y
}

// TopoDeviceKey names a device a map must not add again.
func TopoDeviceKey(id int64) string { return "D" + strconv.FormatInt(id, 10) }

// MapAuto is what a map adds by itself.
type MapAuto struct {
	Links   bool     `json:"auto_links"`
	Devices bool     `json:"auto_devices"`
	Ignore  []string `json:"-"` // links and devices removed by hand
}

// MapAutoOf reads the settings of a map.
func (s *Store) MapAutoOf(ctx context.Context, mapID int64) (*MapAuto, error) {
	a := &MapAuto{}
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT auto_links, auto_devices, auto_ignore FROM maps WHERE id=$1`, mapID).Scan(&a.Links, &a.Devices, &raw); err != nil {
		return nil, notFound(err)
	}
	_ = json.Unmarshal(raw, &a.Ignore)
	return a, nil
}

// SetMapAuto switches the automatic links and devices of a map; switching
// links on again forgets what was removed by hand.
func (s *Store) SetMapAuto(ctx context.Context, mapID int64, links, devices bool) error {
	devices = devices && links
	tag, err := s.db.Exec(ctx, `UPDATE maps SET auto_ignore = CASE WHEN $2 AND NOT auto_links THEN '[]'::jsonb ELSE auto_ignore END,
		auto_links=$2, auto_devices=$3 WHERE id=$1`, mapID, links, devices)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// AutoMaps lists the maps that add links by themselves.
func (s *Store) AutoMaps(ctx context.Context) ([]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM maps WHERE auto_links ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// autoBefore reads the automatic links and devices of a map before it is
// saved, keyed as in the ignore list: only those the editor had loaded
// (ids up to seenLink and seenNode) — what the map added while the editor
// was open is not something the user removed.
func autoBefore(ctx context.Context, tx pgx.Tx, mapID, seenLink, seenNode int64) (links map[string]bool, nodes map[int64]int64, err error) {
	if seenLink == 0 && seenNode == 0 {
		return nil, nil, nil // an editor that does not say what it loaded removes nothing for good
	}
	links, nodes = map[string]bool{}, map[int64]int64{}
	rows, err := tx.Query(ctx, `SELECT na.device_id, COALESCE(l.a_if,0), nb.device_id, COALESCE(l.b_if,0) FROM map_links l
		JOIN map_nodes na ON na.id=l.a_node JOIN map_nodes nb ON nb.id=l.b_node
		WHERE l.map_id=$1 AND l.auto AND l.id <= $2 AND na.device_id IS NOT NULL AND nb.device_id IS NOT NULL`, mapID, seenLink)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var a, b int64
		var ai, bi int
		if err := rows.Scan(&a, &ai, &b, &bi); err != nil {
			rows.Close()
			return nil, nil, err
		}
		links[TopoLinkKey(a, ai, b, bi)] = true
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT id, device_id FROM map_nodes WHERE map_id=$1 AND auto AND id <= $2 AND device_id IS NOT NULL`, mapID, seenNode)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, dev int64
		if err := rows.Scan(&id, &dev); err != nil {
			rows.Close()
			return nil, nil, err
		}
		nodes[id] = dev
	}
	rows.Close()
	return links, nodes, rows.Err()
}

// rememberRemoved adds the automatic links and devices that are gone from
// the saved layout to the map's ignore list.
func rememberRemoved(ctx context.Context, tx pgx.Tx, mapID int64, oldLinks map[string]bool, oldNodes map[int64]int64, out *MapLayout) error {
	if len(oldLinks) == 0 && len(oldNodes) == 0 {
		return nil
	}
	dev := map[int64]int64{}
	for _, n := range out.Nodes {
		if n.Kind == NodeDevice {
			dev[n.ID] = n.DeviceID
		}
	}
	now := map[string]bool{}
	for _, k := range out.Links {
		if dev[k.A] != 0 && dev[k.B] != 0 {
			now[TopoLinkKey(dev[k.A], k.AIf, dev[k.B], k.BIf)] = true
		}
	}
	var gone []string
	for k := range oldLinks {
		if !now[k] {
			gone = append(gone, k)
		}
	}
	for id, d := range oldNodes {
		if _, ok := dev[id]; !ok {
			gone = append(gone, TopoDeviceKey(d))
		}
	}
	if len(gone) == 0 {
		return nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT auto_ignore FROM maps WHERE id=$1`, mapID).Scan(&raw); err != nil {
		return err
	}
	var list []string
	_ = json.Unmarshal(raw, &list)
	for _, g := range gone {
		if !slices.Contains(list, g) {
			list = append(list, g)
		}
	}
	if len(list) > 5000 {
		list = list[len(list)-5000:]
	}
	raw, _ = json.Marshal(list)
	_, err := tx.Exec(ctx, `UPDATE maps SET auto_ignore=$2 WHERE id=$1`, mapID, raw)
	return err
}
