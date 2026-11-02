package store

import (
	"context"
	"errors"
	"strings"
)

// CustomField is a field administrators defined for devices.
type CustomField struct {
	ID    int64
	Name  string
	Kind  string // text, number, date
	Value string // on pages: the value of one device
}

// CustomFields lists the fields.
func (s *Store) CustomFields(ctx context.Context) ([]CustomField, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, kind FROM custom_fields ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CustomField
	for rows.Next() {
		var f CustomField
		if err := rows.Scan(&f.ID, &f.Name, &f.Kind); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// AddCustomField defines a field (ErrConflict: the name exists).
func (s *Store) AddCustomField(ctx context.Context, name, kind string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 60 || strings.ContainsAny(name, "\x00\r\n\t") {
		return 0, errors.New("a field needs a name of up to 60 characters")
	}
	if kind != "text" && kind != "number" && kind != "date" {
		return 0, errors.New("unknown field type")
	}
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO custom_fields(name, kind, created_at) VALUES($1,$2,$3) RETURNING id`, name, kind, s.now()).Scan(&id)
	if isUnique(err) {
		return 0, ErrConflict
	}
	return id, err
}

// DeleteCustomField removes a field and its values.
func (s *Store) DeleteCustomField(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM custom_fields WHERE id=$1`, id)
	return err
}

// FieldValues returns the fields with the values of one device.
func (s *Store) FieldValues(ctx context.Context, deviceID int64) ([]CustomField, error) {
	rows, err := s.db.Query(ctx, `SELECT f.id, f.name, f.kind, COALESCE(v.value, '') FROM custom_fields f
		LEFT JOIN device_field_values v ON v.field_id=f.id AND v.device_id=$1 ORDER BY lower(f.name)`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CustomField
	for rows.Next() {
		var f CustomField
		if err := rows.Scan(&f.ID, &f.Name, &f.Kind, &f.Value); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SetFieldValues stores values of a device by field ID; an empty value
// removes it.
func (s *Store) SetFieldValues(ctx context.Context, deviceID int64, values map[int64]string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for fid, v := range values {
		if v == "" {
			_, err = tx.Exec(ctx, `DELETE FROM device_field_values WHERE field_id=$1 AND device_id=$2`, fid, deviceID)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO device_field_values(field_id, device_id, value) SELECT $1, $2, $3 FROM custom_fields WHERE id=$1
				ON CONFLICT (field_id, device_id) DO UPDATE SET value=EXCLUDED.value`, fid, deviceID, v)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
