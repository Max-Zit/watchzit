package store

import (
	"context"
	"errors"
	"math"
	"strings"
)

// Pictures of the library: uploaded device pictures (assets of kind
// "icon") with what they show, set in the picture editor.

// cleanMeta checks the description of a picture w×h (0: size unknown).
func cleanMeta(m PictureMeta, w, h int) (PictureMeta, error) {
	m.Vendor = strings.TrimSpace(m.Vendor)
	if len([]rune(m.Vendor)) > 60 {
		return m, errors.New("the manufacturer has at most 60 characters")
	}
	m.Type = oneOf(m.Type, PictureTypes, "")
	if m.U < 0 || m.U > 12 {
		return m, errors.New("a picture is 0–12 rack units high")
	}
	if m.Show != 0 && (m.Show < 10 || m.Show > 1200 || math.IsNaN(m.Show)) {
		return m, errors.New("the width on the map is 10–1200")
	}
	if len(m.Ports) > 512 {
		return m, errors.New("a picture has at most 512 ports")
	}
	for i, p := range m.Ports {
		x, y := p[0], p[1]
		if math.IsNaN(x) || math.IsNaN(y) || x < 0 || y < 0 || (w > 0 && x > float64(w)) || (h > 0 && y > float64(h)) {
			return m, errors.New("a port lies outside the picture")
		}
		m.Ports[i] = [2]float64{math.Round(x*10) / 10, math.Round(y*10) / 10}
	}
	if m.Fiber < 0 || m.Fiber > len(m.Ports) {
		return m, errors.New("more SFP ports than ports")
	}
	return m, nil
}

// CreatePicture stores a device picture with its description (a copy of a
// built-in picture, for example).
func (s *Store) CreatePicture(ctx context.Context, name, mime string, data []byte, w, h int, m PictureMeta) (int64, error) {
	m, err := cleanMeta(m, w, h)
	if err != nil {
		return 0, err
	}
	id, err := s.CreateAsset(ctx, "icon", name, mime, data, w, h)
	if err != nil {
		return 0, err
	}
	_, err = s.db.Exec(ctx, `UPDATE map_assets SET meta=$2 WHERE id=$1`, id, m)
	return id, err
}

// UpdatePicture renames a device picture and sets its description.
func (s *Store) UpdatePicture(ctx context.Context, id int64, name string, m PictureMeta) error {
	a, _, err := s.AssetData(ctx, id)
	if err != nil {
		return err
	}
	if a.Kind != "icon" {
		return ErrNotFound
	}
	if name, err = cleanName(name); err != nil {
		return err
	}
	if m, err = cleanMeta(m, a.Width, a.Height); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE map_assets SET name=$2, meta=$3 WHERE id=$1`, id, name, m)
	return err
}

// ClonePicture copies a device picture with its description.
func (s *Store) ClonePicture(ctx context.Context, id int64, name string) (int64, error) {
	a, data, err := s.AssetData(ctx, id)
	if err != nil {
		return 0, err
	}
	if a.Kind != "icon" {
		return 0, ErrNotFound
	}
	return s.CreatePicture(ctx, name, a.Mime, data, a.Width, a.Height, a.Meta)
}
