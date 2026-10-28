package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/max-zit/watchzit/internal/auth"
)

// ---- REST API tokens

// APIToken is a token for the REST API; it acts as its user.
type APIToken struct {
	ID        int64
	UserID    int64
	Name      string
	CreatedAt time.Time
	ExpiresAt *time.Time
	LastUsed  *time.Time
	LastIP    string
}

// APITokenPrefix starts every API token (easy to recognise in secret scans).
const APITokenPrefix = "wzat_"

// ErrBadToken is returned for unknown, expired or disabled tokens.
var ErrBadToken = errors.New("invalid API token")

// CreateAPIToken makes a token for a user; the plain token is returned once.
func (s *Store) CreateAPIToken(ctx context.Context, userID int64, name string, validDays int) (int64, string, error) {
	var err error
	if name, err = cleanName(name); err != nil {
		return 0, "", err
	}
	if validDays < 0 || validDays > 3650 {
		return 0, "", errors.New("a token can be valid for up to 3650 days (0 = no limit)")
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE user_id=$1`, userID).Scan(&n); err != nil {
		return 0, "", err
	}
	if n >= 20 {
		return 0, "", errors.New("at most 20 API tokens per user")
	}
	tok := APITokenPrefix + auth.RandomToken(30)
	var exp *time.Time
	if validDays > 0 {
		t := s.now().AddDate(0, 0, validDays)
		exp = &t
	}
	var id int64
	err = s.db.QueryRow(ctx, `INSERT INTO api_tokens(user_id, name, token_hash, expires_at, created_at) VALUES($1,$2,$3,$4,$5) RETURNING id`,
		userID, name, auth.HashToken("api", tok), exp, s.now()).Scan(&id)
	return id, tok, err
}

// APITokens lists a user's tokens.
func (s *Store) APITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.db.Query(ctx, `SELECT id, user_id, name, created_at, expires_at, last_used, last_ip FROM api_tokens WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.CreatedAt, &t.ExpiresAt, &t.LastUsed, &t.LastIP); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteAPIToken revokes a token of a user.
func (s *Store) DeleteAPIToken(ctx context.Context, userID, id int64) (string, error) {
	var name string
	err := s.db.QueryRow(ctx, `DELETE FROM api_tokens WHERE id=$1 AND user_id=$2 RETURNING name`, id, userID).Scan(&name)
	return name, notFound(err)
}

// UserByAPIToken finds the enabled user of a valid token and notes its use.
func (s *Store) UserByAPIToken(ctx context.Context, token, ip string) (*User, *APIToken, error) {
	if !strings.HasPrefix(token, APITokenPrefix) || len(token) > 200 {
		return nil, nil, ErrBadToken
	}
	var t APIToken
	err := s.db.QueryRow(ctx, `SELECT id, user_id, name, created_at, expires_at FROM api_tokens WHERE token_hash=$1`, auth.HashToken("api", token)).
		Scan(&t.ID, &t.UserID, &t.Name, &t.CreatedAt, &t.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrBadToken
	}
	if err != nil {
		return nil, nil, err
	}
	if t.ExpiresAt != nil && s.now().After(*t.ExpiresAt) {
		return nil, nil, ErrBadToken
	}
	u, err := s.UserByID(ctx, t.UserID)
	if err != nil || u.Disabled {
		return nil, nil, ErrBadToken
	}
	_, _ = s.db.Exec(ctx, `UPDATE api_tokens SET last_used=$2, last_ip=$3 WHERE id=$1`, t.ID, s.now(), truncate(ip, 64))
	return u, &t, nil
}

// ---- dashboards

// Widget kinds.
const (
	WidgetGraph    = "graph"    // a check's graph
	WidgetValue    = "value"    // the latest value of a check's metric, big
	WidgetStatus   = "status"   // devices of a group (or all) with their state
	WidgetProblems = "problems" // open problems
	WidgetMap      = "map"      // a link to a map with its worst state
)

// WidgetKinds lists the widget kinds.
var WidgetKinds = []string{WidgetGraph, WidgetValue, WidgetStatus, WidgetProblems, WidgetMap}

// Widget is one tile of a dashboard.
type Widget struct {
	Kind    string `json:"kind"`
	Title   string `json:"title,omitempty"`
	Size    string `json:"size,omitempty"` // s, m, l (columns of the grid)
	CheckID int64  `json:"check,omitempty"`
	Metric  string `json:"metric,omitempty"`
	GroupID int64  `json:"group,omitempty"`
	MapID   int64  `json:"map,omitempty"`
}

// Dashboard is a page of widgets.
type Dashboard struct {
	ID        int64
	Name      string
	Widgets   []Widget
	CreatedBy string
	CreatedAt time.Time
}

func (w *Widget) clean() error {
	if !slices.Contains(WidgetKinds, w.Kind) {
		return errors.New("unknown widget")
	}
	w.Title = strings.TrimSpace(w.Title)
	if len(w.Title) > 100 {
		return errors.New("the title is too long")
	}
	if w.Size != "s" && w.Size != "l" {
		w.Size = "m"
	}
	switch w.Kind {
	case WidgetGraph, WidgetValue:
		if w.CheckID <= 0 {
			return errors.New("choose a check")
		}
		w.GroupID, w.MapID = 0, 0
		if w.Kind == WidgetGraph {
			w.Metric = ""
		}
	case WidgetStatus:
		w.CheckID, w.Metric, w.MapID = 0, "", 0
	case WidgetProblems:
		w.CheckID, w.Metric, w.MapID = 0, "", 0
	case WidgetMap:
		if w.MapID <= 0 {
			return errors.New("choose a map")
		}
		w.CheckID, w.Metric, w.GroupID = 0, "", 0
	}
	return nil
}

func scanDashboard(row pgx.Row) (*Dashboard, error) {
	d := &Dashboard{}
	var raw []byte
	err := row.Scan(&d.ID, &d.Name, &raw, &d.CreatedBy, &d.CreatedAt)
	if err == nil {
		err = json.Unmarshal(raw, &d.Widgets)
	}
	return d, err
}

// Dashboards lists the dashboards.
func (s *Store) Dashboards(ctx context.Context) ([]*Dashboard, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, widgets, created_by, created_at FROM dashboards ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Dashboard
	for rows.Next() {
		d, err := scanDashboard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DashboardByID loads a dashboard.
func (s *Store) DashboardByID(ctx context.Context, id int64) (*Dashboard, error) {
	d, err := scanDashboard(s.db.QueryRow(ctx, `SELECT id, name, widgets, created_by, created_at FROM dashboards WHERE id=$1`, id))
	return d, notFound(err)
}

// CreateDashboard adds an empty dashboard.
func (s *Store) CreateDashboard(ctx context.Context, name, by string) (int64, error) {
	var err error
	if name, err = cleanName(name); err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(ctx, `INSERT INTO dashboards(name, created_by, created_at) VALUES($1,$2,$3) RETURNING id`, name, by, s.now()).Scan(&id)
	if isUnique(err) {
		return 0, ErrConflict
	}
	return id, err
}

// RenameDashboard changes a dashboard's name.
func (s *Store) RenameDashboard(ctx context.Context, id int64, name string) error {
	var err error
	if name, err = cleanName(name); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE dashboards SET name=$2 WHERE id=$1`, id, name)
	if isUnique(err) {
		return ErrConflict
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetWidgets stores the widgets of a dashboard.
func (s *Store) SetWidgets(ctx context.Context, id int64, ws []Widget) error {
	if len(ws) > 60 {
		return errors.New("at most 60 widgets on a dashboard")
	}
	for i := range ws {
		if err := ws[i].clean(); err != nil {
			return err
		}
	}
	if ws == nil {
		ws = []Widget{}
	}
	raw, _ := json.Marshal(ws)
	tag, err := s.db.Exec(ctx, `UPDATE dashboards SET widgets=$2 WHERE id=$1`, id, raw)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteDashboard removes a dashboard.
func (s *Store) DeleteDashboard(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM dashboards WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// CheckValues returns the latest value of each metric of a check.
func (s *Store) CheckValues(ctx context.Context, id int64) (map[string]float64, error) {
	var raw []byte
	if err := s.db.QueryRow(ctx, `SELECT last_values FROM checks WHERE id=$1`, id).Scan(&raw); err != nil {
		return nil, notFound(err)
	}
	out := map[string]float64{}
	_ = json.Unmarshal(raw, &out)
	return out, nil
}
