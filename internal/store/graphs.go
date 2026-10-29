package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// GraphLayout is how the graphs of a device are arranged. Series that no
// panel shows and that are not hidden get the automatic graphs.
type GraphLayout struct {
	Panels []GraphPanel `json:"panels"`
	Hidden []int32      `json:"hidden,omitempty"` // series not to graph
}

// GraphPanel is one graph: its series and how it draws them.
type GraphPanel struct {
	ID     string   `json:"id"`
	Title  string   `json:"title,omitempty"`
	Series []int32  `json:"series"`
	Type   string   `json:"type,omitempty"`   // line (default), area, bar
	Stack  bool     `json:"stack,omitempty"`  // series added up
	Width  string   `json:"width,omitempty"`  // "" (one column) or full
	Height string   `json:"height,omitempty"` // "" (normal), s or l
	Min    *float64 `json:"min,omitempty"`    // fixed axis ends
	Max    *float64 `json:"max,omitempty"`
	Warn   *float64 `json:"warn,omitempty"` // threshold lines
	Crit   *float64 `json:"crit,omitempty"`
	// set by dragging the corner: columns spanned (1–4; 0 = by Width) and
	// the drawing height in pixels (0 = by Height)
	Cols int `json:"cols,omitempty"`
	H    int `json:"h,omitempty"`
	// Colors gives series their own colour (series ID → #rrggbb).
	Colors map[string]string `json:"colors,omitempty"`
}

// colorRE is a colour as #rrggbb.
var colorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// GraphTypes are the kinds of graphs.
var GraphTypes = []string{"line", "area", "bar"}

// DeviceGraphLayout returns the arranged graphs of a device (nil: none).
func (s *Store) DeviceGraphLayout(ctx context.Context, deviceID int64) (*GraphLayout, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT layout FROM device_graphs WHERE device_id=$1`, deviceID).Scan(&raw)
	if errors.Is(notFound(err), ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	l := &GraphLayout{}
	if err := json.Unmarshal(raw, l); err != nil {
		return nil, nil // unreadable: the automatic graphs
	}
	return l, nil
}

// SetDeviceGraphLayout stores the arranged graphs of a device; series of
// other devices are refused.
func (s *Store) SetDeviceGraphLayout(ctx context.Context, deviceID int64, l *GraphLayout) error {
	own := map[int32]bool{}
	rows, err := s.db.Query(ctx, `SELECT s.id FROM series s JOIN checks c ON c.id = s.check_id WHERE c.device_id=$1`, deviceID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		own[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(l.Panels) > 60 {
		return errors.New("a device can have at most 60 graphs")
	}
	ids := map[string]bool{}
	for i := range l.Panels {
		p := &l.Panels[i]
		if p.ID == "" || len(p.ID) > 40 || ids[p.ID] || strings.Trim(p.ID, "abcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
			return errors.New("invalid graph")
		}
		ids[p.ID] = true
		p.Title = strings.TrimSpace(p.Title)
		if len([]rune(p.Title)) > 100 {
			return errors.New("a graph title has at most 100 characters")
		}
		if len(p.Series) == 0 || len(p.Series) > 12 {
			return errors.New("a graph shows 1–12 values")
		}
		for _, sid := range p.Series {
			if !own[sid] {
				return errors.New("a graph shows values of this device only")
			}
		}
		if p.Type == "line" {
			p.Type = ""
		}
		if p.Type != "" && !slices.Contains(GraphTypes, p.Type) {
			return errors.New("unknown kind of graph")
		}
		if p.Width != "" && p.Width != "full" {
			p.Width = ""
		}
		if p.Height != "" && p.Height != "s" && p.Height != "l" {
			p.Height = ""
		}
		for _, v := range []*float64{p.Min, p.Max, p.Warn, p.Crit} {
			if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0)) {
				return errors.New("invalid number")
			}
		}
		if p.Min != nil && p.Max != nil && *p.Min >= *p.Max {
			return errors.New("the axis minimum must be below its maximum")
		}
		if p.Cols < 0 || p.Cols > 4 {
			p.Cols = 0
		}
		if p.H != 0 && (p.H < 80 || p.H > 900) {
			return errors.New("a graph is 80–900 pixels high")
		}
		for k, c := range p.Colors {
			id, err := strconv.ParseInt(k, 10, 32)
			if err != nil || !slices.Contains(p.Series, int32(id)) || !colorRE.MatchString(c) {
				delete(p.Colors, k)
			}
		}
	}
	hidden := []int32{}
	for _, sid := range l.Hidden {
		if own[sid] && !slices.Contains(hidden, sid) {
			hidden = append(hidden, sid)
		}
	}
	l.Hidden = hidden
	raw, _ := json.Marshal(l)
	_, err = s.db.Exec(ctx, `INSERT INTO device_graphs(device_id, layout, updated_at) VALUES($1,$2,$3)
		ON CONFLICT (device_id) DO UPDATE SET layout=EXCLUDED.layout, updated_at=EXCLUDED.updated_at`, deviceID, raw, s.now())
	return err
}

// ResetDeviceGraphs goes back to the automatic graphs of a device.
func (s *Store) ResetDeviceGraphs(ctx context.Context, deviceID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM device_graphs WHERE device_id=$1`, deviceID)
	return err
}
