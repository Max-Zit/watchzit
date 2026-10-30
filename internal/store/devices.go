package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/max-zit/watchzit/internal/snmp"
)

// Device and check states, from best to worst (Worst picks the last).
const (
	StatusUp      = "up"
	StatusUnknown = "unknown"
	StatusWarning = "warning"
	StatusDown    = "down"
	StatusPaused  = "paused"
)

// Device kinds (icons and later templates).
var DeviceKinds = []string{"router", "switch", "firewall", "server", "pc", "printer", "ups", "accesspoint", "camera", "other"}

// ---- groups

// Group is a folder of devices.
type Group struct {
	ID      int64
	Name    string
	Notes   string
	Devices int
	SLA     float64 // availability target in percent (0 = none)
}

// Groups lists the groups with their device counts.
func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.Query(ctx, `SELECT g.id, g.name, g.notes, (SELECT count(*) FROM devices d WHERE d.group_id=g.id), g.sla_target::float8 FROM device_groups g ORDER BY lower(g.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Notes, &g.Devices, &g.SLA); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func cleanName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || len(n) > 100 || strings.ContainsAny(n, "\x00\r\n\t") {
		return "", errors.New("the name must be 1–100 characters on one line")
	}
	return n, nil
}

// CreateGroup adds a group.
func (s *Store) CreateGroup(ctx context.Context, name, notes string) (int64, error) {
	name, err := cleanName(name)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(ctx, `INSERT INTO device_groups(name, notes) VALUES($1,$2) RETURNING id`, name, strings.TrimSpace(notes)).Scan(&id)
	if isUnique(err) {
		return 0, ErrConflict
	}
	return id, err
}

// UpdateGroup renames a group.
func (s *Store) UpdateGroup(ctx context.Context, id int64, name, notes string) error {
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE device_groups SET name=$2, notes=$3 WHERE id=$1`, id, name, strings.TrimSpace(notes))
	if isUnique(err) {
		return ErrConflict
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteGroup removes a group; its devices stay, without a group.
func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM device_groups WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---- devices

// Device is a monitored network device or computer.
type Device struct {
	ID          int64
	GroupID     *int64
	Group       string
	Name        string
	Address     string
	Kind        string
	WebURL      string
	WebName     string    // label of the web interface button ("" = the default)
	WebLinks    []WebLink // more links
	Notes       string
	Paused      bool
	SNMPProfile int64 // 0 = none
	Parent      int64 // device this one depends on (0 = none)
	ParentName  string
	Status      string
	StatusSince time.Time
	CreatedAt   time.Time
	Checks      int
	ChecksDown  int
}

const deviceCols = `d.id, d.group_id, COALESCE(g.name,''), d.name, d.address, d.kind, d.web_url, d.web_name, d.web_links, d.notes, d.paused, COALESCE(d.snmp_profile_id,0), COALESCE(d.parent_id,0), COALESCE((SELECT p.name FROM devices p WHERE p.id=d.parent_id),''), d.status, d.status_since, d.created_at,
	(SELECT count(*) FROM checks c WHERE c.device_id=d.id), (SELECT count(*) FROM checks c WHERE c.device_id=d.id AND c.status='down' AND NOT c.paused)`

func scanDevice(row interface{ Scan(...any) error }) (*Device, error) {
	d := &Device{}
	var links []byte
	err := row.Scan(&d.ID, &d.GroupID, &d.Group, &d.Name, &d.Address, &d.Kind, &d.WebURL, &d.WebName, &links, &d.Notes, &d.Paused, &d.SNMPProfile, &d.Parent, &d.ParentName, &d.Status, &d.StatusSince, &d.CreatedAt, &d.Checks, &d.ChecksDown)
	_ = json.Unmarshal(links, &d.WebLinks)
	return d, err
}

// DeviceFilter narrows the device list.
type DeviceFilter struct {
	Query    string
	GroupID  int64 // 0 = all, -1 = without a group
	Status   string
	SNMPOnly bool
}

// Devices lists devices, worst status first.
func (s *Store) Devices(ctx context.Context, f DeviceFilter) ([]*Device, error) {
	q := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(f.Query)) + "%"
	rows, err := s.db.Query(ctx, `SELECT `+deviceCols+` FROM devices d LEFT JOIN device_groups g ON g.id=d.group_id
		WHERE ($1 = '%%' OR d.name ILIKE $1 OR d.address ILIKE $1 OR d.notes ILIKE $1)
		AND ($2 = 0 OR ($2 = -1 AND d.group_id IS NULL) OR d.group_id = $2)
		AND ($3 = '' OR d.status = $3) AND (NOT $4 OR d.snmp_profile_id IS NOT NULL)
		ORDER BY array_position(ARRAY['down','warning','unknown','up','paused'], d.status), lower(d.name)`, q, f.GroupID, f.Status, f.SNMPOnly)
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

// DeviceByID loads a device.
func (s *Store) DeviceByID(ctx context.Context, id int64) (*Device, error) {
	d, err := scanDevice(s.db.QueryRow(ctx, `SELECT `+deviceCols+` FROM devices d LEFT JOIN device_groups g ON g.id=d.group_id WHERE d.id=$1`, id))
	return d, notFound(err)
}

// StatusCounts counts devices per status.
func (s *Store) StatusCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.Query(ctx, `SELECT status, count(*) FROM devices GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

var hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62})(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}))*\.?$`)

// ValidAddress accepts an IP address or a DNS name (nothing that could be
// read as an option or a URL by a tool that gets it later).
func ValidAddress(a string) error {
	if _, err := netip.ParseAddr(a); err == nil {
		return nil
	}
	if len(a) <= 253 && hostRE.MatchString(a) && !strings.HasPrefix(a, "-") {
		return nil
	}
	return errors.New("the address must be an IP address or a host name")
}

// DeviceInput is what the device form sets.
type DeviceInput struct {
	GroupID  int64 // 0 = none
	Name     string
	Address  string
	Kind     string
	WebURL   string
	WebName  string
	WebLinks []WebLink
	Notes    string
	Paused   bool
	SNMP     int64 // profile, 0 = none
	Parent   int64 // device this one depends on, 0 = none
}

func (in *DeviceInput) clean() error {
	var err error
	if in.Name, err = cleanName(in.Name); err != nil {
		return err
	}
	in.Address = strings.TrimSpace(in.Address)
	if err := ValidAddress(in.Address); err != nil {
		return err
	}
	ok := false
	for _, k := range DeviceKinds {
		ok = ok || k == in.Kind
	}
	if !ok {
		in.Kind = "other"
	}
	in.WebURL = strings.TrimSpace(in.WebURL)
	if in.WebURL != "" {
		u, err := url.Parse(in.WebURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(in.WebURL) > 500 {
			return errors.New("the web address must start with http:// or https://")
		}
	}
	if in.WebName, err = cleanLabel(in.WebName, 60); err != nil {
		return errors.New("the name of a link can have at most 60 characters on one line")
	}
	if in.WebLinks, err = cleanWebLinks(in.WebLinks); err != nil {
		return err
	}
	in.Notes = strings.TrimSpace(in.Notes)
	if len(in.Notes) > 4000 {
		return errors.New("the notes are too long")
	}
	return nil
}

func nullID(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}

// CreateDevice adds a device.
func (s *Store) CreateDevice(ctx context.Context, in DeviceInput) (int64, error) {
	if err := in.clean(); err != nil {
		return 0, err
	}
	status := StatusUnknown
	if in.Paused {
		status = StatusPaused
	}
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO devices(group_id, name, address, kind, web_url, notes, paused, status, status_since, snmp_profile_id, parent_id, created_at, web_name, web_links) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$9,$12,$13) RETURNING id`,
		nullID(in.GroupID), in.Name, in.Address, in.Kind, in.WebURL, in.Notes, in.Paused, status, s.now(), nullID(in.SNMP), nullID(in.Parent), in.WebName, webLinksJSON(in.WebLinks)).Scan(&id)
	if isUnique(err) {
		return 0, ErrConflict
	}
	if isFK(err) {
		return 0, errors.New("the group, SNMP profile or parent device does not exist")
	}
	return id, err
}

// UpdateDevice changes a device.
func (s *Store) UpdateDevice(ctx context.Context, id int64, in DeviceInput) error {
	if err := in.clean(); err != nil {
		return err
	}
	if err := s.checkParent(ctx, id, in.Parent); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE devices SET group_id=$2, name=$3, address=$4, kind=$5, web_url=$6, notes=$7, paused=$8, snmp_profile_id=$9, parent_id=$10, web_name=$11, web_links=$12 WHERE id=$1`,
		id, nullID(in.GroupID), in.Name, in.Address, in.Kind, in.WebURL, in.Notes, in.Paused, nullID(in.SNMP), nullID(in.Parent), in.WebName, webLinksJSON(in.WebLinks))
	if isUnique(err) {
		return ErrConflict
	}
	if isFK(err) {
		return errors.New("the group, SNMP profile or parent device does not exist")
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err == nil {
		err = s.RefreshDeviceStatus(ctx, id)
	}
	return err
}

// DeleteDevice removes a device and its checks; it returns the series
// whose samples must be deleted too.
func (s *Store) DeleteDevice(ctx context.Context, id int64) ([]int32, error) {
	ids, err := s.seriesOf(ctx, `SELECT s.id FROM series s JOIN checks c ON c.id=s.check_id WHERE c.device_id=$1`, id)
	if err != nil {
		return nil, err
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM devices WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return ids, err
}

func (s *Store) seriesOf(ctx context.Context, q string, id int64) ([]int32, error) {
	rows, err := s.db.Query(ctx, q, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int32])
}

// RefreshDeviceStatus derives the device status from its checks: down when
// every check is down, warning when some are, unknown before the first
// result, paused when the device is paused.
func (s *Store) RefreshDeviceStatus(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `UPDATE devices d SET status=x.st, status_since=CASE WHEN d.status=x.st THEN d.status_since ELSE $2 END
		FROM (SELECT CASE
			WHEN dd.paused THEN 'paused'
			WHEN count(c.id) FILTER (WHERE NOT c.paused) = 0 THEN 'unknown'
			WHEN count(c.id) FILTER (WHERE NOT c.paused AND c.status='down') = count(c.id) FILTER (WHERE NOT c.paused) THEN 'down'
			WHEN count(c.id) FILTER (WHERE NOT c.paused AND c.status='down') > 0 THEN 'warning'
			WHEN count(c.id) FILTER (WHERE NOT c.paused AND c.status='unknown') > 0 THEN 'unknown'
			ELSE 'up' END AS st
			FROM devices dd LEFT JOIN checks c ON c.device_id=dd.id WHERE dd.id=$1 GROUP BY dd.id, dd.paused) x
		WHERE d.id=$1`, id, s.now())
	return err
}

// ---- checks

// Check types.
const (
	CheckICMP         = "icmp"
	CheckTCP          = "tcp"
	CheckSNMP         = "snmp"          // the agent answers; uptime
	CheckSNMPIf       = "snmp_if"       // one interface: traffic, errors, link state
	CheckSNMPCPU      = "snmp_cpu"      // processor load (HOST-RESOURCES)
	CheckSNMPStor     = "snmp_storage"  // memory or a disk (HOST-RESOURCES)
	CheckSNMPOID      = "snmp_oid"      // any numeric OID
	CheckAgent        = "agent"         // the agent is connected; uptime
	CheckAgentCPU     = "agent_cpu"     // processor load (and load average)
	CheckAgentMem     = "agent_mem"     // memory and swap
	CheckAgentDisk    = "agent_disk"    // one file system / drive
	CheckAgentNet     = "agent_net"     // one network interface
	CheckAgentService = "agent_service" // one watched service
	CheckHTTP         = "http"          // a web page: status, time, text, certificate
	CheckTLS          = "tls"           // a TLS certificate: days until it expires
	CheckDNS          = "dns"           // a DNS server answers a name
	CheckPostgres     = "pgsql"         // PostgreSQL: sign-in, response, connections
	CheckMySQL        = "mysql"         // MySQL or MariaDB
	CheckRouterOS     = "routeros"      // MikroTik RouterOS REST API: CPU, memory, uptime
	CheckJSON         = "jsonapi"       // a number from any JSON API
)

// CheckTypes are the types a user can add by hand.
var CheckTypes = []string{CheckICMP, CheckTCP, CheckHTTP, CheckTLS, CheckDNS, CheckPostgres, CheckMySQL, CheckRouterOS, CheckJSON, CheckSNMP, CheckSNMPOID}

// IsSNMP tells whether a check type polls SNMP.
func IsSNMP(typ string) bool { return strings.HasPrefix(typ, "snmp") }

// IsAgent tells whether the agent reports a check (the poller skips it).
func IsAgent(typ string) bool { return strings.HasPrefix(typ, "agent") }

// CheckParams are the settings of a check (by type).
type CheckParams struct {
	// ICMP
	Count     int `json:"count,omitempty"`      // echo requests per run
	TimeoutMS int `json:"timeout_ms,omitempty"` // per request / connection
	// TCP
	Port int `json:"port,omitempty"`
	// SNMP
	IfIndex int     `json:"if_index,omitempty"` // interface
	IfName  string  `json:"if_name,omitempty"`
	Index   int     `json:"index,omitempty"` // hrStorage row
	Descr   string  `json:"descr,omitempty"` // its description (rows move after reboots)
	OID     string  `json:"oid,omitempty"`
	Rate    bool    `json:"rate,omitempty"` // the OID is a counter: store the change per second
	Unit    string  `json:"unit,omitempty"`
	Scale   float64 `json:"scale,omitempty"` // value × scale (0 = 1)
	// HTTP, TLS, DNS, databases, APIs
	URL      string `json:"url,omitempty"`      // HTTP, JSON API (empty: https://<address>/)
	Expect   int    `json:"expect,omitempty"`   // HTTP status code (0: any below 400)
	Contains string `json:"contains,omitempty"` // HTTP body / DNS answer must contain it
	Insecure bool   `json:"insecure,omitempty"` // accept certificates that are not trusted
	Query    string `json:"query,omitempty"`    // DNS name; TLS server name (SNI)
	RType    string `json:"rtype,omitempty"`    // DNS record type
	User     string `json:"user,omitempty"`     // databases, RouterOS, JSON API (basic)
	Secret   string `json:"secret,omitempty"`   // password or token, sealed (never shown)
	Database string `json:"database,omitempty"`
	Path     string `json:"path,omitempty"` // JSON API: where the number is (a.b.0.c)
	Auth     string `json:"auth,omitempty"` // JSON API: "", "basic" or "bearer"
	TLS      bool   `json:"tls,omitempty"`  // RouterOS: HTTPS (else HTTP)
}

// Check is one thing polled on a device.
type Check struct {
	ID          int64
	DeviceID    int64
	Device      string // name
	Address     string // of the device
	Type        string
	Name        string
	Params      CheckParams
	IntervalS   int
	Paused      bool
	DevPaused   bool
	Status      string
	StatusSince time.Time
	LastRun     *time.Time
	LastText    string
	CreatedAt   time.Time
	SNMPProfile int64 // of the device
	Series      []Series
}

// Series is a metric of a check.
type Series struct {
	ID     int32
	Metric string
	Unit   string
}

const checkCols = `c.id, c.device_id, d.name, d.address, c.type, c.name, c.params, c.interval_s, c.paused, d.paused, c.status, c.status_since, c.last_run, c.last_text, c.created_at, COALESCE(d.snmp_profile_id,0)`

func scanCheck(row interface{ Scan(...any) error }) (*Check, error) {
	c := &Check{}
	var raw []byte
	err := row.Scan(&c.ID, &c.DeviceID, &c.Device, &c.Address, &c.Type, &c.Name, &raw, &c.IntervalS, &c.Paused, &c.DevPaused, &c.Status, &c.StatusSince, &c.LastRun, &c.LastText, &c.CreatedAt, &c.SNMPProfile)
	if err == nil {
		err = json.Unmarshal(raw, &c.Params)
	}
	return c, err
}

func (s *Store) checks(ctx context.Context, where string, args ...any) ([]*Check, error) {
	rows, err := s.db.Query(ctx, `SELECT `+checkCols+` FROM checks c JOIN devices d ON d.id=c.device_id `+where+` ORDER BY c.device_id, c.type, c.name`, args...)
	if err != nil {
		return nil, err
	}
	var out []*Check
	byID := map[int64]*Check{}
	for rows.Next() {
		c, err := scanCheck(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
		byID[c.ID] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	ids := make([]int64, 0, len(out))
	for _, c := range out {
		ids = append(ids, c.ID)
	}
	srows, err := s.db.Query(ctx, `SELECT check_id, id, metric, unit FROM series WHERE check_id = ANY($1) ORDER BY id`, ids)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var cid int64
		var x Series
		if err := srows.Scan(&cid, &x.ID, &x.Metric, &x.Unit); err != nil {
			return nil, err
		}
		byID[cid].Series = append(byID[cid].Series, x)
	}
	return out, srows.Err()
}

// DeviceChecks lists the checks of a device.
func (s *Store) DeviceChecks(ctx context.Context, deviceID int64) ([]*Check, error) {
	return s.checks(ctx, `WHERE c.device_id=$1`, deviceID)
}

// AllChecks lists every check (the poller).
func (s *Store) AllChecks(ctx context.Context) ([]*Check, error) { return s.checks(ctx, ``) }

// CheckByID loads a check.
func (s *Store) CheckByID(ctx context.Context, id int64) (*Check, error) {
	cs, err := s.checks(ctx, `WHERE c.id=$1`, id)
	if err != nil {
		return nil, err
	}
	if len(cs) == 0 {
		return nil, ErrNotFound
	}
	return cs[0], nil
}

// CheckInput is what the check form sets.
type CheckInput struct {
	Type      string
	Name      string
	Params    CheckParams
	IntervalS int
	Paused    bool
}

// metricsOf are the series a check type writes.
func metricsOf(typ string, p CheckParams) []Series {
	switch typ {
	case CheckICMP:
		return []Series{{Metric: "rtt_ms", Unit: "ms"}, {Metric: "loss_pct", Unit: "%"}}
	case CheckTCP:
		return []Series{{Metric: "connect_ms", Unit: "ms"}}
	case CheckSNMP:
		return []Series{{Metric: "response_ms", Unit: "ms"}, {Metric: "uptime_s", Unit: "s"}}
	case CheckSNMPIf:
		return []Series{{Metric: "in_bps", Unit: "bit/s"}, {Metric: "out_bps", Unit: "bit/s"}, {Metric: "in_err", Unit: "/s"}, {Metric: "out_err", Unit: "/s"}}
	case CheckSNMPCPU:
		return []Series{{Metric: "cpu_pct", Unit: "%"}}
	case CheckSNMPStor:
		return []Series{{Metric: "used_pct", Unit: "%"}, {Metric: "used_b", Unit: "B"}}
	case CheckSNMPOID:
		return []Series{{Metric: "value", Unit: p.Unit}}
	case CheckAgent:
		return []Series{{Metric: "uptime_s", Unit: "s"}}
	case CheckAgentCPU:
		return []Series{{Metric: "cpu_pct", Unit: "%"}, {Metric: "load1", Unit: ""}}
	case CheckAgentMem:
		return []Series{{Metric: "mem_pct", Unit: "%"}, {Metric: "swap_pct", Unit: "%"}, {Metric: "mem_used_b", Unit: "B"}}
	case CheckAgentDisk:
		return []Series{{Metric: "used_pct", Unit: "%"}, {Metric: "used_b", Unit: "B"}}
	case CheckAgentNet:
		return []Series{{Metric: "in_bps", Unit: "bit/s"}, {Metric: "out_bps", Unit: "bit/s"}, {Metric: "in_err", Unit: "/s"}, {Metric: "out_err", Unit: "/s"}}
	case CheckAgentService:
		return []Series{{Metric: "running", Unit: ""}}
	case CheckHTTP:
		return []Series{{Metric: "response_ms", Unit: "ms"}, {Metric: "status_code", Unit: ""}, {Metric: "cert_days", Unit: "d"}}
	case CheckTLS:
		return []Series{{Metric: "cert_days", Unit: "d"}, {Metric: "handshake_ms", Unit: "ms"}}
	case CheckDNS:
		return []Series{{Metric: "response_ms", Unit: "ms"}}
	case CheckPostgres, CheckMySQL:
		return []Series{{Metric: "response_ms", Unit: "ms"}, {Metric: "connections", Unit: ""}}
	case CheckRouterOS:
		return []Series{{Metric: "cpu_pct", Unit: "%"}, {Metric: "mem_pct", Unit: "%"}, {Metric: "uptime_s", Unit: "s"}, {Metric: "temperature", Unit: "°C"}}
	case CheckJSON:
		return []Series{{Metric: "value", Unit: p.Unit}, {Metric: "response_ms", Unit: "ms"}}
	}
	return nil
}

var unitRE = regexp.MustCompile(`^[\p{L}\p{N}%/°. ]{0,16}$`)

func (in *CheckInput) clean() error {
	if metricsOf(in.Type, in.Params) == nil {
		return errors.New("unknown check type")
	}
	if in.IntervalS == 0 {
		in.IntervalS = 60
	}
	if in.IntervalS < 10 || in.IntervalS > 3600 {
		return errors.New("the interval must be 10–3600 seconds")
	}
	p := &in.Params
	if p.TimeoutMS == 0 {
		p.TimeoutMS = 2000
	}
	if p.TimeoutMS < 100 || p.TimeoutMS > 30000 {
		return errors.New("the timeout must be 100–30000 ms")
	}
	switch in.Type {
	case CheckICMP:
		if p.Count == 0 {
			p.Count = 3
		}
		if p.Count < 1 || p.Count > 20 {
			return errors.New("the number of pings must be 1–20")
		}
		p.Port = 0
		if strings.TrimSpace(in.Name) == "" {
			in.Name = "Ping"
		}
	case CheckTCP:
		if p.Port < 1 || p.Port > 65535 {
			return errors.New("the port must be 1–65535")
		}
		p.Count = 0
		if strings.TrimSpace(in.Name) == "" {
			in.Name = "TCP " + strconv.Itoa(p.Port)
		}
	case CheckHTTP, CheckTLS, CheckDNS, CheckPostgres, CheckMySQL, CheckRouterOS, CheckJSON:
		if err := p.cleanService(in.Type); err != nil {
			return err
		}
		if strings.TrimSpace(in.Name) == "" {
			in.Name = p.defaultName(in.Type)
		}
		in.Name = truncate(strings.Join(strings.Fields(in.Name), " "), 95)
	case CheckAgent, CheckAgentCPU, CheckAgentMem, CheckAgentDisk, CheckAgentNet, CheckAgentService:
		keep := CheckParams{TimeoutMS: p.TimeoutMS, IfIndex: p.IfIndex, IfName: truncate(p.IfName, 95), Descr: truncate(strings.TrimSpace(p.Descr), 95)}
		switch {
		case in.Type == CheckAgentNet && p.IfIndex < 1, (in.Type == CheckAgentDisk || in.Type == CheckAgentService) && keep.Descr == "":
			return errors.New("the agent check misses its interface, drive or service")
		}
		*p = keep
		if strings.TrimSpace(in.Name) == "" {
			in.Name = map[string]string{CheckAgent: "Agent", CheckAgentCPU: "CPU", CheckAgentMem: "Memory"}[in.Type]
			if in.Name == "" {
				in.Name = p.IfName + p.Descr
			}
		}
		in.Name = truncate(strings.Join(strings.Fields(in.Name), " "), 95)
	default: // SNMP
		p.Count, p.Port = 0, 0
		if err := p.cleanSNMP(in.Type); err != nil {
			return err
		}
		if strings.TrimSpace(in.Name) == "" {
			switch in.Type {
			case CheckSNMP:
				in.Name = "SNMP"
			case CheckSNMPIf:
				in.Name = p.IfName
			case CheckSNMPCPU:
				in.Name = "CPU"
			case CheckSNMPStor:
				in.Name = p.Descr
			case CheckSNMPOID:
				in.Name = "OID " + p.OID
			}
		}
		in.Name = truncate(strings.Join(strings.Fields(in.Name), " "), 95)
	}
	var err error
	in.Name, err = cleanName(in.Name)
	return err
}

// CreateCheck adds a check with its series.
func (s *Store) CreateCheck(ctx context.Context, deviceID int64, in CheckInput) (int64, error) {
	if err := in.clean(); err != nil {
		return 0, err
	}
	if HasSecret(in.Type) {
		s.sealCheckSecret(&in.Params, "")
	}
	raw, _ := json.Marshal(in.Params) // #nosec G117 -- Secret is sealed above
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO checks(device_id, type, name, params, interval_s, paused, status_since) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		deviceID, in.Type, in.Name, raw, in.IntervalS, in.Paused, s.now()).Scan(&id)
	if isFK(err) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	for _, m := range metricsOf(in.Type, in.Params) {
		if _, err := tx.Exec(ctx, `INSERT INTO series(check_id, metric, unit) VALUES($1,$2,$3)`, id, m.Metric, m.Unit); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, s.RefreshDeviceStatus(ctx, deviceID)
}

// UpdateCheck changes the settings of a check (not its type).
func (s *Store) UpdateCheck(ctx context.Context, id int64, in CheckInput) error {
	c, err := s.CheckByID(ctx, id)
	if err != nil {
		return err
	}
	in.Type = c.Type
	if (IsSNMP(c.Type) && c.Type != CheckSNMPOID) || IsAgent(c.Type) {
		// what discovery set (interface, storage) is not in the settings form
		p := c.Params
		p.TimeoutMS = in.Params.TimeoutMS
		in.Params = p
	}
	if err := in.clean(); err != nil {
		return err
	}
	if HasSecret(in.Type) {
		s.sealCheckSecret(&in.Params, c.Params.Secret)
	}
	raw, _ := json.Marshal(in.Params) // #nosec G117 -- Secret is sealed above
	if _, err := s.db.Exec(ctx, `UPDATE checks SET name=$2, params=$3, interval_s=$4, paused=$5 WHERE id=$1`, id, in.Name, raw, in.IntervalS, in.Paused); err != nil {
		return err
	}
	return s.RefreshDeviceStatus(ctx, c.DeviceID)
}

// DeleteCheck removes a check; it returns its series for the sample store.
func (s *Store) DeleteCheck(ctx context.Context, id int64) ([]int32, error) {
	c, err := s.CheckByID(ctx, id)
	if err != nil {
		return nil, err
	}
	ids, err := s.seriesOf(ctx, `SELECT id FROM series WHERE check_id=$1`, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM checks WHERE id=$1`, id); err != nil {
		return nil, err
	}
	return ids, s.RefreshDeviceStatus(ctx, c.DeviceID)
}

// CheckResult is the outcome of one run.
type CheckResult struct {
	CheckID int64
	At      time.Time
	Status  string             // up or down
	Text    string             // short human summary ("1.2 ms, 0% loss", "connection refused")
	Values  map[string]float64 // latest value per metric
}

// SaveResults stores the outcome of runs and refreshes the affected
// devices; it returns the checks whose status changed.
func (s *Store) SaveResults(ctx context.Context, rs []CheckResult) ([]StatusChange, error) {
	if len(rs) == 0 {
		return nil, nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	devs := map[int64]bool{}
	var changes []StatusChange
	for _, r := range rs {
		var dev int64
		var old string
		// nil Values keep the stored ones (a first counter poll has no rate yet)
		var vals []byte
		if r.Values != nil {
			vals, _ = json.Marshal(r.Values)
		}
		err := tx.QueryRow(ctx, `UPDATE checks c SET status=$2, last_run=$3, last_text=$4, last_values=COALESCE($5, c.last_values),
			status_since=CASE WHEN c.status=$2 THEN c.status_since ELSE $3 END
			FROM checks o WHERE c.id=$1 AND o.id=c.id RETURNING c.device_id, o.status`, r.CheckID, r.Status, r.At, truncate(r.Text, 300), vals).Scan(&dev, &old)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // deleted meanwhile
		}
		if err != nil {
			return nil, err
		}
		devs[dev] = true
		if old != r.Status {
			changes = append(changes, StatusChange{CheckID: r.CheckID, DeviceID: dev, From: old, To: r.Status, At: r.At, Text: r.Text})
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	for id := range devs {
		if err := s.RefreshDeviceStatus(ctx, id); err != nil {
			return nil, err
		}
	}
	return changes, nil
}

// StatusChange is a check that went up or down.
type StatusChange struct {
	CheckID, DeviceID int64
	From, To          string
	At                time.Time
	Text              string
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}

// SeriesUnit returns the unit of a series (and whether it exists).
func (s *Store) SeriesUnit(ctx context.Context, id int32) (string, error) {
	var u string
	err := s.db.QueryRow(ctx, `SELECT unit FROM series WHERE id=$1`, id).Scan(&u)
	return u, notFound(err)
}

func (p *CheckParams) cleanSNMP(typ string) error {
	keep := CheckParams{TimeoutMS: p.TimeoutMS}
	switch typ {
	case CheckSNMP, CheckSNMPCPU:
	case CheckSNMPIf:
		if p.IfIndex < 1 {
			return errors.New("choose an interface")
		}
		keep.IfIndex, keep.IfName = p.IfIndex, truncate(strings.TrimSpace(p.IfName), 95)
		if keep.IfName == "" {
			keep.IfName = "if" + strconv.Itoa(p.IfIndex)
		}
	case CheckSNMPStor:
		if p.Index < 1 {
			return errors.New("choose a storage")
		}
		keep.Index, keep.Descr = p.Index, truncate(strings.TrimSpace(p.Descr), 95)
	case CheckSNMPOID:
		o, err := snmp.ParseOID(p.OID)
		if err != nil {
			return errors.New("enter a numeric OID such as 1.3.6.1.2.1.1.3.0")
		}
		keep.OID, keep.Rate = o.String(), p.Rate
		keep.Unit = strings.TrimSpace(p.Unit)
		if !unitRE.MatchString(keep.Unit) {
			return errors.New("the unit may have up to 16 letters, digits, % / ° or spaces")
		}
		keep.Scale = p.Scale
		if keep.Scale == 1 {
			keep.Scale = 0
		}
		if keep.Scale < 0 || keep.Scale > 1e12 || (keep.Scale != 0 && keep.Scale < 1e-12) {
			return errors.New("the multiplier must be a positive number")
		}
	default:
		return errors.New("unknown check type")
	}
	*p = keep
	return nil
}

// UpdateCheckParams lets the poller fix the row of a storage check after
// the device renumbered it (the description stays the key).
func (s *Store) UpdateCheckParams(ctx context.Context, id int64, p CheckParams) error {
	raw, _ := json.Marshal(p) // #nosec G117 -- params as loaded: Secret stays sealed
	_, err := s.db.Exec(ctx, `UPDATE checks SET params=$2 WHERE id=$1`, id, raw)
	return err
}

// DevicesWithSNMP lists the devices that have an SNMP profile (the poller
// refreshes their facts and interfaces).
func (s *Store) DevicesWithSNMP(ctx context.Context) ([]*Device, error) {
	return s.Devices(ctx, DeviceFilter{SNMPOnly: true})
}

// Clean validates a check and fills in defaults (the name), as saving would.
func (in *CheckInput) Clean() error { return in.clean() }

// WebLink is a further web page of a device (a second address, iDRAC …).
type WebLink struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// cleanWebLinks drops empty rows, checks the rest (http/https only, at
// most 8) and names a link without a name after its host.
func cleanWebLinks(in []WebLink) ([]WebLink, error) {
	out := []WebLink{}
	for _, l := range in {
		l.Name, l.URL = strings.TrimSpace(l.Name), strings.TrimSpace(l.URL)
		if l.URL == "" {
			if l.Name != "" {
				return nil, errors.New("a web link needs an address")
			}
			continue
		}
		u, err := url.Parse(l.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(l.URL) > 500 {
			return nil, errors.New("the web address must start with http:// or https://")
		}
		if l.Name == "" {
			l.Name = u.Host
		}
		if l.Name, err = cleanLabel(l.Name, 60); err != nil {
			return nil, errors.New("the name of a link can have at most 60 characters on one line")
		}
		out = append(out, l)
	}
	if len(out) > 8 {
		return nil, errors.New("a device can have at most 8 more web links")
	}
	return out, nil
}

func webLinksJSON(l []WebLink) []byte {
	if len(l) == 0 {
		return []byte("[]")
	}
	b, _ := json.Marshal(l)
	return b
}

// SetGroupSLA sets the availability target of a group (0 = none).
func (s *Store) SetGroupSLA(ctx context.Context, id int64, target float64) error {
	if target != 0 && (target < 50 || target > 100) {
		return errors.New("the SLA target is a percentage between 50 and 100")
	}
	tag, err := s.db.Exec(ctx, `UPDATE device_groups SET sla_target=$2 WHERE id=$1`, id, target)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
