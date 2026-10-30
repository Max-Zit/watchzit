package store

import (
	"context"
	"net/netip"
	"strings"
	"time"
)

// ---- syslog messages and SNMP traps

// Log kinds.
const (
	LogSyslog = "syslog"
	LogTrap   = "trap"
)

// TriggerLog opens problems from syslog messages and traps.
const TriggerLog = "log"

// LogMessage is one received syslog message or trap.
type LogMessage struct {
	ID       int64
	At       time.Time
	DeviceID int64 // 0: from an address no device has
	Device   string
	Source   netip.Addr
	Kind     string
	Facility int // syslog facility (-1 for traps)
	Severity int // syslog level 0 (emergency) … 7 (debug); traps 4 (warning)
	Host     string
	Tag      string
	Message  string
}

// AddLogs stores received messages.
func (s *Store) AddLogs(ctx context.Context, ms []LogMessage) error {
	if len(ms) == 0 {
		return nil
	}
	at, dev, src, kind, fac, sev, host, tag, msg := []time.Time{}, []*int64{}, []string{}, []string{}, []int16{}, []int16{}, []string{}, []string{}, []string{}
	for _, m := range ms {
		at = append(at, m.At)
		if m.DeviceID != 0 {
			id := m.DeviceID
			dev = append(dev, &id)
		} else {
			dev = append(dev, nil)
		}
		src = append(src, m.Source.String())
		kind = append(kind, m.Kind)
		fac = append(fac, int16(m.Facility)) // #nosec G115 -- 0–23 or -1
		sev = append(sev, int16(m.Severity)) // #nosec G115 -- 0–7
		host = append(host, cutText(m.Host, 200))
		tag = append(tag, cutText(m.Tag, 100))
		msg = append(msg, cutText(m.Message, 4000))
	}
	_, err := s.db.Exec(ctx, `INSERT INTO log_messages(at, device_id, source, kind, facility, severity, host, tag, message)
		SELECT * FROM unnest($1::timestamptz[], $2::bigint[], $3::inet[], $4::text[], $5::smallint[], $6::smallint[], $7::text[], $8::text[], $9::text[])`,
		at, dev, src, kind, fac, sev, host, tag, msg)
	return err
}

func cutText(s string, n int) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "?")
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "")
	}
	return s
}

// LogFilter selects messages.
type LogFilter struct {
	DeviceID int64
	Kind     string
	MaxLevel int    // only levels up to this (0–7; 7 = all)
	Text     string // contains, case-insensitive
	Before   int64  // older than this ID (paging)
	Since    time.Time
	Limit    int
}

// Logs lists messages, newest first.
func (s *Store) Logs(ctx context.Context, f LogFilter) ([]*LogMessage, error) {
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 200
	}
	if f.MaxLevel <= 0 || f.MaxLevel > 7 {
		f.MaxLevel = 7
	}
	rows, err := s.db.Query(ctx, `SELECT l.id, l.at, COALESCE(l.device_id,0), COALESCE(d.name,''), host(l.source), l.kind, l.facility, l.severity, l.host, l.tag, l.message
		FROM log_messages l LEFT JOIN devices d ON d.id=l.device_id
		WHERE ($1 = 0 OR l.device_id=$1) AND ($2 = '' OR l.kind=$2) AND l.severity <= $3
		AND ($4 = '' OR l.message ILIKE '%' || $4 || '%' OR l.tag ILIKE '%' || $4 || '%' OR l.host ILIKE '%' || $4 || '%')
		AND ($5 = 0 OR l.id < $5) AND l.at >= $6
		ORDER BY l.id DESC LIMIT $7`,
		f.DeviceID, f.Kind, f.MaxLevel, likeEscape(f.Text), f.Before, f.Since, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LogMessage
	for rows.Next() {
		m := &LogMessage{}
		var src string
		if err := rows.Scan(&m.ID, &m.At, &m.DeviceID, &m.Device, &src, &m.Kind, &m.Facility, &m.Severity, &m.Host, &m.Tag, &m.Message); err != nil {
			return nil, err
		}
		m.Source, _ = netip.ParseAddr(src)
		out = append(out, m)
	}
	return out, rows.Err()
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(s))
}

// PruneLogs deletes messages older than the given time.
func (s *Store) PruneLogs(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM log_messages WHERE at < $1`, before)
	return tag.RowsAffected(), err
}

// DeviceAddresses maps the addresses of devices to their IDs (to know who
// sent a message).
func (s *Store) DeviceAddresses(ctx context.Context) (map[netip.Addr]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT id, address FROM devices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[netip.Addr]int64{}
	for rows.Next() {
		var id int64
		var a string
		if err := rows.Scan(&id, &a); err != nil {
			return nil, err
		}
		if ip, err := netip.ParseAddr(strings.TrimSpace(a)); err == nil {
			if _, dup := out[ip]; !dup {
				out[ip.Unmap()] = id
			}
		}
	}
	return out, rows.Err()
}
