package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/snmp"
)

// SNMPProfile is a set of SNMP credentials shared by devices. Secrets are
// never read back into forms (write-only); Config opens them for polling.
type SNMPProfile struct {
	ID        int64
	Name      string
	Version   int // 1, 2 (v2c), 3
	Port      int
	User      string
	AuthProto string
	PrivProto string
	Context   string
	HasSecret bool // community or passwords are set
	Devices   int
	CreatedAt time.Time

	community, authPass, privPass string // sealed
}

// VersionName is "v1", "v2c" or "v3".
func (p *SNMPProfile) VersionName() string {
	return map[int]string{1: "v1", 2: "v2c", 3: "v3"}[p.Version]
}

// Security sums up the v3 security level for lists.
func (p *SNMPProfile) Security() string {
	if p.Version != 3 {
		return ""
	}
	switch {
	case p.PrivProto != "":
		return p.AuthProto + " / " + p.PrivProto
	case p.AuthProto != "":
		return p.AuthProto
	}
	return "noAuthNoPriv"
}

const profileCols = `p.id, p.name, p.version, p.port, p.community, p.v3_user, p.auth_proto, p.auth_pass, p.priv_proto, p.priv_pass, p.context, p.created_at,
	(SELECT count(*) FROM devices d WHERE d.snmp_profile_id=p.id)`

func scanProfile(row interface{ Scan(...any) error }) (*SNMPProfile, error) {
	p := &SNMPProfile{}
	err := row.Scan(&p.ID, &p.Name, &p.Version, &p.Port, &p.community, &p.User, &p.AuthProto, &p.authPass, &p.PrivProto, &p.privPass, &p.Context, &p.CreatedAt, &p.Devices)
	p.HasSecret = p.community != "" || p.authPass != ""
	return p, err
}

// SNMPProfiles lists the profiles.
func (s *Store) SNMPProfiles(ctx context.Context) ([]*SNMPProfile, error) {
	rows, err := s.db.Query(ctx, `SELECT `+profileCols+` FROM snmp_profiles p ORDER BY lower(p.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SNMPProfile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SNMPProfileByID loads one profile.
func (s *Store) SNMPProfileByID(ctx context.Context, id int64) (*SNMPProfile, error) {
	p, err := scanProfile(s.db.QueryRow(ctx, `SELECT `+profileCols+` FROM snmp_profiles p WHERE p.id=$1`, id))
	return p, notFound(err)
}

// SNMPProfileInput is what the profile form sets. Empty secrets keep the
// stored ones on update.
type SNMPProfileInput struct {
	Name      string
	Version   int
	Port      int
	Community string
	User      string
	AuthProto string
	AuthPass  string
	PrivProto string
	PrivPass  string
	Context   string
}

func validProto(p string, list []string) bool {
	if p == "" {
		return true
	}
	for _, x := range list {
		if x == p {
			return true
		}
	}
	return false
}

// clean checks the input; old is the stored profile on update (nil on create).
func (in *SNMPProfileInput) clean(old *SNMPProfile) error {
	var err error
	if in.Name, err = cleanName(in.Name); err != nil {
		return err
	}
	if in.Port == 0 {
		in.Port = 161
	}
	if in.Port < 1 || in.Port > 65535 {
		return errors.New("the port must be 1–65535")
	}
	for _, s := range []string{in.Community, in.User, in.AuthPass, in.PrivPass, in.Context} {
		if len(s) > 128 || strings.ContainsAny(s, "\x00\r\n") {
			return errors.New("SNMP names and passwords must be up to 128 characters on one line")
		}
	}
	switch in.Version {
	case 1, 2:
		in.User, in.AuthProto, in.AuthPass, in.PrivProto, in.PrivPass, in.Context = "", "", "", "", "", ""
		if in.Community == "" && (old == nil || old.community == "" || old.Version == 3) {
			return errors.New("enter the community")
		}
	case 3:
		in.Community = ""
		in.User = strings.TrimSpace(in.User)
		if !validProto(in.AuthProto, snmp.AuthProtocols) || !validProto(in.PrivProto, snmp.PrivProtocols) {
			return errors.New("unknown SNMPv3 protocol")
		}
		if in.AuthProto == "" {
			in.AuthPass, in.PrivProto, in.PrivPass = "", "", ""
		}
		if in.PrivProto == "" {
			in.PrivPass = ""
		}
		// check with the stored passwords where the form left them empty
		chk := snmp.V3{User: in.User, AuthProto: in.AuthProto, AuthPass: in.AuthPass, PrivProto: in.PrivProto, PrivPass: in.PrivPass}
		if old != nil && old.Version == 3 {
			if chk.AuthPass == "" && in.AuthProto != "" && old.authPass != "" {
				chk.AuthPass = "stored-password"
			}
			if chk.PrivPass == "" && in.PrivProto != "" && old.privPass != "" {
				chk.PrivPass = "stored-password"
			}
		}
		if err := chk.Check(); err != nil {
			return err
		}
	default:
		return errors.New("choose the SNMP version")
	}
	return nil
}

// CreateSNMPProfile adds a profile.
func (s *Store) CreateSNMPProfile(ctx context.Context, in SNMPProfileInput) (int64, error) {
	if err := in.clean(nil); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO snmp_profiles(name, version, port, community, v3_user, auth_proto, auth_pass, priv_proto, priv_pass, context)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		in.Name, in.Version, in.Port, s.box.seal("snmp.community", in.Community), in.User, in.AuthProto,
		s.box.seal("snmp.auth", in.AuthPass), in.PrivProto, s.box.seal("snmp.priv", in.PrivPass), in.Context).Scan(&id)
	if isUnique(err) {
		return 0, ErrConflict
	}
	return id, err
}

// UpdateSNMPProfile changes a profile; empty secrets keep the stored ones.
func (s *Store) UpdateSNMPProfile(ctx context.Context, id int64, in SNMPProfileInput) error {
	old, err := s.SNMPProfileByID(ctx, id)
	if err != nil {
		return err
	}
	if err := in.clean(old); err != nil {
		return err
	}
	// clean made sure a stored secret exists wherever the form left one empty
	keep := func(purpose, plain, sealed string, used bool) string {
		switch {
		case !used:
			return ""
		case plain == "":
			return sealed
		}
		return s.box.seal(purpose, plain)
	}
	comm := keep("snmp.community", in.Community, old.community, in.Version != 3)
	auth := keep("snmp.auth", in.AuthPass, old.authPass, in.AuthProto != "")
	priv := keep("snmp.priv", in.PrivPass, old.privPass, in.PrivProto != "")
	_, err = s.db.Exec(ctx, `UPDATE snmp_profiles SET name=$2, version=$3, port=$4, community=$5, v3_user=$6, auth_proto=$7, auth_pass=$8, priv_proto=$9, priv_pass=$10, context=$11 WHERE id=$1`,
		id, in.Name, in.Version, in.Port, comm, in.User, in.AuthProto, auth, in.PrivProto, priv, in.Context)
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

// DeleteSNMPProfile removes a profile (its devices stop SNMP polling).
func (s *Store) DeleteSNMPProfile(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM snmp_profiles WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SNMPConfig opens the secrets of a profile for the poller.
func (s *Store) SNMPConfig(p *SNMPProfile) (snmp.Config, error) {
	cfg := snmp.Config{Version: p.Version}
	var err error
	if p.Version == 3 {
		cfg.V3 = snmp.V3{User: p.User, AuthProto: p.AuthProto, PrivProto: p.PrivProto, Context: p.Context}
		if cfg.V3.AuthPass, err = s.box.open("snmp.auth", p.authPass); err != nil {
			return cfg, err
		}
		cfg.V3.PrivPass, err = s.box.open("snmp.priv", p.privPass)
		return cfg, err
	}
	cfg.Community, err = s.box.open("snmp.community", p.community)
	return cfg, err
}

// DeviceSNMP returns the profile of a device (ErrNotFound without one).
func (s *Store) DeviceSNMP(ctx context.Context, deviceID int64) (*SNMPProfile, error) {
	p, err := scanProfile(s.db.QueryRow(ctx, `SELECT `+profileCols+` FROM snmp_profiles p JOIN devices d ON d.snmp_profile_id=p.id WHERE d.id=$1`, deviceID))
	return p, notFound(err)
}

// ---- facts and interfaces

// Facts is what a device tells about itself.
type Facts struct {
	SysName     string    `json:"sys_name,omitempty"`
	SysDescr    string    `json:"sys_descr,omitempty"`
	SysObjectID string    `json:"sys_object_id,omitempty"`
	SysLocation string    `json:"sys_location,omitempty"`
	SysContact  string    `json:"sys_contact,omitempty"`
	Vendor      string    `json:"vendor,omitempty"`
	Updated     time.Time `json:"updated,omitzero"`
}

// SetFacts stores what discovery learned.
func (s *Store) SetFacts(ctx context.Context, deviceID int64, f Facts) error {
	raw, _ := json.Marshal(f)
	_, err := s.db.Exec(ctx, `UPDATE devices SET facts=$2 WHERE id=$1`, deviceID, raw)
	return err
}

// DeviceFacts reads them.
func (s *Store) DeviceFacts(ctx context.Context, deviceID int64) (Facts, error) {
	var raw []byte
	var f Facts
	err := s.db.QueryRow(ctx, `SELECT facts FROM devices WHERE id=$1`, deviceID).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &f)
	}
	return f, notFound(err)
}

// Interface is a network interface of a device.
type Interface struct {
	Index   int
	Name    string
	Alias   string
	Descr   string
	Type    int
	Speed   int64 // bits per second
	MAC     string
	AdminUp bool
	OperUp  bool
	Updated time.Time
}

// Label is the name people know the port by.
func (i *Interface) Label() string {
	if i.Name != "" {
		return i.Name
	}
	return i.Descr
}

// SetInterfaces replaces the interface list of a device.
func (s *Store) SetInterfaces(ctx context.Context, deviceID int64, ifs []Interface) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM device_interfaces WHERE device_id=$1`, deviceID); err != nil {
		return err
	}
	now := s.now()
	for _, i := range ifs {
		if _, err := tx.Exec(ctx, `INSERT INTO device_interfaces(device_id, if_index, name, alias, descr, if_type, speed_bps, mac, admin_up, oper_up, updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, deviceID, i.Index, truncate(i.Name, 200), truncate(i.Alias, 200), truncate(i.Descr, 300), i.Type, i.Speed, i.MAC, i.AdminUp, i.OperUp, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Interfaces lists the interfaces of a device.
func (s *Store) Interfaces(ctx context.Context, deviceID int64) ([]Interface, error) {
	rows, err := s.db.Query(ctx, `SELECT if_index, name, alias, descr, if_type, speed_bps, mac, admin_up, oper_up, updated_at FROM device_interfaces WHERE device_id=$1 ORDER BY if_index`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Interface
	for rows.Next() {
		var i Interface
		if err := rows.Scan(&i.Index, &i.Name, &i.Alias, &i.Descr, &i.Type, &i.Speed, &i.MAC, &i.AdminUp, &i.OperUp, &i.Updated); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// DeviceDescrs returns what SNMP told about each device: the system
// description and the vendor (the map picks a model picture by them).
func (s *Store) DeviceDescrs(ctx context.Context) (map[int64][2]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id, COALESCE(facts->>'sys_descr',''), COALESCE(facts->>'vendor','') FROM devices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][2]string{}
	for rows.Next() {
		var id int64
		var d [2]string
		if err := rows.Scan(&id, &d[0], &d[1]); err != nil {
			return nil, err
		}
		out[id] = d
	}
	return out, rows.Err()
}
