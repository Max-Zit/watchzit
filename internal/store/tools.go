package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/max-zit/watchzit/internal/launch"
)

// Tool opens a device with a local program through the launcher.
type Tool struct {
	ID        int64
	Builtin   string // "" for a custom tool
	Name      string
	Program   string
	Args      []string
	Port      int
	Kinds     []string
	Enabled   bool
	CreatedAt time.Time
}

// For reports whether the tool is offered for a kind of device.
func (t *Tool) For(kind string) bool {
	return t.Enabled && (len(t.Kinds) == 0 || slices.Contains(t.Kinds, kind))
}

// ToolInput is what the tool form sets.
type ToolInput struct {
	Name    string
	Program string
	Args    []string
	Port    int
	Kinds   []string
	Enabled bool
}

func (in *ToolInput) clean(builtin bool) error {
	var err error
	if in.Name, err = cleanName(in.Name); err != nil {
		return err
	}
	if in.Port < 1 || in.Port > 65535 {
		return errors.New("the port must be 1–65535")
	}
	for _, k := range in.Kinds {
		if !slices.Contains(DeviceKinds, k) {
			return errors.New("unknown device type")
		}
	}
	slices.Sort(in.Kinds)
	in.Kinds = slices.Compact(in.Kinds)
	if in.Kinds == nil {
		in.Kinds = []string{}
	}
	if builtin {
		in.Program, in.Args = "", []string{}
		return nil
	}
	if err := launch.CheckProgram(in.Program); err != nil {
		return err
	}
	if in.Args == nil {
		in.Args = []string{}
	}
	return launch.CheckArgs(in.Args)
}

const toolCols = `id, COALESCE(builtin,''), name, program, args, port, kinds, enabled, created_at`

func scanTool(row pgx.Row) (*Tool, error) {
	t := &Tool{}
	err := row.Scan(&t.ID, &t.Builtin, &t.Name, &t.Program, &t.Args, &t.Port, &t.Kinds, &t.Enabled, &t.CreatedAt)
	return t, err
}

// Tools lists the tools: built-in ones first.
func (s *Store) Tools(ctx context.Context) ([]*Tool, error) {
	rows, err := s.db.Query(ctx, `SELECT `+toolCols+` FROM tools ORDER BY builtin IS NULL, array_position(ARRAY['ssh','rdp','winbox','vnc','telnet'], builtin), lower(name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Tool
	for rows.Next() {
		t, err := scanTool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ToolByID loads a tool.
func (s *Store) ToolByID(ctx context.Context, id int64) (*Tool, error) {
	t, err := scanTool(s.db.QueryRow(ctx, `SELECT `+toolCols+` FROM tools WHERE id=$1`, id))
	return t, notFound(err)
}

// CreateTool adds a custom tool.
func (s *Store) CreateTool(ctx context.Context, in ToolInput) (int64, error) {
	if err := in.clean(false); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO tools(name, program, args, port, kinds, enabled) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
		in.Name, in.Program, in.Args, in.Port, in.Kinds, in.Enabled).Scan(&id)
	if isUnique(err) {
		return 0, ErrConflict
	}
	return id, err
}

// UpdateTool changes a tool; a built-in tool keeps its program.
func (s *Store) UpdateTool(ctx context.Context, id int64, in ToolInput) error {
	t, err := s.ToolByID(ctx, id)
	if err != nil {
		return err
	}
	if err := in.clean(t.Builtin != ""); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE tools SET name=$2, program=$3, args=$4, port=$5, kinds=$6, enabled=$7 WHERE id=$1`,
		id, in.Name, in.Program, in.Args, in.Port, in.Kinds, in.Enabled)
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

// DeleteTool removes a custom tool (built-in ones can only be switched off).
func (s *Store) DeleteTool(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM tools WHERE id=$1 AND builtin IS NULL`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return errors.New("built-in tools can only be switched off")
	}
	return err
}

// DeviceAccess is how tools sign in to a device.
type DeviceAccess struct {
	User  string
	Ports map[int64]int // tool → port, where it differs from the default
}

// DeviceAccess loads the access settings of a device.
func (s *Store) DeviceAccess(ctx context.Context, id int64) (DeviceAccess, error) {
	a := DeviceAccess{Ports: map[int64]int{}}
	err := s.db.QueryRow(ctx, `SELECT access_user, access_ports FROM devices WHERE id=$1`, id).Scan(&a.User, &a.Ports)
	return a, notFound(err)
}

// SetDeviceAccess stores the access settings of a device.
func (s *Store) SetDeviceAccess(ctx context.Context, id int64, a DeviceAccess) error {
	if !launch.ValidUser(a.User) {
		return errors.New("the user name may contain letters, digits and . _ @ \\ - only")
	}
	for tool, p := range a.Ports {
		if p < 1 || p > 65535 {
			delete(a.Ports, tool)
		}
	}
	if a.Ports == nil {
		a.Ports = map[int64]int{}
	}
	tag, err := s.db.Exec(ctx, `UPDATE devices SET access_user=$2, access_ports=$3 WHERE id=$1`, id, a.User, a.Ports)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// PortFor is the port a tool uses on the device.
func (a DeviceAccess) PortFor(t *Tool) int {
	if p, ok := a.Ports[t.ID]; ok && p > 0 {
		return p
	}
	return t.Port
}
