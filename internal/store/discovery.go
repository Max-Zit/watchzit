package store

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

// ---- network discovery

// DiscoveryRange is an address range to scan.
type DiscoveryRange struct {
	ID        int64
	CIDR      string
	Every     string // "", day, week
	LastRun   *time.Time
	LastFound int
}

// Due reports whether a scheduled range should run at now.
func (r *DiscoveryRange) Due(now time.Time) bool {
	var gap time.Duration
	switch r.Every {
	case "day":
		gap = 24 * time.Hour
	case "week":
		gap = 7 * 24 * time.Hour
	default:
		return false
	}
	return r.LastRun == nil || now.Sub(*r.LastRun) >= gap-time.Minute
}

// DiscoveredHost is a host a scan found that is not a device yet.
type DiscoveredHost struct {
	Address   netip.Addr
	RangeID   int64
	Name      string
	DNSName   string
	SysName   string
	SysDescr  string
	Vendor    string
	Kind      string
	Ports     []int32
	Ping      bool
	SNMP      int64
	FirstSeen time.Time
	LastSeen  time.Time
}

// DiscoveryRanges lists the ranges.
func (s *Store) DiscoveryRanges(ctx context.Context) ([]*DiscoveryRange, error) {
	rows, err := s.db.Query(ctx, `SELECT id, cidr, every, last_run, last_found FROM discovery_ranges ORDER BY cidr`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DiscoveryRange
	for rows.Next() {
		r := &DiscoveryRange{}
		if err := rows.Scan(&r.ID, &r.CIDR, &r.Every, &r.LastRun, &r.LastFound); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateDiscoveryRange adds a range (already checked by the caller).
func (s *Store) CreateDiscoveryRange(ctx context.Context, cidr, every string) (int64, error) {
	if every != "" && every != "day" && every != "week" {
		return 0, errors.New("unknown schedule")
	}
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO discovery_ranges(cidr, every) VALUES($1,$2) RETURNING id`, cidr, every).Scan(&id)
	if isUnique(err) {
		return 0, ErrConflict
	}
	return id, err
}

// SetDiscoveryRange changes the schedule of a range.
func (s *Store) SetDiscoveryRange(ctx context.Context, id int64, every string) error {
	if every != "" && every != "day" && every != "week" {
		return errors.New("unknown schedule")
	}
	_, err := s.db.Exec(ctx, `UPDATE discovery_ranges SET every=$2 WHERE id=$1`, id, every)
	return err
}

// DeleteDiscoveryRange removes a range and the hosts only it found.
func (s *Store) DeleteDiscoveryRange(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM discovery_ranges WHERE id=$1`, id)
	return err
}

// SaveDiscovery records a scan of a range: hosts that are devices already
// are left out; new ones are added, known ones refreshed.
func (s *Store) SaveDiscovery(ctx context.Context, rangeID int64, hosts []DiscoveredHost, at time.Time) error {
	known, err := s.DeviceAddresses(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	n := 0
	for _, h := range hosts {
		if _, dev := known[h.Address]; dev {
			continue
		}
		n++
		var prof *int64
		if h.SNMP != 0 {
			prof = &h.SNMP
		}
		if _, err := tx.Exec(ctx, `INSERT INTO discovered_hosts(address, range_id, name, dns_name, sys_name, sys_descr, vendor, kind, ports, ping, snmp_profile, first_seen, last_seen)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)
			ON CONFLICT (address) DO UPDATE SET range_id=EXCLUDED.range_id, name=EXCLUDED.name, dns_name=EXCLUDED.dns_name, sys_name=EXCLUDED.sys_name,
			sys_descr=EXCLUDED.sys_descr, vendor=EXCLUDED.vendor, kind=EXCLUDED.kind, ports=EXCLUDED.ports, ping=EXCLUDED.ping, snmp_profile=EXCLUDED.snmp_profile, last_seen=EXCLUDED.last_seen`,
			h.Address.String(), rangeID, cutText(h.Name, 100), cutText(h.DNSName, 255), cutText(h.SysName, 255), cutText(h.SysDescr, 500), h.Vendor, h.Kind, h.Ports, h.Ping, prof, at); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE discovery_ranges SET last_run=$2, last_found=$3 WHERE id=$1`, rangeID, at, n); err != nil {
		return err
	}
	// hosts that became devices meanwhile
	if _, err := tx.Exec(ctx, `DELETE FROM discovered_hosts h USING devices d WHERE d.address = host(h.address)`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DiscoveredHosts lists the hosts found that are not devices (dismissed
// ones only when asked).
func (s *Store) DiscoveredHosts(ctx context.Context, dismissed bool) ([]*DiscoveredHost, error) {
	rows, err := s.db.Query(ctx, `SELECT host(h.address), COALESCE(h.range_id,0), h.name, h.dns_name, h.sys_name, h.sys_descr, h.vendor, h.kind, h.ports, h.ping,
		COALESCE(h.snmp_profile,0), h.first_seen, h.last_seen FROM discovered_hosts h
		WHERE h.dismissed = $1 AND NOT EXISTS (SELECT 1 FROM devices d WHERE d.address = host(h.address)) ORDER BY h.address`, dismissed)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DiscoveredHost
	for rows.Next() {
		h := &DiscoveredHost{}
		var a string
		if err := rows.Scan(&a, &h.RangeID, &h.Name, &h.DNSName, &h.SysName, &h.SysDescr, &h.Vendor, &h.Kind, &h.Ports, &h.Ping, &h.SNMP, &h.FirstSeen, &h.LastSeen); err != nil {
			return nil, err
		}
		h.Address, _ = netip.ParseAddr(a)
		out = append(out, h)
	}
	return out, rows.Err()
}

// DismissHosts hides found hosts (or shows them again).
func (s *Store) DismissHosts(ctx context.Context, addrs []string, dismiss bool) error {
	_, err := s.db.Exec(ctx, `UPDATE discovered_hosts SET dismissed=$2 WHERE host(address) = ANY($1)`, addrs, dismiss)
	return err
}
