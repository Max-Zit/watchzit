package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
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
	NodeItem   = "item" // something not monitored: patch panel, internet, building …
	NodeRack   = "rack"
	NodeHost   = "host" // a hypervisor host with its virtual machines inside
)

// LinkKinds are the cable and connection kinds.
var LinkKinds = []string{"ethernet", "fiber", "fiber-sm", "dac", "wireless", "vpn", "vxlan", "wan", "serial", "power"}

// LinkRoutes are the ways a link is drawn through its bend points.
var LinkRoutes = []string{"straight", "ortho", "diagonal", "curve"}

// MapNode is a device, a submap, a passive item, a rack or a text.
type MapNode struct {
	ID       int64   `json:"id"`
	Kind     string  `json:"kind"`
	DeviceID int64   `json:"device,omitempty"`
	SubmapID int64   `json:"submap,omitempty"`
	Label    string  `json:"label,omitempty"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Icon     string  `json:"icon,omitempty"`   // "lib:<name>", "asset:<id>" or "" (kind symbol)
	Size     float64 `json:"size,omitempty"`   // shown width (0 = default)
	Parent   int64   `json:"parent,omitempty"` // rack it is mounted in, or hypervisor host it runs on
	U        int     `json:"u,omitempty"`      // rack: height in units; mounted: lowest unit; on a host: its place
	// HostDevice is the physical server a hypervisor host (kind host) runs on.
	HostDevice int64 `json:"host_device,omitempty"`
}

// MapLink connects two nodes, optionally port to port (ifIndex for the
// traffic, slot for the port on the picture).
type MapLink struct {
	ID     int64        `json:"id"`
	A      int64        `json:"a"`
	B      int64        `json:"b"`
	AIf    int          `json:"a_if,omitempty"`
	BIf    int          `json:"b_if,omitempty"`
	ASlot  int          `json:"a_slot,omitempty"`
	BSlot  int          `json:"b_slot,omitempty"`
	Kind   string       `json:"kind"`
	Label  string       `json:"label,omitempty"`
	Route  string       `json:"route,omitempty"`
	Points [][2]float64 `json:"points,omitempty"`
	VLANs  []PortVLAN   `json:"vlans,omitempty"` // set by hand
	VNI    int          `json:"vni,omitempty"`   // VXLAN VNI set by hand
}

// MapBackground is a picture under the map.
type MapBackground struct {
	Asset   int64   `json:"asset,omitempty"` // 0 = the dotted grid
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	W       float64 `json:"w"`
	Opacity float64 `json:"opacity"`
}

// MapLayout is what the editor saves: nodes with client ids (new nodes
// have ids ≤ 0) and links that refer to them.
type MapLayout struct {
	Nodes      []MapNode      `json:"nodes"`
	Links      []MapLink      `json:"links"`
	Background *MapBackground `json:"background,omitempty"`
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
	l := &MapLayout{Nodes: []MapNode{}, Links: []MapLink{}, Background: &MapBackground{}}
	bg := l.Background
	var asset *int64
	if err := s.db.QueryRow(ctx, `SELECT bg_asset, bg_x, bg_y, bg_w, bg_opacity FROM maps WHERE id=$1`, id).Scan(&asset, &bg.X, &bg.Y, &bg.W, &bg.Opacity); err != nil {
		return nil, notFound(err)
	}
	if asset != nil {
		bg.Asset = *asset
	}
	rows, err := s.db.Query(ctx, `SELECT id, kind, COALESCE(device_id,0), COALESCE(submap_id,0), label, x, y, icon, size, COALESCE(parent,0), u, COALESCE(host_device,0) FROM map_nodes WHERE map_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n MapNode
		if err := rows.Scan(&n.ID, &n.Kind, &n.DeviceID, &n.SubmapID, &n.Label, &n.X, &n.Y, &n.Icon, &n.Size, &n.Parent, &n.U, &n.HostDevice); err != nil {
			rows.Close()
			return nil, err
		}
		l.Nodes = append(l.Nodes, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT id, a_node, b_node, COALESCE(a_if,0), COALESCE(b_if,0), a_slot, b_slot, kind, label, route, points, vlans, vni FROM map_links WHERE map_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k MapLink
		var pts, vls []byte
		if err := rows.Scan(&k.ID, &k.A, &k.B, &k.AIf, &k.BIf, &k.ASlot, &k.BSlot, &k.Kind, &k.Label, &k.Route, &pts, &vls, &k.VNI); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(pts, &k.Points)
		_ = json.Unmarshal(vls, &k.VLANs)
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

func badCoord(vs ...float64) bool {
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > mapLimit {
			return true
		}
	}
	return false
}

var iconRE = regexp.MustCompile(`^(lib:[a-z0-9-]{1,40}|asset:[0-9]{1,18})?$`)

func oneOf(v string, list []string, def string) string {
	for _, x := range list {
		if x == v {
			return v
		}
	}
	return def
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
	// parents are set in a second pass, after every node has its real id
	if _, err := tx.Exec(ctx, `UPDATE map_nodes SET parent=NULL WHERE map_id=$1`, mapID); err != nil {
		return nil, err
	}
	kinds := map[int64]string{}
	for _, n := range in.Nodes {
		kinds[n.ID] = n.Kind
	}
	ids := map[int64]int64{} // client id → real id
	keep := map[int64]bool{}
	out := &MapLayout{Nodes: []MapNode{}, Links: []MapLink{}}
	devices := map[int64]bool{}
	for _, n := range in.Nodes {
		if badCoord(n.X, n.Y, n.Size) || n.Size < 0 || n.Size > 5000 {
			return nil, errors.New("a node lies outside the map")
		}
		if n.Label, err = cleanLabel(n.Label, 200); err != nil {
			return nil, err
		}
		if !iconRE.MatchString(n.Icon) {
			return nil, errors.New("unknown picture")
		}
		var dev, sub, hostDev *int64
		if n.Kind != NodeHost {
			n.HostDevice = 0
		}
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
			n.DeviceID, n.SubmapID, n.Icon = 0, 0, ""
			if n.Label == "" {
				continue
			}
		case NodeItem:
			n.DeviceID, n.SubmapID = 0, 0
		case NodeRack:
			n.DeviceID, n.SubmapID, n.Icon, n.Parent = 0, 0, "", 0
			if n.U < 1 || n.U > 60 {
				return nil, errors.New("a rack has 1–60 units")
			}
		case NodeHost:
			n.DeviceID, n.SubmapID, n.Parent, n.U = 0, 0, 0, 0
			if n.HostDevice <= 0 {
				return nil, errors.New("choose the physical server of each hypervisor")
			}
			hostDev = &n.HostDevice
		default:
			return nil, errors.New("unknown node kind")
		}
		if n.Parent != 0 {
			switch {
			case kinds[n.Parent] == NodeHost:
				if n.Kind != NodeDevice && n.Kind != NodeItem || n.U < 0 || n.U > 1000 {
					return nil, errors.New("only devices and items run on a hypervisor host")
				}
			case kinds[n.Parent] != NodeRack || n.U < 1 || n.U > 60:
				return nil, errors.New("a mounted device needs a rack and a unit")
			}
		} else if n.Kind != NodeRack {
			n.U = 0
		}
		var real int64
		if n.ID > 0 && old[n.ID] {
			_, err = tx.Exec(ctx, `UPDATE map_nodes SET kind=$2, device_id=$3, submap_id=$4, label=$5, x=$6, y=$7, icon=$8, size=$9, u=$10, host_device=$11 WHERE id=$1`,
				n.ID, n.Kind, dev, sub, n.Label, n.X, n.Y, n.Icon, n.Size, n.U, hostDev)
			real = n.ID
		} else {
			err = tx.QueryRow(ctx, `INSERT INTO map_nodes(map_id, kind, device_id, submap_id, label, x, y, icon, size, u, host_device) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
				mapID, n.Kind, dev, sub, n.Label, n.X, n.Y, n.Icon, n.Size, n.U, hostDev).Scan(&real)
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
	for i := range out.Nodes {
		n := &out.Nodes[i]
		if n.Parent == 0 {
			continue
		}
		p, ok := ids[n.Parent]
		if !ok {
			return nil, errors.New("a mounted device needs a rack and a unit")
		}
		n.Parent = p
		if _, err := tx.Exec(ctx, `UPDATE map_nodes SET parent=$2 WHERE id=$1`, n.ID, p); err != nil {
			return nil, err
		}
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
		k.Kind = oneOf(k.Kind, LinkKinds, "ethernet")
		k.Route = oneOf(k.Route, LinkRoutes, "straight")
		if k.Label, err = cleanLabel(k.Label, 100); err != nil {
			return nil, err
		}
		if k.AIf < 0 || k.BIf < 0 || k.ASlot < 0 || k.BSlot < 0 || k.ASlot > 1000 || k.BSlot > 1000 {
			return nil, errors.New("invalid interface")
		}
		if len(k.Points) > 50 {
			return nil, errors.New("a link can have at most 50 bends")
		}
		if k.VLANs, err = cleanLinkVLANs(k.VLANs); err != nil {
			return nil, err
		}
		if k.VNI < 0 || k.VNI > MaxVNI {
			return nil, errors.New("a VXLAN VNI is a number from 1 to 16777215")
		}
		vls, _ := json.Marshal(k.VLANs)
		for _, p := range k.Points {
			if badCoord(p[0], p[1]) {
				return nil, errors.New("a node lies outside the map")
			}
		}
		pts := []byte("[]")
		if len(k.Points) > 0 {
			pts, _ = json.Marshal(k.Points)
		}
		k.A, k.B = a, b
		if err := tx.QueryRow(ctx, `INSERT INTO map_links(map_id, a_node, b_node, a_if, b_if, a_slot, b_slot, kind, label, route, points, vlans, vni) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) RETURNING id`,
			mapID, a, b, intPtr(k.AIf), intPtr(k.BIf), k.ASlot, k.BSlot, k.Kind, k.Label, k.Route, pts, vls, k.VNI).Scan(&k.ID); err != nil {
			return nil, err
		}
		out.Links = append(out.Links, k)
	}
	if bg := in.Background; bg != nil {
		if badCoord(bg.X, bg.Y, bg.W) || bg.W < 10 || bg.Opacity < 0 || bg.Opacity > 1 {
			return nil, errors.New("invalid background")
		}
		var asset *int64
		if bg.Asset > 0 {
			asset = &bg.Asset
		}
		_, err := tx.Exec(ctx, `UPDATE maps SET bg_asset=$2, bg_x=$3, bg_y=$4, bg_w=$5, bg_opacity=$6 WHERE id=$1`, mapID, asset, bg.X, bg.Y, bg.W, bg.Opacity)
		if isFK(err) {
			return nil, errors.New("unknown picture")
		}
		if err != nil {
			return nil, err
		}
		out.Background = bg
	}
	if _, err := tx.Exec(ctx, `UPDATE maps SET updated_at=$2 WHERE id=$1`, mapID, s.now()); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

// ---- live data for the map

// LiveDevice is what the map shows about a device.
type LiveDevice struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Address    string     `json:"address"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	WebURL     string     `json:"web,omitempty"`
	WebName    string     `json:"web_name,omitempty"`
	WebLinks   []WebLink  `json:"links,omitempty"`
	ChecksDown int        `json:"down"`
	Checks     int        `json:"checks"`
	Vendor     string     `json:"vendor,omitempty"`
	Tools      []LiveTool `json:"tools,omitempty"` // filled by the console, not for kiosks
}

// LiveTool is a tool that opens a device from the map.
type LiveTool struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // built-in tool or "custom"
}

// LivePort is an interface with its latest traffic.
type LivePort struct {
	Name   string     `json:"name"`
	Alias  string     `json:"alias,omitempty"`
	Speed  int64      `json:"speed,omitempty"`
	Up     bool       `json:"up"`
	Admin  bool       `json:"admin"`
	In     *float64   `json:"in,omitempty"`  // bit/s
	Out    *float64   `json:"out,omitempty"` // bit/s
	Check  int64      `json:"check,omitempty"`
	Status string     `json:"status,omitempty"` // of the interface check
	VLANs  []PortVLAN `json:"vlans,omitempty"`
}

// MapLive is the state of everything on a map, refreshed by the browser.
type MapLive struct {
	Devices map[int64]*LiveDevice       `json:"devices"`
	Ports   map[int64]map[int]*LivePort `json:"ports"`             // device → ifIndex → port
	Maps    map[int64]string            `json:"maps"`              // submap → worst status
	VLANs   map[int]string              `json:"vlans,omitempty"`   // VLAN → name (may be empty)
	Tunnels []LiveTunnel                `json:"tunnels,omitempty"` // VXLAN tunnels between devices of the map
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
	rows, err = s.db.Query(ctx, `SELECT device_id, id, params, last_values, status, paused FROM checks WHERE type IN ($1, $3) AND device_id = ANY($2)`, CheckSNMPIf, ids, CheckAgentNet)
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
		} else if n.Kind == NodeHost && n.HostDevice > 0 {
			ids = append(ids, n.HostDevice)
		}
	}
	if len(ids) > 0 {
		rows, err := s.db.Query(ctx, `SELECT d.id, d.name, d.address, d.kind, d.status, d.web_url, d.web_name, d.web_links, COALESCE(d.facts->>'vendor',''),
			(SELECT count(*) FROM checks c WHERE c.device_id=d.id), (SELECT count(*) FROM checks c WHERE c.device_id=d.id AND c.status='down' AND NOT c.paused)
			FROM devices d WHERE d.id = ANY($1)`, ids)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			d := &LiveDevice{}
			var links []byte
			if err := rows.Scan(&d.ID, &d.Name, &d.Address, &d.Kind, &d.Status, &d.WebURL, &d.WebName, &links, &d.Vendor, &d.Checks, &d.ChecksDown); err != nil {
				rows.Close()
				return nil, err
			}
			_ = json.Unmarshal(links, &d.WebLinks)
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
		if live.VLANs, err2 = s.portVLANs(ctx, ids, live.Ports); err2 != nil {
			return nil, err2
		}
		if live.Tunnels, err2 = s.mapTunnels(ctx, live.Devices); err2 != nil {
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
