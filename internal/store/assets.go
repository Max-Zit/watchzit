package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Asset is an uploaded picture: a map background or a device icon.
type Asset struct {
	ID        int64       `json:"id"`
	Kind      string      `json:"kind"`
	Name      string      `json:"name"`
	Mime      string      `json:"mime"`
	Width     int         `json:"w"`
	Height    int         `json:"h"`
	Size      int         `json:"size"`
	Meta      PictureMeta `json:"meta"`
	CreatedAt time.Time   `json:"-"`
}

// PictureMeta is what a picture of the library shows: its manufacturer,
// type (a device kind, "storage", "rack", "logical", "power" or "other"),
// rack units (0: not rack-mounted), default width on a map and the port
// positions in picture coordinates, port 1 first; the last Fiber ports are
// SFP cages.
type PictureMeta struct {
	Vendor string       `json:"vendor,omitempty"`
	Type   string       `json:"type,omitempty"`
	U      int          `json:"u,omitempty"`
	Show   float64      `json:"show,omitempty"`
	Ports  [][2]float64 `json:"ports,omitempty"`
	Fiber  int          `json:"fiber,omitempty"`
	// Replaces names the built-in picture this one stands for on every map
	// (an edited built-in picture); empty for pictures of their own.
	Replaces string `json:"replaces,omitempty"`
}

// PictureTypes are the types a picture can have.
var PictureTypes = []string{"switch", "router", "firewall", "server", "storage", "accesspoint", "ups", "power", "camera", "pc", "printer", "rack", "logical", "other"}

// MaxAssetSize limits uploads (a floor plan photo stays well below).
const MaxAssetSize = 8 << 20

// CreateAsset stores a picture the web layer has checked.
func (s *Store) CreateAsset(ctx context.Context, kind, name, mime string, data []byte, w, h int) (int64, error) {
	if kind != "background" && kind != "icon" {
		return 0, errors.New("unknown picture kind")
	}
	if len(data) == 0 || len(data) > MaxAssetSize {
		return 0, errors.New("the picture must be at most 8 MB")
	}
	name, err := cleanName(name)
	if err != nil {
		name = "picture"
	}
	var id int64
	err = s.db.QueryRow(ctx, `INSERT INTO map_assets(kind, name, mime, data, width, height, created_at) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		kind, name, mime, data, w, h, s.now()).Scan(&id)
	return id, err
}

// Assets lists the pictures of a kind (without their data).
func (s *Store) Assets(ctx context.Context, kind string) ([]Asset, error) {
	rows, err := s.db.Query(ctx, `SELECT id, kind, name, mime, width, height, length(data), created_at, meta FROM map_assets WHERE kind=$1 ORDER BY lower(name), id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		var a Asset
		if err := rows.Scan(&a.ID, &a.Kind, &a.Name, &a.Mime, &a.Width, &a.Height, &a.Size, &a.CreatedAt, &a.Meta); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AssetData returns a picture with its bytes.
func (s *Store) AssetData(ctx context.Context, id int64) (*Asset, []byte, error) {
	a := &Asset{}
	var data []byte
	err := s.db.QueryRow(ctx, `SELECT id, kind, name, mime, width, height, data, created_at, meta FROM map_assets WHERE id=$1`, id).
		Scan(&a.ID, &a.Kind, &a.Name, &a.Mime, &a.Width, &a.Height, &data, &a.CreatedAt, &a.Meta)
	if err != nil {
		return nil, nil, notFound(err)
	}
	a.Size = len(data)
	return a, data, nil
}

// ErrInUse means a picture is still shown on a map.
var ErrInUse = errors.New("the picture is still used on a map")

// DeleteAsset removes a picture no map uses.
func (s *Store) DeleteAsset(ctx context.Context, id int64) error {
	var used int
	if err := s.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM map_nodes WHERE icon=$2) + (SELECT count(*) FROM maps WHERE bg_asset=$1)`,
		id, "asset:"+strconv.FormatInt(id, 10)).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return ErrInUse
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM map_assets WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---- kiosk displays

// Kiosk is a display that shows maps through a secret link.
type Kiosk struct {
	ID        int64
	Name      string
	Maps      []int64
	RotateS   int
	Theme     string
	CreatedAt time.Time
	LastSeen  *time.Time
	LastIP    string
}

func tokenHash(token string) []byte {
	h := sha256.Sum256([]byte("watchzit-kiosk:" + token))
	return h[:]
}

// KioskInput is what the kiosk form sets.
type KioskInput struct {
	Name    string
	Maps    []int64
	RotateS int
	Theme   string
}

func (in *KioskInput) clean() error {
	var err error
	if in.Name, err = cleanName(in.Name); err != nil {
		return err
	}
	if len(in.Maps) == 0 || len(in.Maps) > 50 {
		return errors.New("choose at least one map")
	}
	if in.RotateS == 0 {
		in.RotateS = 30
	}
	if in.RotateS < 5 || in.RotateS > 3600 {
		return errors.New("maps change every 5–3600 seconds")
	}
	in.Theme = oneOf(in.Theme, []string{"dark", "light"}, "dark")
	return nil
}

// CreateKiosk adds a display and returns its secret token (shown once).
func (s *Store) CreateKiosk(ctx context.Context, in KioskInput) (int64, string, error) {
	if err := in.clean(); err != nil {
		return 0, "", err
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return 0, "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO kiosks(name, token_hash, maps, rotate_s, theme, created_at) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
		in.Name, tokenHash(token), in.Maps, in.RotateS, in.Theme, s.now()).Scan(&id)
	return id, token, err
}

// UpdateKiosk changes a display (the link stays).
func (s *Store) UpdateKiosk(ctx context.Context, id int64, in KioskInput) error {
	if err := in.clean(); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE kiosks SET name=$2, maps=$3, rotate_s=$4, theme=$5 WHERE id=$1`, id, in.Name, in.Maps, in.RotateS, in.Theme)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteKiosk revokes a display's link.
func (s *Store) DeleteKiosk(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM kiosks WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

const kioskCols = `id, name, maps, rotate_s, theme, created_at, last_seen, last_ip`

func scanKiosk(row interface{ Scan(...any) error }) (*Kiosk, error) {
	k := &Kiosk{}
	err := row.Scan(&k.ID, &k.Name, &k.Maps, &k.RotateS, &k.Theme, &k.CreatedAt, &k.LastSeen, &k.LastIP)
	return k, err
}

// Kiosks lists the displays.
func (s *Store) Kiosks(ctx context.Context) ([]*Kiosk, error) {
	rows, err := s.db.Query(ctx, `SELECT `+kioskCols+` FROM kiosks ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Kiosk
	for rows.Next() {
		k, err := scanKiosk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// KioskByID loads a display.
func (s *Store) KioskByID(ctx context.Context, id int64) (*Kiosk, error) {
	k, err := scanKiosk(s.db.QueryRow(ctx, `SELECT `+kioskCols+` FROM kiosks WHERE id=$1`, id))
	return k, notFound(err)
}

// KioskByToken finds the display of a link and notes that it was seen.
func (s *Store) KioskByToken(ctx context.Context, token, ip string) (*Kiosk, error) {
	if len(token) < 20 || len(token) > 64 || strings.ContainsAny(token, "/?#") {
		return nil, ErrNotFound
	}
	k, err := scanKiosk(s.db.QueryRow(ctx, `UPDATE kiosks SET last_seen=$2, last_ip=$3 WHERE token_hash=$1 RETURNING `+kioskCols, tokenHash(token), s.now(), ip))
	return k, notFound(err)
}
