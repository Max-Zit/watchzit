package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Severities, least severe first.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Severities lists the severities, least severe first.
var Severities = []string{SeverityInfo, SeverityWarning, SeverityCritical}

// SeverityRank orders severities (higher is worse; -1 unknown).
func SeverityRank(s string) int { return slices.Index(Severities, s) }

// Trigger kinds.
const (
	TriggerDeviceDown = "device_down"
	TriggerCheckDown  = "check_down"
	TriggerMetric     = "metric"
)

// checkParent refuses a parent that is the device itself or depends on it.
func (s *Store) checkParent(ctx context.Context, id, parent int64) error {
	for p, n := parent, 0; p != 0; n++ {
		if p == id {
			return errors.New("a device cannot depend on itself or on a device that depends on it")
		}
		if n > 100 {
			return errors.New("the dependency chain is too long")
		}
		err := s.db.QueryRow(ctx, `SELECT COALESCE(parent_id,0) FROM devices WHERE id=$1`, p).Scan(&p)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("the group, SNMP profile or parent device does not exist")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ---- triggers

// Trigger turns check results into problems.
type Trigger struct {
	ID         int64
	Name       string
	Kind       string
	CheckTypes []string
	Metric     string
	Op         string
	Threshold  float64
	ForS       int
	Severity   string
	GroupID    int64 // 0 = every group
	Group      string
	DeviceID   int64 // 0 = every device
	Device     string
	Enabled    bool
	CreatedAt  time.Time
}

// TriggerInput is what the trigger form sets.
type TriggerInput struct {
	Name       string
	Kind       string
	CheckTypes []string
	Metric     string
	Op         string
	Threshold  float64
	ForS       int
	Severity   string
	GroupID    int64
	DeviceID   int64
	Enabled    bool
}

// TriggerMetrics are the metrics a metric trigger can watch, with the
// check types that have them.
func TriggerMetrics() map[string][]string {
	out := map[string][]string{}
	for _, t := range allCheckTypes {
		for _, m := range metricsOf(t, CheckParams{}) {
			out[m.Metric] = append(out[m.Metric], t)
		}
	}
	return out
}

var allCheckTypes = []string{CheckICMP, CheckTCP, CheckSNMP, CheckSNMPIf, CheckSNMPCPU, CheckSNMPStor, CheckSNMPOID,
	CheckAgent, CheckAgentCPU, CheckAgentMem, CheckAgentDisk, CheckAgentNet, CheckAgentService}

// MetricUnit is the unit of a metric of a check type ("" when unknown).
func MetricUnit(typ, metric string, p CheckParams) string {
	for _, m := range metricsOf(typ, p) {
		if m.Metric == metric {
			return m.Unit
		}
	}
	return ""
}

func (in *TriggerInput) clean() error {
	var err error
	if in.Name, err = cleanName(in.Name); err != nil {
		return err
	}
	if SeverityRank(in.Severity) < 0 {
		return errors.New("unknown severity")
	}
	if in.ForS < 0 || in.ForS > 86400 {
		return errors.New("the delay must be 0–86400 seconds")
	}
	if in.GroupID != 0 && in.DeviceID != 0 {
		return errors.New("choose a group or a device, not both")
	}
	switch in.Kind {
	case TriggerDeviceDown, TriggerCheckDown:
		in.Metric, in.Op, in.Threshold = "", ">", 0
		if in.Kind == TriggerDeviceDown {
			in.CheckTypes = nil
		}
	case TriggerMetric:
		types, ok := TriggerMetrics()[in.Metric]
		if !ok {
			return errors.New("choose the value to watch")
		}
		if in.Op != ">" && in.Op != "<" {
			return errors.New("the condition must be above or below")
		}
		if math.IsNaN(in.Threshold) || math.IsInf(in.Threshold, 0) {
			return errors.New("the threshold must be a number")
		}
		for _, t := range in.CheckTypes {
			if !slices.Contains(types, t) {
				return fmt.Errorf("checks of type %s have no such value", t)
			}
		}
	default:
		return errors.New("unknown trigger kind")
	}
	for _, t := range in.CheckTypes {
		if !slices.Contains(allCheckTypes, t) {
			return errors.New("unknown check type")
		}
	}
	slices.Sort(in.CheckTypes)
	in.CheckTypes = slices.Compact(in.CheckTypes)
	if in.CheckTypes == nil {
		in.CheckTypes = []string{}
	}
	return nil
}

const triggerCols = `t.id, t.name, t.kind, t.check_types, t.metric, t.op, t.threshold, t.for_s, t.severity, COALESCE(t.group_id,0), COALESCE(g.name,''),
	COALESCE(t.device_id,0), COALESCE(d.name,''), t.enabled, t.created_at`

func scanTrigger(row pgx.Row) (*Trigger, error) {
	t := &Trigger{}
	err := row.Scan(&t.ID, &t.Name, &t.Kind, &t.CheckTypes, &t.Metric, &t.Op, &t.Threshold, &t.ForS, &t.Severity, &t.GroupID, &t.Group, &t.DeviceID, &t.Device, &t.Enabled, &t.CreatedAt)
	return t, err
}

// Triggers lists the triggers.
func (s *Store) Triggers(ctx context.Context) ([]*Trigger, error) {
	rows, err := s.db.Query(ctx, `SELECT `+triggerCols+` FROM triggers t LEFT JOIN device_groups g ON g.id=t.group_id LEFT JOIN devices d ON d.id=t.device_id
		ORDER BY array_position(ARRAY['device_down','check_down','metric'], t.kind), lower(t.name), t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Trigger
	for rows.Next() {
		t, err := scanTrigger(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TriggerByID loads a trigger.
func (s *Store) TriggerByID(ctx context.Context, id int64) (*Trigger, error) {
	t, err := scanTrigger(s.db.QueryRow(ctx, `SELECT `+triggerCols+` FROM triggers t LEFT JOIN device_groups g ON g.id=t.group_id LEFT JOIN devices d ON d.id=t.device_id WHERE t.id=$1`, id))
	return t, notFound(err)
}

// CreateTrigger adds a trigger.
func (s *Store) CreateTrigger(ctx context.Context, in TriggerInput) (int64, error) {
	if err := in.clean(); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO triggers(name, kind, check_types, metric, op, threshold, for_s, severity, group_id, device_id, enabled)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
		in.Name, in.Kind, in.CheckTypes, in.Metric, in.Op, in.Threshold, in.ForS, in.Severity, nullID(in.GroupID), nullID(in.DeviceID), in.Enabled).Scan(&id)
	if isFK(err) {
		return 0, errors.New("the group or device does not exist")
	}
	return id, err
}

// UpdateTrigger changes a trigger.
func (s *Store) UpdateTrigger(ctx context.Context, id int64, in TriggerInput) error {
	if err := in.clean(); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE triggers SET name=$2, kind=$3, check_types=$4, metric=$5, op=$6, threshold=$7, for_s=$8, severity=$9, group_id=$10, device_id=$11, enabled=$12 WHERE id=$1`,
		id, in.Name, in.Kind, in.CheckTypes, in.Metric, in.Op, in.Threshold, in.ForS, in.Severity, nullID(in.GroupID), nullID(in.DeviceID), in.Enabled)
	if isFK(err) {
		return errors.New("the group or device does not exist")
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteTrigger removes a trigger; its open problems resolve on the next
// evaluation.
func (s *Store) DeleteTrigger(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM triggers WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---- what the alert engine looks at

// AlertDevice is a device as the alert engine sees it.
type AlertDevice struct {
	ID          int64
	Name        string
	Address     string
	GroupID     int64
	Parent      int64
	Paused      bool
	Status      string
	StatusSince time.Time
}

// AlertCheck is a check as the alert engine sees it.
type AlertCheck struct {
	ID          int64
	DeviceID    int64
	Type        string
	Name        string
	Params      CheckParams
	Paused      bool
	Status      string
	StatusSince time.Time
	LastText    string
	Values      map[string]float64
}

// AlertInputs loads every device and check for one evaluation.
func (s *Store) AlertInputs(ctx context.Context) ([]AlertDevice, []AlertCheck, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, address, COALESCE(group_id,0), COALESCE(parent_id,0), paused, status, status_since FROM devices`)
	if err != nil {
		return nil, nil, err
	}
	var devs []AlertDevice
	for rows.Next() {
		var d AlertDevice
		if err := rows.Scan(&d.ID, &d.Name, &d.Address, &d.GroupID, &d.Parent, &d.Paused, &d.Status, &d.StatusSince); err != nil {
			rows.Close()
			return nil, nil, err
		}
		devs = append(devs, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT id, device_id, type, name, params, paused, status, status_since, last_text, last_values FROM checks`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var cs []AlertCheck
	for rows.Next() {
		var c AlertCheck
		var params, vals []byte
		if err := rows.Scan(&c.ID, &c.DeviceID, &c.Type, &c.Name, &params, &c.Paused, &c.Status, &c.StatusSince, &c.LastText, &vals); err != nil {
			return nil, nil, err
		}
		_ = json.Unmarshal(params, &c.Params)
		_ = json.Unmarshal(vals, &c.Values)
		cs = append(cs, c)
	}
	return devs, cs, rows.Err()
}

// ---- problems

// Problem is a condition a trigger found.
type Problem struct {
	ID         int64
	Key        string
	TriggerID  int64
	DeviceID   int64
	Device     string
	Address    string
	CheckID    int64
	Check      string
	Severity   string
	Title      string
	Detail     string
	StartedAt  time.Time
	ResolvedAt *time.Time
	Suppressed string
	NotifiedAt *time.Time
	AckAt      *time.Time
	AckBy      string
	AckNote    string
}

// Open reports whether the problem is still on.
func (p *Problem) Open() bool { return p.ResolvedAt == nil }

// Duration is how long the problem lasted (until now while open).
func (p *Problem) Duration(now time.Time) time.Duration {
	end := now
	if p.ResolvedAt != nil {
		end = *p.ResolvedAt
	}
	return end.Sub(p.StartedAt)
}

const problemCols = `p.id, p.key, COALESCE(p.trigger_id,0), p.device_id, d.name, d.address, COALESCE(p.check_id,0), COALESCE(c.name,''), p.severity, p.title, p.detail,
	p.started_at, p.resolved_at, p.suppressed, p.notified_at, p.ack_at, p.ack_by, p.ack_note`

func scanProblem(row pgx.Row) (*Problem, error) {
	p := &Problem{}
	err := row.Scan(&p.ID, &p.Key, &p.TriggerID, &p.DeviceID, &p.Device, &p.Address, &p.CheckID, &p.Check, &p.Severity, &p.Title, &p.Detail,
		&p.StartedAt, &p.ResolvedAt, &p.Suppressed, &p.NotifiedAt, &p.AckAt, &p.AckBy, &p.AckNote)
	return p, err
}

// ProblemFilter selects problems.
type ProblemFilter struct {
	Open       bool  // only open ones (else only resolved ones)
	Suppressed bool  // include suppressed open problems
	DeviceID   int64 // 0 = all
	Severity   string
	Since      time.Time // resolved ones started after this
	Limit      int
}

// Problems lists problems, worst and newest first.
func (s *Store) Problems(ctx context.Context, f ProblemFilter) ([]*Problem, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 500
	}
	rows, err := s.db.Query(ctx, `SELECT `+problemCols+` FROM problems p JOIN devices d ON d.id=p.device_id LEFT JOIN checks c ON c.id=p.check_id
		WHERE (p.resolved_at IS NULL) = $1 AND ($2 OR p.resolved_at IS NOT NULL OR p.suppressed='') AND ($3 = 0 OR p.device_id=$3)
		AND ($4 = '' OR p.severity=$4) AND (p.resolved_at IS NULL OR p.started_at >= $5)
		ORDER BY CASE WHEN p.resolved_at IS NULL THEN array_position(ARRAY['critical','warning','info'], p.severity) ELSE 0 END, p.started_at DESC LIMIT $6`,
		f.Open, f.Suppressed, f.DeviceID, f.Severity, f.Since, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Problem
	for rows.Next() {
		p, err := scanProblem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProblemByID loads a problem.
func (s *Store) ProblemByID(ctx context.Context, id int64) (*Problem, error) {
	p, err := scanProblem(s.db.QueryRow(ctx, `SELECT `+problemCols+` FROM problems p JOIN devices d ON d.id=p.device_id LEFT JOIN checks c ON c.id=p.check_id WHERE p.id=$1`, id))
	return p, notFound(err)
}

// ProblemCounts counts open problems that are not suppressed, by severity.
func (s *Store) ProblemCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.Query(ctx, `SELECT severity, count(*) FROM problems WHERE resolved_at IS NULL AND suppressed='' GROUP BY severity`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var sev string
		var n int
		if err := rows.Scan(&sev, &n); err != nil {
			return nil, err
		}
		out[sev] = n
	}
	return out, rows.Err()
}

// OpenProblems returns every open problem (for the alert engine).
func (s *Store) OpenProblems(ctx context.Context) ([]*Problem, error) {
	return s.Problems(ctx, ProblemFilter{Open: true, Suppressed: true, Limit: 1000000})
}

// NewProblem is a problem the engine opens.
type NewProblem struct {
	Key        string
	TriggerID  int64
	DeviceID   int64
	CheckID    int64
	Severity   string
	Title      string
	Detail     string
	Suppressed string
	At         time.Time
}

// OpenProblem records a new problem; it returns 0 when one with the key is
// already open.
func (s *Store) OpenProblem(ctx context.Context, n NewProblem) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO problems(key, trigger_id, device_id, check_id, severity, title, detail, suppressed, started_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (key) WHERE resolved_at IS NULL DO NOTHING RETURNING id`,
		n.Key, nullID(n.TriggerID), n.DeviceID, nullID(n.CheckID), n.Severity, truncate(n.Title, 200), truncate(n.Detail, 500), n.Suppressed, n.At).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) || isFK(err) {
		return 0, nil // already open, or the device or check went away meanwhile
	}
	return id, err
}

// UpdateProblem changes what an open problem says and whether it is
// suppressed.
func (s *Store) UpdateProblem(ctx context.Context, id int64, severity, title, detail, suppressed string) error {
	_, err := s.db.Exec(ctx, `UPDATE problems SET severity=$2, title=$3, detail=$4, suppressed=$5 WHERE id=$1 AND resolved_at IS NULL`,
		id, severity, truncate(title, 200), truncate(detail, 500), suppressed)
	return err
}

// ResolveProblem closes a problem.
func (s *Store) ResolveProblem(ctx context.Context, id int64, at time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE problems SET resolved_at=$2 WHERE id=$1 AND resolved_at IS NULL`, id, at)
	return err
}

// MarkNotified records that problems were sent.
func (s *Store) MarkNotified(ctx context.Context, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.Exec(ctx, `UPDATE problems SET notified_at=$2 WHERE id = ANY($1) AND notified_at IS NULL`, ids, at)
	return err
}

// AckProblem acknowledges an open problem (someone is on it).
func (s *Store) AckProblem(ctx context.Context, id int64, user, note string) error {
	note = strings.TrimSpace(note)
	if len(note) > 500 {
		return errors.New("the note is too long")
	}
	tag, err := s.db.Exec(ctx, `UPDATE problems SET ack_at=$2, ack_by=$3, ack_note=$4 WHERE id=$1 AND resolved_at IS NULL`, id, s.now(), user, note)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// PurgeProblems deletes resolved problems older than the cut-off.
func (s *Store) PurgeProblems(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM problems WHERE resolved_at < $1`, before)
	return tag.RowsAffected(), err
}

// ---- maintenance

// Maintenance is a window in which problems are not sent.
type Maintenance struct {
	ID        int64
	Name      string
	StartsAt  time.Time
	EndsAt    time.Time
	DeviceIDs []int64
	GroupIDs  []int64
	CreatedBy string
	CreatedAt time.Time
}

// Active reports whether the window is on at t.
func (m *Maintenance) Active(t time.Time) bool { return !t.Before(m.StartsAt) && t.Before(m.EndsAt) }

// Covers reports whether the window holds the device.
func (m *Maintenance) Covers(deviceID, groupID int64) bool {
	return slices.Contains(m.DeviceIDs, deviceID) || groupID != 0 && slices.Contains(m.GroupIDs, groupID)
}

// Maintenances lists windows that end after the cut-off, soonest first.
func (s *Store) Maintenances(ctx context.Context, endsAfter time.Time) ([]*Maintenance, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, starts_at, ends_at, device_ids, group_ids, created_by, created_at FROM maintenance
		WHERE ends_at > $1 ORDER BY starts_at, id`, endsAfter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Maintenance
	for rows.Next() {
		m := &Maintenance{}
		if err := rows.Scan(&m.ID, &m.Name, &m.StartsAt, &m.EndsAt, &m.DeviceIDs, &m.GroupIDs, &m.CreatedBy, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CreateMaintenance adds a window.
func (s *Store) CreateMaintenance(ctx context.Context, m Maintenance) (int64, error) {
	var err error
	if m.Name, err = cleanName(m.Name); err != nil {
		return 0, err
	}
	if !m.EndsAt.After(m.StartsAt) {
		return 0, errors.New("the end must be after the start")
	}
	if m.EndsAt.Sub(m.StartsAt) > 366*24*time.Hour {
		return 0, errors.New("a maintenance window can last at most a year")
	}
	if len(m.DeviceIDs)+len(m.GroupIDs) == 0 {
		return 0, errors.New("choose at least one device or group")
	}
	if m.DeviceIDs == nil {
		m.DeviceIDs = []int64{}
	}
	if m.GroupIDs == nil {
		m.GroupIDs = []int64{}
	}
	var id int64
	err = s.db.QueryRow(ctx, `INSERT INTO maintenance(name, starts_at, ends_at, device_ids, group_ids, created_by) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
		m.Name, m.StartsAt, m.EndsAt, m.DeviceIDs, m.GroupIDs, m.CreatedBy).Scan(&id)
	return id, err
}

// EndMaintenance ends a running window now, or removes a planned one.
func (s *Store) EndMaintenance(ctx context.Context, id int64) (*Maintenance, error) {
	m := &Maintenance{}
	err := s.db.QueryRow(ctx, `SELECT id, name, starts_at, ends_at FROM maintenance WHERE id=$1`, id).Scan(&m.ID, &m.Name, &m.StartsAt, &m.EndsAt)
	if err != nil {
		return nil, notFound(err)
	}
	now := s.now()
	if m.StartsAt.After(now) {
		_, err = s.db.Exec(ctx, `DELETE FROM maintenance WHERE id=$1`, id)
	} else {
		_, err = s.db.Exec(ctx, `UPDATE maintenance SET ends_at=GREATEST(starts_at + interval '1 second', $2) WHERE id=$1`, id, now)
	}
	return m, err
}

// ---- notification channels

// Channel kinds.
const (
	ChannelEmail   = "email"
	ChannelWebhook = "webhook"
	ChannelSlack   = "slack"
	ChannelTeams   = "teams"
)

// Channel is where problems are sent.
type Channel struct {
	ID          int64
	Name        string
	Kind        string
	Target      string // recipients (email) or the sealed URL
	Secret      string // sealed HMAC key (webhook)
	MinSeverity string
	OnResolve   bool
	Enabled     bool
	LastSent    *time.Time
	LastError   string
	CreatedAt   time.Time
}

// Channels lists the channels.
func (s *Store) Channels(ctx context.Context) ([]*Channel, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, kind, target, secret, min_severity, on_resolve, enabled, last_sent, last_error, created_at
		FROM notify_channels ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Channel
	for rows.Next() {
		c := &Channel{}
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Target, &c.Secret, &c.MinSeverity, &c.OnResolve, &c.Enabled, &c.LastSent, &c.LastError, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateChannel adds a channel (target and secret already sealed by the
// caller where needed).
func (s *Store) CreateChannel(ctx context.Context, c Channel) (int64, error) {
	var err error
	if c.Name, err = cleanName(c.Name); err != nil {
		return 0, err
	}
	if SeverityRank(c.MinSeverity) < 0 {
		return 0, errors.New("unknown severity")
	}
	var id int64
	err = s.db.QueryRow(ctx, `INSERT INTO notify_channels(name, kind, target, secret, min_severity, on_resolve, enabled) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		c.Name, c.Kind, c.Target, c.Secret, c.MinSeverity, c.OnResolve, c.Enabled).Scan(&id)
	if isUnique(err) {
		return 0, ErrConflict
	}
	return id, err
}

// UpdateChannel changes the name and options of a channel; target and
// secret change only when not empty.
func (s *Store) UpdateChannel(ctx context.Context, c Channel) error {
	var err error
	if c.Name, err = cleanName(c.Name); err != nil {
		return err
	}
	if SeverityRank(c.MinSeverity) < 0 {
		return errors.New("unknown severity")
	}
	tag, err := s.db.Exec(ctx, `UPDATE notify_channels SET name=$2, min_severity=$3, on_resolve=$4, enabled=$5,
		target=CASE WHEN $6='' THEN target ELSE $6 END, secret=CASE WHEN $7='' THEN secret ELSE $7 END WHERE id=$1`,
		c.ID, c.Name, c.MinSeverity, c.OnResolve, c.Enabled, c.Target, c.Secret)
	if isUnique(err) {
		return ErrConflict
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteChannel removes a channel.
func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM notify_channels WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ChannelResult records the outcome of a delivery.
func (s *Store) ChannelResult(ctx context.Context, id int64, at time.Time, sendErr error) error {
	msg := ""
	if sendErr != nil {
		msg = truncate(sendErr.Error(), 300)
	}
	_, err := s.db.Exec(ctx, `UPDATE notify_channels SET last_sent=CASE WHEN $3='' THEN $2 ELSE last_sent END, last_error=$3 WHERE id=$1`, id, at, msg)
	return err
}
