package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ---- configuration backups

// DeviceBackup is how the configuration of a device is read.
type DeviceBackup struct {
	DeviceID  int64
	Device    string
	Address   string
	Enabled   bool
	Method    string
	Port      int
	User      string
	Secret    string // sealed in the database, open here
	Key       string // sealed in the database, open here
	HasSecret bool
	HasKey    bool
	Command   string
	HostKey   string
	LastRun   *time.Time
	LastError string
}

func backupPurpose(dev int64, what string) string {
	return "device_backup." + what + "." + strconv.FormatInt(dev, 10)
}

// ConfigVersion is a stored configuration.
type ConfigVersion struct {
	ID       int64
	DeviceID int64
	At       time.Time
	Hash     string
	Size     int
	Added    int
	Removed  int
	Content  string // only by LatestConfig and ConfigVersionByID
}

const backupCols = `b.device_id, d.name, d.address, b.enabled, b.method, b.port, b.username, b.secret, b.key, b.command, b.host_key, b.last_run, b.last_error`

func (s *Store) scanBackup(row interface{ Scan(...any) error }) (*DeviceBackup, error) {
	b := &DeviceBackup{}
	var secret, key string
	if err := row.Scan(&b.DeviceID, &b.Device, &b.Address, &b.Enabled, &b.Method, &b.Port, &b.User, &secret, &key, &b.Command, &b.HostKey, &b.LastRun, &b.LastError); err != nil {
		return nil, notFound(err)
	}
	b.HasSecret, b.HasKey = secret != "", key != ""
	var err error
	if b.Secret, err = s.box.open(backupPurpose(b.DeviceID, "secret"), secret); err != nil {
		return nil, err
	}
	if b.Key, err = s.box.open(backupPurpose(b.DeviceID, "key"), key); err != nil {
		return nil, err
	}
	return b, nil
}

// DeviceBackupOf returns the backup settings of a device (ErrNotFound: none).
func (s *Store) DeviceBackupOf(ctx context.Context, dev int64) (*DeviceBackup, error) {
	return s.scanBackup(s.db.QueryRow(ctx, `SELECT `+backupCols+` FROM device_backup b JOIN devices d ON d.id=b.device_id WHERE b.device_id=$1`, dev))
}

// DeviceBackups lists the enabled backups of devices that are not paused.
func (s *Store) DeviceBackups(ctx context.Context) ([]*DeviceBackup, error) {
	rows, err := s.db.Query(ctx, `SELECT `+backupCols+` FROM device_backup b JOIN devices d ON d.id=b.device_id WHERE b.enabled AND NOT d.paused ORDER BY d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DeviceBackup
	for rows.Next() {
		b, err := s.scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SaveDeviceBackup stores the settings; an empty secret or key keeps the
// saved one unless its clear flag is set.
func (s *Store) SaveDeviceBackup(ctx context.Context, b DeviceBackup, clearSecret, clearKey bool) error {
	switch b.Method {
	case "mikrotik", "cisco", "fortigate":
		b.Command = ""
	case "command":
		b.Command = strings.TrimSpace(b.Command)
		if b.Command == "" || len(b.Command) > 500 || strings.ContainsAny(b.Command, "\r\n") {
			return errors.New("enter the command that prints the configuration (one line)")
		}
	default:
		return errors.New("unknown backup method")
	}
	b.User = strings.TrimSpace(b.User)
	if b.User == "" || len(b.User) > 100 {
		return errors.New("enter the SSH user")
	}
	if b.Port < 1 || b.Port > 65535 {
		return errors.New("the SSH port must be between 1 and 65535")
	}
	if len(b.Secret) > 500 || len(b.Key) > 16000 {
		return errors.New("the password or key is too long")
	}
	secret, key := s.box.seal(backupPurpose(b.DeviceID, "secret"), b.Secret), s.box.seal(backupPurpose(b.DeviceID, "key"), b.Key)
	_, err := s.db.Exec(ctx, `INSERT INTO device_backup(device_id, enabled, method, port, username, secret, key, command)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (device_id) DO UPDATE SET enabled=$2, method=$3, port=$4, username=$5, command=$8,
		secret = CASE WHEN $9 THEN '' WHEN $6 = '' THEN device_backup.secret ELSE $6 END,
		key = CASE WHEN $10 THEN '' WHEN $7 = '' THEN device_backup.key ELSE $7 END`,
		b.DeviceID, b.Enabled, b.Method, b.Port, b.User, secret, key, b.Command, clearSecret, clearKey)
	if err != nil {
		return err
	}
	var hasSecret, hasKey bool
	if err := s.db.QueryRow(ctx, `SELECT secret <> '', key <> '' FROM device_backup WHERE device_id=$1`, b.DeviceID).Scan(&hasSecret, &hasKey); err != nil {
		return err
	}
	if !hasSecret && !hasKey {
		_ = s.DeleteDeviceBackup(ctx, b.DeviceID)
		return errors.New("enter a password or a private key")
	}
	return nil
}

// DeleteDeviceBackup stops the backups of a device (its versions stay).
func (s *Store) DeleteDeviceBackup(ctx context.Context, dev int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM device_backup WHERE device_id=$1`, dev)
	return err
}

// ForgetHostKey clears the pinned SSH host key.
func (s *Store) ForgetHostKey(ctx context.Context, dev int64) error {
	_, err := s.db.Exec(ctx, `UPDATE device_backup SET host_key='' WHERE device_id=$1`, dev)
	return err
}

// BackupRan records the outcome of a backup (and the host key seen first).
func (s *Store) BackupRan(ctx context.Context, dev int64, at time.Time, hostKey string, runErr error) error {
	msg := ""
	if runErr != nil {
		msg = cutText(runErr.Error(), 500)
	}
	_, err := s.db.Exec(ctx, `UPDATE device_backup SET last_run=$2, last_error=$3, host_key = CASE WHEN host_key = '' THEN $4 ELSE host_key END WHERE device_id=$1`,
		dev, at, msg, hostKey)
	return err
}

const versionCols = `id, device_id, at, hash, size, added, removed`

// LatestConfig is the newest stored configuration of a device (nil: none).
func (s *Store) LatestConfig(ctx context.Context, dev int64) (*ConfigVersion, error) {
	v := &ConfigVersion{}
	err := s.db.QueryRow(ctx, `SELECT `+versionCols+`, content FROM config_versions WHERE device_id=$1 ORDER BY at DESC, id DESC LIMIT 1`, dev).
		Scan(&v.ID, &v.DeviceID, &v.At, &v.Hash, &v.Size, &v.Added, &v.Removed, &v.Content)
	if errors.Is(notFound(err), ErrNotFound) {
		return nil, nil
	}
	return v, err
}

// AddConfigVersion stores a configuration and keeps the newest keep
// versions of the device.
func (s *Store) AddConfigVersion(ctx context.Context, v ConfigVersion, keep int) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO config_versions(device_id, at, hash, size, added, removed, content) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		v.DeviceID, v.At, v.Hash, len(v.Content), v.Added, v.Removed, v.Content).Scan(&id)
	if err != nil {
		return 0, err
	}
	if keep > 0 {
		_, err = s.db.Exec(ctx, `DELETE FROM config_versions WHERE device_id=$1 AND id NOT IN
			(SELECT id FROM config_versions WHERE device_id=$1 ORDER BY at DESC, id DESC LIMIT $2)`, v.DeviceID, keep)
	}
	return id, err
}

// ConfigVersions lists the versions of a device, newest first (no content).
func (s *Store) ConfigVersions(ctx context.Context, dev int64) ([]*ConfigVersion, error) {
	rows, err := s.db.Query(ctx, `SELECT `+versionCols+` FROM config_versions WHERE device_id=$1 ORDER BY at DESC, id DESC`, dev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ConfigVersion
	for rows.Next() {
		v := &ConfigVersion{}
		if err := rows.Scan(&v.ID, &v.DeviceID, &v.At, &v.Hash, &v.Size, &v.Added, &v.Removed); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ConfigVersionByID returns a version with its content and the one before it.
func (s *Store) ConfigVersionByID(ctx context.Context, dev, id int64) (cur, prev *ConfigVersion, err error) {
	cur = &ConfigVersion{}
	err = s.db.QueryRow(ctx, `SELECT `+versionCols+`, content FROM config_versions WHERE device_id=$1 AND id=$2`, dev, id).
		Scan(&cur.ID, &cur.DeviceID, &cur.At, &cur.Hash, &cur.Size, &cur.Added, &cur.Removed, &cur.Content)
	if err != nil {
		return nil, nil, notFound(err)
	}
	p := &ConfigVersion{}
	err = s.db.QueryRow(ctx, `SELECT `+versionCols+`, content FROM config_versions WHERE device_id=$1 AND (at, id) < ($2, $3) ORDER BY at DESC, id DESC LIMIT 1`,
		dev, cur.At, cur.ID).Scan(&p.ID, &p.DeviceID, &p.At, &p.Hash, &p.Size, &p.Added, &p.Removed, &p.Content)
	if err == nil {
		prev = p
	} else if !errors.Is(notFound(err), ErrNotFound) {
		return nil, nil, err
	}
	return cur, prev, nil
}
