package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Template is a set of checks for a kind of device.
type Template struct {
	ID        int64
	Name      string
	Kinds     []string // device kinds it is meant for
	Auto      bool     // applied by itself to new devices of these kinds
	Checks    []TemplateCheck
	NChecks   int // in lists
	NDevices  int // devices it was applied to
	CreatedAt time.Time
}

// TemplateCheck is a check a template adds.
type TemplateCheck struct {
	ID        int64
	Type      string
	Name      string
	Params    CheckParams
	IntervalS int
}

// TemplateCheckTypes are the checks a template can hold: those that need
// nothing but the device's address (no passwords, no SNMP rows or
// interfaces, which discovery and the agent add by themselves).
var TemplateCheckTypes = []string{CheckICMP, CheckTCP, CheckHTTP, CheckTLS, CheckDNS, CheckSNMP, CheckSNMPCPU, CheckSNMPUPS}

func cleanTemplate(name string, kinds []string) (string, []string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 || strings.ContainsAny(name, "\x00\r\n\t") {
		return "", nil, errors.New("a template needs a name of up to 100 characters")
	}
	out := []string{}
	for _, k := range kinds {
		if slices.Contains(DeviceKinds, k) && !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return name, out, nil
}

// Templates lists the templates with how many checks and devices they have.
func (s *Store) Templates(ctx context.Context) ([]*Template, error) {
	rows, err := s.db.Query(ctx, `SELECT t.id, t.name, t.kinds, t.auto, t.created_at,
		(SELECT count(*) FROM template_checks c WHERE c.template_id=t.id), (SELECT count(*) FROM device_templates d WHERE d.template_id=t.id)
		FROM templates t ORDER BY lower(t.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Template
	for rows.Next() {
		t := &Template{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Kinds, &t.Auto, &t.CreatedAt, &t.NChecks, &t.NDevices); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TemplateByID loads a template with its checks.
func (s *Store) TemplateByID(ctx context.Context, id int64) (*Template, error) {
	t := &Template{}
	err := s.db.QueryRow(ctx, `SELECT id, name, kinds, auto, created_at, (SELECT count(*) FROM device_templates d WHERE d.template_id=t.id) FROM templates t WHERE id=$1`, id).
		Scan(&t.ID, &t.Name, &t.Kinds, &t.Auto, &t.CreatedAt, &t.NDevices)
	if err != nil {
		return nil, notFound(err)
	}
	rows, err := s.db.Query(ctx, `SELECT id, type, name, params, interval_s FROM template_checks WHERE template_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c TemplateCheck
		var raw []byte
		if err := rows.Scan(&c.ID, &c.Type, &c.Name, &raw, &c.IntervalS); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &c.Params)
		t.Checks = append(t.Checks, c)
	}
	t.NChecks = len(t.Checks)
	return t, rows.Err()
}

// SaveTemplate creates (id 0) or changes a template; ErrConflict when the
// name is taken.
func (s *Store) SaveTemplate(ctx context.Context, id int64, name string, kinds []string, auto bool) (int64, error) {
	name, kinds, err := cleanTemplate(name, kinds)
	if err != nil {
		return 0, err
	}
	if id == 0 {
		err = s.db.QueryRow(ctx, `INSERT INTO templates(name, kinds, auto, created_at) VALUES($1,$2,$3,$4) RETURNING id`, name, kinds, auto, s.now()).Scan(&id)
	} else {
		tag, e := s.db.Exec(ctx, `UPDATE templates SET name=$2, kinds=$3, auto=$4 WHERE id=$1`, id, name, kinds, auto)
		err = e
		if err == nil && tag.RowsAffected() == 0 {
			return 0, ErrNotFound
		}
	}
	if isUnique(err) {
		return 0, ErrConflict
	}
	return id, err
}

// DeleteTemplate removes a template; the checks it added stay on the devices.
func (s *Store) DeleteTemplate(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM templates WHERE id=$1`, id)
	return err
}

// AddTemplateCheck adds a check to a template (checked like a device check).
func (s *Store) AddTemplateCheck(ctx context.Context, templateID int64, in CheckInput) (int64, error) {
	if !slices.Contains(TemplateCheckTypes, in.Type) {
		return 0, errors.New("a template can hold ping, TCP, web page, certificate, DNS and SNMP checks")
	}
	if err := in.clean(); err != nil {
		return 0, err
	}
	in.Params.Secret = ""
	raw, _ := json.Marshal(in.Params) // #nosec G117 -- no secrets in template checks
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO template_checks(template_id, type, name, params, interval_s) VALUES($1,$2,$3,$4,$5) RETURNING id`,
		templateID, in.Type, in.Name, raw, in.IntervalS).Scan(&id)
	if isFK(err) {
		return 0, ErrNotFound
	}
	return id, err
}

// DeleteTemplateCheck removes a check from a template (not from devices).
func (s *Store) DeleteTemplateCheck(ctx context.Context, templateID, checkID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM template_checks WHERE id=$1 AND template_id=$2`, checkID, templateID)
	return err
}

// checkKey tells checks of a device apart: the same type with the same
// port, address, name or OID is the same check.
func checkKey(typ string, p CheckParams) string {
	return typ + "|" + strconv.Itoa(p.Port) + "|" + p.URL + "|" + p.Query + "|" + p.OID
}

// ApplyTemplate adds the template's checks a device does not have yet and
// remembers the template on the device; it returns how many were added.
func (s *Store) ApplyTemplate(ctx context.Context, templateID, deviceID int64) (int, error) {
	t, err := s.TemplateByID(ctx, templateID)
	if err != nil {
		return 0, err
	}
	cs, err := s.DeviceChecks(ctx, deviceID)
	if err != nil {
		return 0, err
	}
	have := map[string]bool{}
	for _, c := range cs {
		have[checkKey(c.Type, c.Params)] = true
	}
	added := 0
	for _, tc := range t.Checks {
		k := checkKey(tc.Type, tc.Params)
		if have[k] {
			continue
		}
		if _, err := s.CreateCheck(ctx, deviceID, CheckInput{Type: tc.Type, Name: tc.Name, Params: tc.Params, IntervalS: tc.IntervalS}); err != nil {
			return added, err
		}
		have[k] = true
		added++
	}
	_, err = s.db.Exec(ctx, `INSERT INTO device_templates(device_id, template_id, applied_at) VALUES($1,$2,$3)
		ON CONFLICT (device_id, template_id) DO UPDATE SET applied_at=EXCLUDED.applied_at`, deviceID, templateID, s.now())
	return added, err
}

// DeviceTemplates lists the templates applied to a device.
func (s *Store) DeviceTemplates(ctx context.Context, deviceID int64) ([]*Template, error) {
	rows, err := s.db.Query(ctx, `SELECT t.id, t.name, t.kinds, t.auto, t.created_at FROM device_templates d JOIN templates t ON t.id=d.template_id
		WHERE d.device_id=$1 ORDER BY lower(t.name)`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Template
	for rows.Next() {
		t := &Template{}
		if err := rows.Scan(&t.ID, &t.Name, &t.Kinds, &t.Auto, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TemplateDevices lists the devices a template was applied to.
func (s *Store) TemplateDevices(ctx context.Context, templateID int64) ([]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT device_id FROM device_templates WHERE template_id=$1 ORDER BY device_id`, templateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ApplyAutoTemplates applies the templates meant for new devices of a kind;
// it returns how many checks were added.
func (s *Store) ApplyAutoTemplates(ctx context.Context, deviceID int64, kind string) (int, error) {
	ts, err := s.Templates(ctx)
	if err != nil {
		return 0, err
	}
	added := 0
	for _, t := range ts {
		if !t.Auto || !slices.Contains(t.Kinds, kind) {
			continue
		}
		n, err := s.ApplyTemplate(ctx, t.ID, deviceID)
		added += n
		if err != nil {
			return added, err
		}
	}
	return added, nil
}
