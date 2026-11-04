package agent

import (
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/poller"
	"github.com/max-zit/watchzit/internal/store"
)

// TestCollectorBuffer: results wait while the channel is down, in order,
// a failed send puts them back in front, and results older than a day or
// beyond the limit are dropped (the newest are kept).
func TestCollectorBuffer(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	c := newCollectorRun()
	c.now = func() time.Time { return now }
	res := func(id int64, at time.Time) poller.RemoteResult {
		return poller.RemoteResult{Result: store.CheckResult{CheckID: id, At: at, Status: store.StatusUp}}
	}
	c.add([]poller.RemoteResult{res(1, now.Add(-25*time.Hour)), res(2, now.Add(-2*time.Hour)), res(3, now.Add(-time.Hour))})
	select {
	case <-c.notify:
	default:
		t.Fatal("no notice of new results")
	}
	got := c.take(1)
	if len(got) != 1 || got[0].Result.CheckID != 2 {
		t.Fatalf("first: %+v (the day-old one must be dropped)", got)
	}
	c.putBack(got)
	if got := c.take(10); len(got) != 2 || got[0].Result.CheckID != 2 || got[1].Result.CheckID != 3 {
		t.Fatalf("after put back: %+v", got)
	}
	// the limit keeps the newest
	big := make([]poller.RemoteResult, collectorBufMax+5)
	for i := range big {
		big[i] = res(int64(i), now)
	}
	c.add(big)
	if n := c.waiting(); n != collectorBufMax {
		t.Fatalf("waiting %d", n)
	}
	if got := c.take(1); got[0].Result.CheckID != 5 {
		t.Fatalf("oldest kept %d", got[0].Result.CheckID)
	}
}

// TestCollectorAcks: batches stay until the console acknowledges them; a
// channel that ends puts the rest back in front, in order.
func TestCollectorAcks(t *testing.T) {
	c := newCollectorRun()
	now := time.Now()
	for i := int64(1); i <= 6; i++ {
		c.add([]poller.RemoteResult{{Result: store.CheckResult{CheckID: i, At: now}}})
	}
	s1, b1 := c.next(2, true)
	s2, b2 := c.next(2, true)
	if s1 != 1 || s2 != 2 || b1[0].Result.CheckID != 1 || b2[0].Result.CheckID != 3 {
		t.Fatalf("batches %d %d", s1, s2)
	}
	c.ack(1)
	c.requeue() // the channel died with batch 2 unacknowledged
	got := c.take(10)
	if len(got) != 4 || got[0].Result.CheckID != 3 || got[3].Result.CheckID != 6 {
		t.Fatalf("after requeue: %+v", got)
	}
	// without acknowledgements (an older console) nothing is kept
	c.add(got)
	if _, b := c.next(10, false); len(b) != 4 || len(c.sent) != 0 {
		t.Fatal("untracked send kept batches")
	}
}
