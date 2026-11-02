package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

// TestEventReader: each message is sent once, a failed read is tried again
// from the same point, and one report carries at most maxEvents.
func TestEventReader(t *testing.T) {
	start := time.Now().Add(-time.Minute)
	var evs []proto.Event
	var fail bool
	r := &EventReader{since: start, read: func(context.Context, time.Time) ([]proto.Event, error) {
		if fail {
			return nil, errors.New("journal busy")
		}
		return evs, nil
	}}
	evs = []proto.Event{{Time: start.Add(-time.Second), Message: "old"}, {Time: start.Add(10 * time.Second), Message: "disk error"}}
	if got := r.Read(context.Background()); len(got) != 1 || got[0].Message != "disk error" {
		t.Fatalf("%+v", got)
	}
	fail = true
	before := r.since
	if got := r.Read(context.Background()); got != nil {
		t.Fatal("events from a failed read")
	}
	if !r.since.Equal(before) {
		t.Fatal("a failed read moved on")
	}
	fail, evs = false, nil
	for i := 0; i < 150; i++ {
		evs = append(evs, proto.Event{Time: time.Now().Add(time.Duration(i+1) * time.Millisecond), Message: "x"})
	}
	if got := r.Read(context.Background()); len(got) != maxEvents {
		t.Fatalf("%d events in one report", len(got))
	}
	if got := r.Read(context.Background()); len(got) != 0 {
		t.Fatalf("sent again: %d", len(got))
	}
}
