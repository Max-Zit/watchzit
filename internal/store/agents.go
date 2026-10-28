package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/max-zit/watchzit/internal/proto"
)

// ---- enrollment tokens

// EnrollToken lets computers join (one code for many, with limits).
type EnrollToken struct {
	ID        int64
	Name      string
	GroupID   int64
	Group     string
	CreatedAt time.Time
	CreatedBy string
	ExpiresAt *time.Time
	MaxUses   *int
	Uses      int
	Revoked   bool
}

// Active tells whether the token still enrolls.
func (t *EnrollToken) Active(now time.Time) bool {
	return !t.Revoked && (t.ExpiresAt == nil || now.Before(*t.ExpiresAt)) && (t.MaxUses == nil || t.Uses < *t.MaxUses)
}

func enrollHash(token string) []byte {
	h := sha256.Sum256([]byte("watchzit-enroll:" + token))
	return h[:]
}

// CreateEnrollToken adds a token and returns its secret (shown once).
func (s *Store) CreateEnrollToken(ctx context.Context, name string, groupID int64, validDays, maxUses int, by string) (int64, string, error) {
	name, err := cleanName(name)
	if err != nil {
		return 0, "", err
	}
	if validDays < 0 || validDays > 3650 || maxUses < 0 || maxUses > 100000 {
		return 0, "", errors.New("the code can be valid for up to 3650 days and up to 100000 computers")
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return 0, "", err
	}
	token := "wzet_" + base64.RawURLEncoding.EncodeToString(b)
	var exp *time.Time
	if validDays > 0 {
		t := s.now().AddDate(0, 0, validDays)
		exp = &t
	}
	var mu *int
	if maxUses > 0 {
		mu = &maxUses
	}
	var id int64
	err = s.db.QueryRow(ctx, `INSERT INTO enroll_tokens(name, token_hash, group_id, created_at, created_by, expires_at, max_uses) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		name, enrollHash(token), nullID(groupID), s.now(), by, exp, mu).Scan(&id)
	if isFK(err) {
		return 0, "", errors.New("the group does not exist")
	}
	return id, token, err
}

// EnrollTokens lists the tokens, newest first.
func (s *Store) EnrollTokens(ctx context.Context) ([]*EnrollToken, error) {
	rows, err := s.db.Query(ctx, `SELECT t.id, t.name, COALESCE(t.group_id,0), COALESCE(g.name,''), t.created_at, t.created_by, t.expires_at, t.max_uses, t.uses, t.revoked
		FROM enroll_tokens t LEFT JOIN device_groups g ON g.id=t.group_id ORDER BY t.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*EnrollToken
	for rows.Next() {
		t := &EnrollToken{}
		if err := rows.Scan(&t.ID, &t.Name, &t.GroupID, &t.Group, &t.CreatedAt, &t.CreatedBy, &t.ExpiresAt, &t.MaxUses, &t.Uses, &t.Revoked); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeEnrollToken stops a token (enrolled agents stay).
func (s *Store) RevokeEnrollToken(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `UPDATE enroll_tokens SET revoked=true WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ErrTokenInvalid: the enrollment token is wrong, expired, revoked or used up.
var ErrTokenInvalid = errors.New("invalid enrollment token")

// ---- agents on devices

// AgentInfo is the agent side of a device.
type AgentInfo struct {
	DeviceID  int64
	Device    string
	AgentID   string
	Version   string
	OS        string
	Seen      *time.Time
	IP        string
	Online    bool
	Inventory proto.Inventory
}

// KeyID is the agent id derived from its public key.
func KeyID(pub []byte) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

// EnrollAgent joins a computer: the token is checked and counted; an
// existing device with one of the computer's addresses or its host name
// (and no agent yet) gets the agent, otherwise a new device is made.
func (s *Store) EnrollAgent(ctx context.Context, token string, pub []byte, hostname, osName, version string, addrs []string, ip string) (*AgentInfo, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var tid int64
	var group *int64
	var hash []byte
	err = tx.QueryRow(ctx, `SELECT id, group_id, token_hash FROM enroll_tokens WHERE token_hash=$1 AND NOT revoked
		AND (expires_at IS NULL OR expires_at > $2) AND (max_uses IS NULL OR uses < max_uses) FOR UPDATE`, enrollHash(token), s.now()).Scan(&tid, &group, &hash)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && subtle.ConstantTimeCompare(hash, enrollHash(token)) != 1) {
		return nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM devices WHERE agent_key=$1)`, pub).Scan(&taken); err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrConflict
	}
	hostname = strings.TrimSpace(hostname)
	if len(hostname) > 100 || hostname == "" {
		hostname = "computer-" + KeyID(pub)[:6]
	}
	var valid []string
	for _, a := range addrs {
		if ap, err := netip.ParseAddr(a); err == nil && !ap.IsLoopback() && len(valid) < 32 {
			valid = append(valid, ap.String())
		}
	}
	// an existing device without an agent: same address, else same name
	var id int64
	var name string
	err = tx.QueryRow(ctx, `SELECT id, name FROM devices WHERE agent_key IS NULL AND (address = ANY($1) OR lower(name)=lower($2))
		ORDER BY (address = ANY($1)) DESC, id LIMIT 1`, valid, hostname).Scan(&id, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		addr := ip
		if len(valid) > 0 {
			addr = valid[0]
		}
		kind := "pc"
		if l := strings.ToLower(osName); strings.Contains(l, "server") || !strings.Contains(l, "windows") {
			kind = "server"
		}
		name = hostname
		for i := 2; ; i++ {
			err = tx.QueryRow(ctx, `INSERT INTO devices(group_id, name, address, kind, status, status_since) VALUES($1,$2,$3,$4,'unknown',$5)
				ON CONFLICT DO NOTHING RETURNING id`, group, name, addr, kind, s.now()).Scan(&id)
			if !errors.Is(err, pgx.ErrNoRows) || i > 50 {
				break
			}
			name = fmt.Sprintf("%s-%d", hostname, i)
		}
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE devices SET agent_key=$2, agent_id=$3, agent_version=$4, agent_os=$5, agent_ip=$6, agent_seen=$7 WHERE id=$1`,
		id, pub, KeyID(pub), version, truncate(osName, 200), ip, s.now()); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE enroll_tokens SET uses=uses+1 WHERE id=$1`, tid); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &AgentInfo{DeviceID: id, Device: name, AgentID: KeyID(pub), Version: version, OS: osName}, nil
}

const agentCols = `id, name, agent_id, agent_version, agent_os, agent_seen, agent_ip, agent_online, agent_inventory`

func scanAgent(row pgx.Row) (*AgentInfo, error) {
	a := &AgentInfo{}
	var inv []byte
	err := row.Scan(&a.DeviceID, &a.Device, &a.AgentID, &a.Version, &a.OS, &a.Seen, &a.IP, &a.Online, &inv)
	if err == nil {
		_ = json.Unmarshal(inv, &a.Inventory)
	}
	return a, notFound(err)
}

// AgentByKey finds the device of an agent key.
func (s *Store) AgentByKey(ctx context.Context, pub []byte) (*AgentInfo, error) {
	return scanAgent(s.db.QueryRow(ctx, `SELECT `+agentCols+` FROM devices WHERE agent_key=$1`, pub))
}

// DeviceAgent returns the agent of a device (ErrNotFound without one).
func (s *Store) DeviceAgent(ctx context.Context, deviceID int64) (*AgentInfo, error) {
	return scanAgent(s.db.QueryRow(ctx, `SELECT `+agentCols+` FROM devices WHERE id=$1 AND agent_key IS NOT NULL`, deviceID))
}

// AgentSeen notes a connection (hello) or its end.
func (s *Store) AgentSeen(ctx context.Context, deviceID int64, version, osName, ip string, online bool) error {
	_, err := s.db.Exec(ctx, `UPDATE devices SET agent_version=COALESCE(NULLIF($2,''), agent_version), agent_os=COALESCE(NULLIF($3,''), agent_os),
		agent_ip=COALESCE(NULLIF($4,''), agent_ip), agent_online=$5, agent_seen=$6 WHERE id=$1`, deviceID, version, truncate(osName, 200), ip, online, s.now())
	return err
}

// SetAgentInventory stores what the agent reported about the machine
// and fills the device facts from it.
func (s *Store) SetAgentInventory(ctx context.Context, deviceID int64, inv *proto.Inventory) error {
	if len(inv.Services) > 2000 {
		inv.Services = inv.Services[:2000]
	}
	raw, _ := json.Marshal(inv)
	f, err := s.DeviceFacts(ctx, deviceID)
	if err != nil {
		return err
	}
	if f.SysName == "" || f.Updated.IsZero() { // SNMP facts win when both exist
		f.SysName = inv.Hostname
		f.SysDescr = strings.TrimSpace(inv.OS + " · " + inv.CPUModel)
		f.Vendor = strings.TrimSpace(inv.Vendor + " " + inv.Model)
	}
	fraw, _ := json.Marshal(f)
	_, err = s.db.Exec(ctx, `UPDATE devices SET agent_inventory=$2, facts=$3 WHERE id=$1`, deviceID, raw, fraw)
	return err
}

// ForgetAgent removes the agent from a device (the device and its data
// stay; the computer must enroll again).
func (s *Store) ForgetAgent(ctx context.Context, deviceID int64) error {
	_, err := s.db.Exec(ctx, `UPDATE devices SET agent_key=NULL, agent_id='', agent_online=false WHERE id=$1`, deviceID)
	return err
}

// Agents lists the devices with an agent.
func (s *Store) Agents(ctx context.Context) ([]*AgentInfo, error) {
	rows, err := s.db.Query(ctx, `SELECT `+agentCols+` FROM devices WHERE agent_key IS NOT NULL ORDER BY agent_online, lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AgentInfo
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
