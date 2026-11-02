package agent

import (
	"context"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

// Error messages of the system: Windows event logs (System, Application:
// critical and error) and the systemd journal (priority err and worse),
// sent with the next report. The first read looks five minutes back.

// maxEvents limits the messages of one report.
const maxEvents = 100

// EventReader remembers how far it read.
type EventReader struct {
	mu    sync.Mutex
	since time.Time
	read  func(ctx context.Context, since time.Time) ([]proto.Event, error) // tests replace it
}

// NewEventReader reads this computer's logs.
func NewEventReader() *EventReader {
	return &EventReader{since: time.Now().Add(-5 * time.Minute), read: readEvents}
}

// Read returns the messages since the last read (oldest first).
func (r *EventReader) Read(ctx context.Context) []proto.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	evs, err := r.read(ctx, r.since)
	if err != nil {
		return nil // tried again next time from the same point
	}
	var out []proto.Event
	for _, e := range evs {
		if e.Time.After(r.since) {
			out = append(out, e)
		}
	}
	if len(out) > maxEvents {
		out = out[len(out)-maxEvents:]
	}
	r.since = now
	for _, e := range out {
		if e.Time.After(r.since) {
			r.since = e.Time
		}
	}
	return out
}
