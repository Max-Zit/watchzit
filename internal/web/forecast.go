package web

import (
	"context"
	"errors"
	"time"

	"github.com/max-zit/watchzit/internal/alert"
	"github.com/max-zit/watchzit/internal/store"
)

// MetricHistory reads a check's metric for forecast triggers (hourly or
// finer points; nothing while the series does not exist yet).
func (s *Server) MetricHistory(ctx context.Context, checkID int64, metric string, from, to time.Time) ([]alert.Point, error) {
	id, err := s.store.SeriesID(ctx, checkID, metric)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	res, err := s.tsdb.Query(ctx, id, from, to, 200)
	if err != nil {
		return nil, err
	}
	out := make([]alert.Point, 0, len(res.Points))
	for _, p := range res.Points {
		if p.N > 0 {
			out = append(out, alert.Point{T: time.Unix(p.T, 0), V: p.Avg})
		}
	}
	return out, nil
}
