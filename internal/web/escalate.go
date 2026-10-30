package web

import (
	"context"
	"time"

	"github.com/max-zit/watchzit/internal/alert"
	"github.com/max-zit/watchzit/internal/store"
)

// RunEscalation tells waiting and on-call channels about open problems once
// a minute (see Escalate).
func (s *Server) RunEscalation(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Escalate(ctx); err != nil {
				s.log.Error("escalation", "err", err)
			}
		}
	}
}

// Escalate sends open problems to the channels that wait, repeat or keep
// hours of duty: a problem goes to such a channel when it has been open for
// the channel's delay (and is not acknowledged, unless the channel wants
// those too) while the channel is on duty, and again every repeat interval.
func (s *Server) Escalate(ctx context.Context) error {
	chans, err := s.store.Channels(ctx)
	if err != nil {
		return err
	}
	var esc []*store.Channel
	for _, c := range chans {
		if c.Enabled && c.Escalates() {
			esc = append(esc, c)
		}
	}
	if len(esc) == 0 {
		return nil
	}
	open, err := s.store.Problems(ctx, store.ProblemFilter{Open: true})
	if err != nil {
		return err
	}
	sent, err := s.store.Sent(ctx, s.now())
	if err != nil {
		return err
	}
	now := s.now()
	n := s.notifySettings(ctx)
	for _, c := range esc {
		if !c.OnDuty(now) {
			continue
		}
		var list []notifyEvent
		for _, p := range open {
			if store.SeverityRank(p.Severity) < store.SeverityRank(c.MinSeverity) || (c.SkipAcked && p.AckAt != nil) {
				continue
			}
			if now.Sub(p.StartedAt) < time.Duration(c.DelayMin)*time.Minute {
				continue
			}
			last, told := sent[c.ID][p.ID]
			switch {
			case !told && now.Sub(p.StartedAt) > 7*24*time.Hour:
				continue // older problems are not news any more
			case told && (c.RepeatMin == 0 || now.Sub(last) < time.Duration(c.RepeatMin)*time.Minute):
				continue
			}
			list = append(list, toNotifyEvent(alert.Event{Kind: alert.EventProblem, Problem: *p}, n.ConsoleURL))
		}
		s.send(c, n, list)
	}
	return nil
}
