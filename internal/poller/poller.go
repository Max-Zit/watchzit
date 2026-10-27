// Package poller runs the checks of all devices on their intervals, stores
// their results and feeds the metric store.
package poller

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/probe"
	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/tsdb"
)

// Poller schedules and runs checks.
type Poller struct {
	store *store.Store
	db    *tsdb.DB
	log   *slog.Logger
	now   func() time.Time

	// Workers is the number of checks run at the same time.
	Workers int
	// OnChange is told about checks that went up or down.
	OnChange func([]store.StatusChange)

	mu      sync.Mutex
	entries map[int64]*entry
	reload  chan struct{}
}

type entry struct {
	c       *store.Check
	next    time.Time
	running bool
}

// New creates a poller.
func New(st *store.Store, db *tsdb.DB, log *slog.Logger) *Poller {
	return &Poller{store: st, db: db, log: log, now: time.Now, Workers: 64, entries: map[int64]*entry{}, reload: make(chan struct{}, 1)}
}

// Reload makes the poller read the checks again (after a change).
func (p *Poller) Reload() {
	select {
	case p.reload <- struct{}{}:
	default:
	}
}

// RunNow runs a check at the next tick.
func (p *Poller) RunNow(id int64) {
	p.mu.Lock()
	if e := p.entries[id]; e != nil {
		e.next = time.Time{}
	}
	p.mu.Unlock()
}

func (p *Poller) load(ctx context.Context) error {
	cs, err := p.store.AllChecks(ctx)
	if err != nil {
		return err
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := map[int64]bool{}
	for _, c := range cs {
		seen[c.ID] = true
		if e := p.entries[c.ID]; e != nil {
			if e.c.IntervalS != c.IntervalS {
				e.next = now
			}
			e.c = c
			continue
		}
		// Spread new checks over their interval so 500 devices do not all
		// start in the same second; a fresh check runs within 3 seconds.
		delay := time.Duration(rand.Int64N(int64(min(c.IntervalS, 3)) * int64(time.Second))) // #nosec G404 -- jitter
		if time.Since(c.CreatedAt) > time.Minute {
			delay = time.Duration(rand.Int64N(int64(c.IntervalS) * int64(time.Second))) // #nosec G404 -- jitter
		}
		p.entries[c.ID] = &entry{c: c, next: now.Add(delay)}
	}
	for id := range p.entries {
		if !seen[id] {
			delete(p.entries, id)
		}
	}
	return nil
}

// Run polls until ctx ends.
func (p *Poller) Run(ctx context.Context) {
	if err := p.load(ctx); err != nil {
		p.log.Error("poller: cannot load checks", "err", err)
	}
	jobs := make(chan *store.Check, p.Workers)
	results := make(chan outcome, p.Workers*4)
	var wg sync.WaitGroup
	for i := 0; i < p.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				results <- p.runCheck(ctx, c)
			}
		}()
	}
	saved := make(chan struct{})
	go func() {
		p.saveLoop(ctx, results)
		close(saved)
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	reload := time.NewTicker(time.Minute)
	defer reload.Stop()
	for {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			close(results)
			<-saved
			return
		case <-p.reload:
			if err := p.load(ctx); err != nil {
				p.log.Error("poller: cannot load checks", "err", err)
			}
		case <-reload.C:
			if err := p.load(ctx); err != nil {
				p.log.Error("poller: cannot load checks", "err", err)
			}
		case <-tick.C:
			p.dispatch(ctx, jobs)
		}
	}
}

func (p *Poller) dispatch(ctx context.Context, jobs chan<- *store.Check) {
	now := p.now()
	p.mu.Lock()
	var due []*store.Check
	for _, e := range p.entries {
		if e.running || e.c.Paused || e.c.DevPaused || now.Before(e.next) {
			continue
		}
		e.running = true
		e.next = now.Add(time.Duration(e.c.IntervalS) * time.Second)
		due = append(due, e.c)
	}
	p.mu.Unlock()
	for _, c := range due {
		select {
		case jobs <- c:
		case <-ctx.Done():
			return
		}
	}
}

type outcome struct {
	res     store.CheckResult
	samples []tsdb.Sample
}

func (p *Poller) done(id int64) {
	p.mu.Lock()
	if e := p.entries[id]; e != nil {
		e.running = false
	}
	p.mu.Unlock()
}

// saveLoop writes results in batches.
func (p *Poller) saveLoop(ctx context.Context, results <-chan outcome) {
	var batch []store.CheckResult
	flush := func() {
		if len(batch) == 0 {
			return
		}
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		changes, err := p.store.SaveResults(c, batch)
		cancel()
		if err != nil {
			p.log.Error("poller: cannot save results", "err", err)
		}
		if len(changes) > 0 && p.OnChange != nil {
			p.OnChange(changes)
		}
		batch = batch[:0]
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case o, ok := <-results:
			if !ok {
				flush()
				return
			}
			batch = append(batch, o.res)
			p.db.Add(o.samples...)
			p.done(o.res.CheckID)
			if len(batch) >= 500 {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

// round2 keeps two decimals (0.01 ms is finer than any network measures,
// and decimal values are stored much smaller).
func round2(v float64) float64 { return math.Round(v*100) / 100 }

func ms(d time.Duration) float64 { return round2(float64(d) / float64(time.Millisecond)) }

func seriesID(c *store.Check, metric string) int32 {
	for _, s := range c.Series {
		if s.Metric == metric {
			return s.ID
		}
	}
	return 0
}

// runCheck runs one check (never longer than its interval).
func (p *Poller) runCheck(ctx context.Context, c *store.Check) outcome {
	limit := time.Duration(c.IntervalS) * time.Second
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	now := p.now()
	o := outcome{res: store.CheckResult{CheckID: c.ID, At: now, Status: store.StatusDown}}
	sample := func(metric string, v float64) {
		if id := seriesID(c, metric); id != 0 {
			o.samples = append(o.samples, tsdb.Sample{Series: id, T: now, V: v})
		}
	}
	addr, err := probe.Resolve(ctx, c.Address)
	if err != nil {
		o.res.Text = err.Error()
		if c.Type == store.CheckICMP {
			sample("loss_pct", 100)
		}
		return o
	}
	timeout := time.Duration(c.Params.TimeoutMS) * time.Millisecond
	switch c.Type {
	case store.CheckICMP:
		r, err := probe.Ping(ctx, addr, c.Params.Count, timeout)
		if err != nil && r.Sent == 0 {
			o.res.Text = err.Error()
			return o
		}
		sample("loss_pct", round2(r.Loss()))
		if avg, ok := r.AvgRTT(); ok {
			sample("rtt_ms", ms(avg))
			o.res.Status = store.StatusUp
			o.res.Text = fmt.Sprintf("%.2f ms, %.0f%% loss", ms(avg), r.Loss())
		} else {
			o.res.Text = "no answer (100% loss)"
		}
	case store.CheckTCP:
		rtt, err := probe.TCPConnect(ctx, addr, c.Params.Port, timeout)
		if err != nil {
			o.res.Text = err.Error()
			return o
		}
		sample("connect_ms", ms(rtt))
		o.res.Status = store.StatusUp
		o.res.Text = fmt.Sprintf("open, %.2f ms", ms(rtt))
	default:
		o.res.Text = "unknown check type"
	}
	return o
}
