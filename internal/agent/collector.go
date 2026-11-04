package agent

import (
	"context"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/poller"
)

// A remote collector keeps running its checks while the channel to the
// console is down (a site whose internet link fails); the results wait in
// memory and go to the console when the channel is back. They do not
// survive a restart of the agent: the checks come from the console.

// Buffer limits: the newest results are kept.
const (
	collectorBufMax = 200000         // results
	collectorBufAge = 24 * time.Hour // older ones are dropped
	collectorBatch  = 500            // results per message
)

type collectorRun struct {
	mu     sync.Mutex
	p      *poller.Poller
	stop   context.CancelFunc
	buf    []poller.RemoteResult
	notify chan struct{}
	now    func() time.Time
	sent   []sentBatch // waiting for the console's acknowledgement
	seq    int64
}

func newCollectorRun() *collectorRun {
	return &collectorRun{notify: make(chan struct{}, 1), now: time.Now}
}

// configure starts, updates or stops the poller (an empty list stops it
// and drops what was waiting). ctx is the agent's, not the channel's.
func (c *collectorRun) configure(ctx context.Context, a *Agent, cfg poller.RemoteConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(cfg.Checks) == 0 {
		if c.stop != nil {
			c.stop()
			c.p, c.stop, c.buf, c.sent = nil, nil, nil, nil
			a.Log.Info("no longer a remote collector")
		}
		return
	}
	if c.p == nil {
		c.p = poller.NewCollector(a.Log, c.add)
		pctx, cancel := context.WithCancel(ctx)
		c.stop = cancel
		go c.p.Run(pctx)
	}
	c.p.SetRemote(cfg)
	a.Log.Info("remote collector checks", "count", c.p.Remote())
}

// add queues results (the poller's callback).
func (c *collectorRun) add(rs []poller.RemoteResult) {
	c.mu.Lock()
	c.buf = append(c.buf, rs...)
	if over := len(c.buf) - collectorBufMax; over > 0 {
		c.buf = append([]poller.RemoteResult(nil), c.buf[over:]...)
	}
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// take removes up to n results that are not too old.
func (c *collectorRun) take(n int) []poller.RemoteResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	cut := c.now().Add(-collectorBufAge)
	i := 0
	for i < len(c.buf) && c.buf[i].Result.At.Before(cut) {
		i++
	}
	c.buf = c.buf[i:]
	if n > len(c.buf) {
		n = len(c.buf)
	}
	out := append([]poller.RemoteResult(nil), c.buf[:n]...)
	c.buf = c.buf[n:]
	return out
}

// putBack returns results that could not be sent, in front of the queue.
func (c *collectorRun) putBack(rs []poller.RemoteResult) {
	c.mu.Lock()
	c.buf = append(append([]poller.RemoteResult(nil), rs...), c.buf...)
	c.mu.Unlock()
}

// waiting is the number of queued results.
func (c *collectorRun) waiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.buf)
}

// close stops the poller (the agent stops).
func (c *collectorRun) close() {
	c.mu.Lock()
	if c.stop != nil {
		c.stop()
	}
	c.mu.Unlock()
}

// Batches sent but not yet acknowledged by the console: a channel can die
// with writes that never arrived, so they go back to the queue unless the
// console said they are stored.
const collectorInFlight = 20 // batches at most; then wait for acknowledgements

type sentBatch struct {
	seq int64
	rs  []poller.RemoteResult
}

// next takes the next batch to send and remembers it until ack.
func (c *collectorRun) next(n int, track bool) (int64, []poller.RemoteResult) {
	if track {
		c.mu.Lock()
		full := len(c.sent) >= collectorInFlight
		c.mu.Unlock()
		if full {
			return 0, nil
		}
	}
	rs := c.take(n)
	if len(rs) == 0 || !track {
		return 0, rs
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	c.sent = append(c.sent, sentBatch{seq: c.seq, rs: rs})
	return c.seq, rs
}

// ack forgets the batches up to seq.
func (c *collectorRun) ack(seq int64) {
	c.mu.Lock()
	i := 0
	for i < len(c.sent) && c.sent[i].seq <= seq {
		i++
	}
	c.sent = c.sent[i:]
	c.mu.Unlock()
	select { // room again: send more
	case c.notify <- struct{}{}:
	default:
	}
}

// requeue puts the unacknowledged batches back in front (the channel ended).
func (c *collectorRun) requeue() {
	c.mu.Lock()
	var rs []poller.RemoteResult
	for _, b := range c.sent {
		rs = append(rs, b.rs...)
	}
	c.sent = nil
	c.mu.Unlock()
	if len(rs) > 0 {
		c.putBack(rs)
	}
}
