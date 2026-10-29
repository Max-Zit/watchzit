package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/datasource"
)

// ---- data sources

// DataSource is a database or service outside WatchZit that graphs read.
type DataSource struct {
	ID        int64
	Name      string
	Kind      string
	Address   string
	Database  string
	User      string
	TLS       string
	HasSecret bool
	UpdatedAt time.Time
}

// DataSourceInput is what the data source form sets; an empty Secret
// keeps the stored one unless ClearSecret is set.
type DataSourceInput struct {
	Name, Kind, Address, Database, User, Secret, TLS string
	ClearSecret                                      bool
}

const dsSecretPurpose = "datasources.secret"

func (in *DataSourceInput) clean() error {
	var err error
	if in.Name, err = cleanName(in.Name); err != nil {
		return err
	}
	if !slices.Contains(datasource.Kinds, in.Kind) {
		return errors.New("unknown kind of data source")
	}
	in.Address = strings.TrimSpace(in.Address)
	if in.Address == "" || len(in.Address) > 500 || strings.ContainsAny(in.Address, " \r\n\t\x00") {
		return errors.New("enter the address of the data source")
	}
	if !datasource.IsSQL(in.Kind) {
		if !strings.HasPrefix(in.Address, "http://") && !strings.HasPrefix(in.Address, "https://") {
			return errors.New("the address must start with http:// or https://")
		}
		in.TLS = oneOf(in.TLS, []string{"", "insecure"}, "")
	} else {
		in.TLS = oneOf(in.TLS, []string{"", "require", "verify", "disable"}, "")
	}
	in.Database, in.User = strings.TrimSpace(in.Database), strings.TrimSpace(in.User)
	if len(in.Database) > 200 || len(in.User) > 200 || len(in.Secret) > 4000 {
		return errors.New("a field is too long")
	}
	return nil
}

const dsCols = `id, name, kind, address, database, username, tls, secret <> '', updated_at`

func scanDataSource(row interface{ Scan(...any) error }) (*DataSource, error) {
	d := &DataSource{}
	err := row.Scan(&d.ID, &d.Name, &d.Kind, &d.Address, &d.Database, &d.User, &d.TLS, &d.HasSecret, &d.UpdatedAt)
	return d, notFound(err)
}

// DataSources lists the data sources by name.
func (s *Store) DataSources(ctx context.Context) ([]*DataSource, error) {
	rows, err := s.db.Query(ctx, `SELECT `+dsCols+` FROM datasources ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DataSource
	for rows.Next() {
		d, err := scanDataSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DataSourceByID returns one data source.
func (s *Store) DataSourceByID(ctx context.Context, id int64) (*DataSource, error) {
	return scanDataSource(s.db.QueryRow(ctx, `SELECT `+dsCols+` FROM datasources WHERE id=$1`, id))
}

// DataSourceConfig is how to reach a data source, with its secret opened.
func (s *Store) DataSourceConfig(ctx context.Context, id int64) (datasource.Config, error) {
	var c datasource.Config
	var sealed string
	err := s.db.QueryRow(ctx, `SELECT kind, address, database, username, secret, tls FROM datasources WHERE id=$1`, id).
		Scan(&c.Kind, &c.Address, &c.Database, &c.User, &sealed, &c.TLS)
	if err != nil {
		return c, notFound(err)
	}
	c.Secret, err = s.box.open(dsSecretPurpose, sealed)
	return c, err
}

// ConfigOf is how to reach a data source being entered: the form's values,
// with the stored secret when the form leaves it empty (id > 0).
func (s *Store) ConfigOf(ctx context.Context, id int64, in DataSourceInput) (datasource.Config, error) {
	if err := in.clean(); err != nil {
		return datasource.Config{}, err
	}
	c := datasource.Config{Kind: in.Kind, Address: in.Address, Database: in.Database, User: in.User, Secret: in.Secret, TLS: in.TLS}
	if id > 0 && in.Secret == "" && !in.ClearSecret {
		old, err := s.DataSourceConfig(ctx, id)
		if err != nil {
			return c, err
		}
		c.Secret = old.Secret
	}
	return c, nil
}

// CreateDataSource adds a data source.
func (s *Store) CreateDataSource(ctx context.Context, in DataSourceInput) (int64, error) {
	if err := in.clean(); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO datasources(name, kind, address, database, username, secret, tls, created_at, updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8) RETURNING id`,
		in.Name, in.Kind, in.Address, in.Database, in.User, s.box.seal(dsSecretPurpose, in.Secret), in.TLS, s.now()).Scan(&id)
	if isUnique(err) {
		return 0, ErrConflict
	}
	return id, err
}

// UpdateDataSource changes a data source.
func (s *Store) UpdateDataSource(ctx context.Context, id int64, in DataSourceInput) error {
	if err := in.clean(); err != nil {
		return err
	}
	q := `UPDATE datasources SET name=$2, kind=$3, address=$4, database=$5, username=$6, tls=$7, updated_at=$8`
	args := []any{id, in.Name, in.Kind, in.Address, in.Database, in.User, in.TLS, s.now()}
	if in.Secret != "" || in.ClearSecret {
		q += `, secret=$9`
		args = append(args, s.box.seal(dsSecretPurpose, in.Secret))
	}
	tag, err := s.db.Exec(ctx, q+` WHERE id=$1`, args...)
	if isUnique(err) {
		return ErrConflict
	}
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteDataSource removes a data source; graphs that read it show that.
func (s *Store) DeleteDataSource(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM datasources WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// dataSourceIDs is the set of existing data sources.
func (s *Store) dataSourceIDs(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM datasources`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// GraphQuery is a query of a data source shown on a graph.
type GraphQuery struct {
	Source int64  `json:"source"`
	Query  string `json:"query"`
	Label  string `json:"label,omitempty"` // put before the names of its series
	Unit   string `json:"unit,omitempty"`
}

// cleanQueries checks the queries of a graph.
func cleanQueries(qs []GraphQuery, known map[int64]bool) error {
	if len(qs) > 6 {
		return errors.New("a graph has at most 6 queries")
	}
	for i := range qs {
		q := &qs[i]
		q.Query = strings.TrimSpace(q.Query)
		q.Label = strings.TrimSpace(q.Label)
		q.Unit = strings.TrimSpace(q.Unit)
		if !known[q.Source] {
			return errors.New("choose a data source")
		}
		if len(q.Query) > 8000 {
			return errors.New("a query has at most 8000 characters")
		}
		if len([]rune(q.Label)) > 100 || len([]rune(q.Unit)) > 20 || strings.ContainsAny(q.Label+q.Unit, "\r\n\x00") {
			return errors.New("the name or unit of a query is too long")
		}
	}
	return nil
}
