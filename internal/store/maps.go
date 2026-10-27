package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

// Map is a network diagram.
type Map struct {
	ID        int64
	Name      string
	Notes     string
	Devices   int
	Status    string // worst status of its devices (submaps included)
	UpdatedAt time.Time
}

// Map node kinds.
const (
	NodeDevice = "device"
	NodeSubmap = "submap"
	NodeLabel  = "label"
)

// Link kinds.
var LinkKinds = []string{"wired", "wireless", "vpn"}

// MapNode is a device, a submap or a text on a map.
type MapNode struct {
	ID       int64   `json:"id"`
	Kind     string  `json:"kind"`
	DeviceID int64   `json:"device,omitempty"`
	SubmapID int64   `json:"submap,omitempty"`
	Label    string  `json:"label,omitempty"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
}

// MapLink connects two nodes, optionally port to port (ifIndex).
type MapLink struct {
	ID    int64  `json:"id"`
	A     int64  `json:"a"`
	B     int64  `json:"b"`
	AIf   int    `json:"a_if,omitempty"`
	BIf   int    `json:"b_if,omitempty"`
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
}

// MapLayout is what the editor saves: nodes with client ids (new nodes
// have ids ≤ 0) and links that refer to them.
type MapLayout struct {
	Nodes []MapNode `json:"nodes"`
	Links []MapLink `json:"links"`
}

// statusRank orders statuses for "worst of" (higher is worse).
var statusRank = map[string]int{StatusPaused: 0, StatusUp: 1, StatusUnknown: 2, StatusWarning: 3, StatusDown: 4}

// Worse returns the worse of two statuses.
func Worse(a, b string) string {
	if statusRank[b] > statusRank[a] {
		return b
	}
	return a
}

// Maps lists the maps with their worst status.
func (s *Store) Maps(ctx context.Context) ([]*Map, error) {
	rows, err := s.db.Query(ctx, `SELECT m.id, m.name, m.notes, m.updated_at,
		(SELECT count(*) FROM map_nodes n WHERE n.map_id=m.id AND n.kind='device') FROM maps m ORDER BY lower(m.name)`)
	if err != nil {
		return nil, err
	}
	var out []*Map
	for rows.Next() {
		m := &Map{}
		if err := rows.Scan(&m.ID, &m.Name, &m.Notes, &m.UpdatedAt, &m.Devices); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	st, err := s.mapStatuses(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range out {
		m.Status = st[m.ID]
	}
	return out, nil
}

// mapStatuses computes the worst device status of every map, following
// submaps (cycles are allowed for navigation and are cut here).
func (s *Store) mapStatuses(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.Query(ctx, `SELECT n.map_id, n.kind, COALESCE(d.status,''), COALESCE(n.submap_id,0)
		FROM map_nodes n LEFT JOIN devices d ON d.id=n.device_id WHERE n.kind IN ('device','submap')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	direct := map[int64]string{}
	subs := map[int64][]int64{}
	for rows.Next() {
		var mapID, sub int64
		var kind, st string
		if err := rows.Scan(&mapID, &kind, &st, &sub); err != nil {
			return nil, err
		}
		if kind == NodeDevice {
			direct[mapID] = Worse(direct[mapID], st)
		} else {
			subs[mapID] = append(subs[mapID], sub)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[int64]string{}
	var walk func(id int64, seen map[int64]bool) string
	walk = func(id int64, seen map[int64]bool) string {
		if seen[id] {
			return ""
		}
		seen[id] = true
		w := direct[id]
		for _, sub := range subs[id] {
			w = Worse(w, walk(sub, seen))
		}
		return w
	}
	for id := range direct {
		out[id] = walk(id, map[int64]bool{})
	}
	for id := range subs {
		out[id] = walk(id, map[int64]bool{})
	}
	return out, nil
}

// MapByID loads a map.
func (s *Store) MapByID(ctx context.Context, id int64) (*Map, error) {
	ms, err := s.Maps(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range ms {
		if m.ID == id {
			return m, nil
		}
	}
	return nil, ErrNotFound
}

// CreateMap adds an empty map.
func (s *Store) CreateMap(ctx context.Context, name, notes string) (int64, error) {
	name, err := cleanName(name)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(ctx, `INSERT INTO maps(name, notes, created_at, updated_at) VALUES($1,$2,$3,$3) RETURNING id`, name, strings.TrimSpace(notes), s.now()).Scan(&id)
	if isUnique(err) {
		return 0, ErrConflict
	}
	return id, err
}

// UpdateMap renames a map.
func (s *Store) UpdateMap(ctx context.Context, id int64, name, notes string) error {
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE maps SET name=$2, notes=$3, updated_at=$4 WHERE id=$1`, id, name, strings.TrimSpace(notes), s.now())
	if isUnique(err) {
		return ErrConflict
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteMap removes a map (devices stay; submap nodes pointing to it go).
func (s *Store) DeleteMap(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM maps WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// MapLayout loads the nodes and links of a map.
func (s *Store) MapLayout(ctx context.Context, id int64) (*MapLayout, error) {
	l := &MapLayout{Nodes: []MapNode{}, Links: []MapLink{}}
	rows, err := s.db.Query(ctx, `SELECT id, kind, COALESCE(device_id,0), COALESCE(submap_id,0), label, x, y FROM map_nodes WHERE map_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n MapNode
		if err := rows.Scan(&n.ID, &n.Kind, &n.DeviceID, &n.SubmapID, &n.Label, &n.X, &n.Y); err != nil {
			rows.Close()
			return nil, err
		}
		l.Nodes = append(l.Nodes, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT id, a_node, b_node, COALESCE(a_if,0), COALESCE(b_if,0), kind, label FROM map_links WHERE map_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k MapLink
		if err := rows.Scan(&k.ID, &k.A, &k.B, &k.AIf, &k.BIf, &k.Kind, &k.Label); err != nil {
			return nil, err
		}
		l.Links = append(l.Links, k)
	}
	return l, rows.Err()
}

func cleanLabel(s string, n int) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) > n || strings.ContainsAny(s, "\x00\r\n\t") {
		return "", errors.New("labels must be short and on one line")
	}
	return s, nil
}

// coordinates far outside any sensible drawing are refused
const mapLimit = 1e6

func intPtr(i int) *int {
	if i <= 0 {
		return nil
	}
	return &i
}

// SaveMapLayout replaces the nodes and links of a map with the editor's
// state. Existing node ids are kept (so links and history stay stable);
// new nodes get ids. It returns the saved layout with the real ids.
func (s *Store) SaveMapLayout(ctx context.Context, mapID int64, in *MapLayout) (*MapLayout, error) {
	if len(in.Nodes) > 2000 || len(in.Links) > 5000 {
		return nil, errors.New("the map is too large (at most 2000 nodes and 5000 links)")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM maps WHERE id=$1 FOR UPDATE`, mapID).Scan(&exists); err != nil {
		return nil, notFound(err)
	}
	// nodes that belong to this map now
	old := map[int64]bool{}
	rows, err := tx.Query(ctx, `SELECT id FROM map_nodes WHERE map_id=$1`, mapID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		old[id] = true
	}
	rows.Close()
	if _, err := tx.Exec(ctx, `DELETE FROM map_links WHERE map_id=$1`, mapID); err != nil {
		return nil, err
	}
	ids := map[int64]int64{} // client id → real id
	keep := map[int64]bool{}
	out := &MapLayout{Nodes: []MapNode{}, Links: []MapLink{}}
	devices := map[int64]bool{}
	for _, n := range in.Nodes {
		if math.IsNaN(n.X) || math.IsNaN(n.Y) || math.Abs(n.X) > mapLimit || math.Abs(n.Y) > mapLimit {
			return nil, errors.New("a node lies outside the map")
		}
		if n.Label, err = cleanLabel(n.Label, 200); err != nil {
			return nil, err
		}
		var dev, sub *int64
		switch n.Kind {
		case NodeDevice:
			if devices[n.DeviceID] {
				return nil, errors.New("a device can be on a map only once")
			}
			devices[n.DeviceID] = true
			dev, n.SubmapID = &n.DeviceID, 0
		case NodeSubmap:
			sub, n.DeviceID = &n.SubmapID, 0
		case NodeLabel:
			n.DeviceID, n.SubmapID = 0, 0
			if n.Label == "" {
				continue
			}
		default:
			return nil, errors.New("unknown node kind")
		}
		var real int64
		if n.ID > 0 && old[n.ID] {
			_, err = tx.Exec(ctx, `UPDATE map_nodes SET kind=$2, device_id=$3, submap_id=$4, label=$5, x=$6, y=$7 WHERE id=$1`, n.ID, n.Kind, dev, sub, n.Label, n.X, n.Y)
			real = n.ID
		} else {
			err = tx.QueryRow(ctx, `INSERT INTO map_nodes(map_id, kind, device_id, submap_id, label, x, y) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
				mapID, n.Kind, dev, sub, n.Label, n.X, n.Y).Scan(&real)
		}
		if isFK(err) {
			return nil, errors.New("a device or map on the map no longer exists")
		}
		if err != nil {
			return nil, err
		}
		ids[n.ID] = real
		keep[real] = true
		n.ID = real
		out.Nodes = append(out.Nodes, n)
	}
	for id := range old {
		if !keep[id] {
			if _, err := tx.Exec(ctx, `DELETE FROM map_nodes WHERE id=$1`, id); err != nil {
				return nil, err
			}
		}
	}
	for _, k := range in.Links {
		a, okA := ids[k.A]
		b, okB := ids[k.B]
		if !okA || !okB || a == b {
			return nil, errors.New("a link must connect two nodes of the map")
		}
		ok := false
		for _, x := range LinkKinds {
			ok = ok || x == k.Kind
		}
		if !ok {
			k.Kind = "wired"
		}
		if k.Label, err = cleanLabel(k.Label, 100); err != nil {
			return nil, err
		}
		if k.AIf < 0 || k.BIf < 0 {
			return nil, errors.New("invalid interface")
		}
		k.A, k.B = a, b
		if err := tx.QueryRow(ctx, `INSERT INTO map_links(map_id, a_node, b_node, a_if, b_if, kind, label) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			mapID, a, b, intPtr(k.AIf), intPtr(k.BIf), k.Kind, k.Label).Scan(&k.ID); err != nil {
			return nil, err
		}
		out.Links = append(out.Links, k)
	}
	if _, err := tx.Exec(ctx, `UPDATE maps SET updated_at=$2 WHERE id=$1`, mapID, s.now()); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// ---- live data for the map

// LiveDevice is what the map shows about a device.
type LiveDevice struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Address    string `json:"address"`
	Kind       string `json:"kind"`
	Status     string `json:"status"`
	WebURL     string `json:"web,omitempty"`
	ChecksDown int    `json:"down"`
	Checks     int    `json:"checks"`
	Vendor     string `json:"vendor,omitempty"`
}

// LivePort is an interface with its latest traffic.
type LivePort struct {
	Name   string   `json:"name"`
	Alias  string   `json:"alias,omitempty"`
	Speed  int64    `json:"speed,omitempty"`
	Up     bool     `json:"up"`
	Admin  bool     `json:"admin"`
	In     *float64 `json:"in,omitempty"`  // bit/s
	Out    *float64 `json:"out,omitempty"` // bit/s
	Check  int64    `json:"check,omitempty"`
	Status string   `json:"status,omitempty"` // of the interface check
}

// MapLive is the state of everything on a map, refreshed by the browser.
type MapLive struct {
	Devices map[int64]*LiveDevice       `json:"devices"`
	Ports   map[int64]map[int]*LivePort `json:"ports"` // device → ifIndex → port
	Maps    map[int64]string            `json:"maps"`  // submap → worst status
}

// DevicePorts lists the interfaces of devices with their latest traffic.
func (s *Store) DevicePorts(ctx context.Context, ids []int64) (map[int64]map[int]*LivePort, error) {
	out := map[int64]map[int]*LivePort{}
	rows, err := s.db.Query(ctx, `SELECT device_id, if_index, name, alias, descr, speed_bps, oper_up, admin_up FROM device_interfaces WHERE device_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var dev int64
		var idx int
		var name, descr string
		p := &LivePort{}
		if err := rows.Scan(&dev, &idx, &name, &p.Alias, &descr, &p.Speed, &p.Up, &p.Admin); err != nil {
			rows.Close()
			return nil, err
		}
		p.Name = name
		if p.Name == "" {
			p.Name = descr
		}
		if out[dev] == nil {
			out[dev] = map[int]*LivePort{}
		}
		out[dev][idx] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// traffic from the interface checks
	rows, err = s.db.Query(ctx, `SELECT device_id, id, params, last_values, status, paused FROM checks WHERE type=$1 AND device_id = ANY($2)`, CheckSNMPIf, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var dev, id int64
		var rawP, rawV []byte
		var status string
		var paused bool
		if err := rows.Scan(&dev, &id, &rawP, &rawV, &status, &paused); err != nil {
			return nil, err
		}
		var p CheckParams
		var v map[string]float64
		_ = json.Unmarshal(rawP, &p)
		_ = json.Unmarshal(rawV, &v)
		if out[dev] == nil {
			out[dev] = map[int]*LivePort{}
		}
		lp := out[dev][p.IfIndex]
		if lp == nil {
			lp = &LivePort{Name: p.IfName, Up: status == StatusUp, Admin: true}
			out[dev][p.IfIndex] = lp
		}
		lp.Check = id
		lp.Status = status
		if paused {
			lp.Status = StatusPaused
		}
		if x, ok := v["in_bps"]; ok {
			lp.In = &x
		}
		if x, ok := v["out_bps"]; ok {
			lp.Out = &x
		}
	}
	return out, rows.Err()
}

// MapLiveData returns the live state of the devices and submaps on a map.
func (s *Store) MapLiveData(ctx context.Context, l *MapLayout) (*MapLive, error) {
	live := &MapLive{Devices: map[int64]*LiveDevice{}, Ports: map[int64]map[int]*LivePort{}, Maps: map[int64]string{}}
	var ids []int64
	for _, n := range l.Nodes {
		if n.Kind == NodeDevice {
			ids = append(ids, n.DeviceID)
		}
	}
	if len(ids) > 0 {
		rows, err := s.db.Query(ctx, `SELECT d.id, d.name, d.address, d.kind, d.status, d.web_url, COALESCE(d.facts->>'vendor',''),
			(SELECT count(*) FROM checks c WHERE c.device_id=d.id), (SELECT count(*) FROM checks c WHERE c.device_id=d.id AND c.status='down' AND NOT c.paused)
			FROM devices d WHERE d.id = ANY($1)`, ids)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			d := &LiveDevice{}
			if err := rows.Scan(&d.ID, &d.Name, &d.Address, &d.Kind, &d.Status, &d.WebURL, &d.Vendor, &d.Checks, &d.ChecksDown); err != nil {
				rows.Close()
				return nil, err
			}
			live.Devices[d.ID] = d
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		var err2 error
		if live.Ports, err2 = s.DevicePorts(ctx, ids); err2 != nil {
			return nil, err2
		}
	}
	st, err := s.mapStatuses(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range l.Nodes {
		if n.Kind == NodeSubmap {
			live.Maps[n.SubmapID] = st[n.SubmapID]
		}
	}
	return live, nil
}
