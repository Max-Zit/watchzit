// Package poller runs the checks of all devices on their intervals, stores
// their results and feeds the metric store.
package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/netip"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/probe"
	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/snmpmon"
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

	mu       sync.Mutex
	entries  map[int64]*entry
	reload   chan struct{}
	profiles map[int64]*store.SNMPProfile
	counters map[int64]*counterState // last raw counters per check
}

type counterState struct {
	at   time.Time
	vals map[string]snmpmon.Counter
}

type entry struct {
	c       *store.Check
	next    time.Time
	running bool
}

// New creates a poller.
func New(st *store.Store, db *tsdb.DB, log *slog.Logger) *Poller {
	return &Poller{store: st, db: db, log: log, now: time.Now, Workers: 64, entries: map[int64]*entry{}, reload: make(chan struct{}, 1), profiles: map[int64]*store.SNMPProfile{}, counters: map[int64]*counterState{}}
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
	profs, err := p.store.SNMPProfiles(ctx)
	if err != nil {
		return err
	}
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.profiles = map[int64]*store.SNMPProfile{}
	for _, pr := range profs {
		p.profiles[pr.ID] = pr
	}
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
			delete(p.counters, id)
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
	// facts and interfaces of SNMP devices: soon after start, then hourly
	refresh := time.NewTimer(20 * time.Second)
	defer refresh.Stop()
	var refreshing sync.WaitGroup
	defer refreshing.Wait()
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
		case <-refresh.C:
			refresh.Reset(time.Hour)
			refreshing.Add(1)
			go func() { defer refreshing.Done(); p.Refresh(ctx, 0) }()
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
		if !store.IsSNMP(c.Type) {
			o.res.Text = "unknown check type"
			break
		}
		p.runSNMP(ctx, c, addr, timeout, now, &o, sample)
	}
	return o
}

// snmpClient opens a client with the device's profile.
func (p *Poller) snmpClient(profileID int64, addr netip.Addr, timeout time.Duration) (*snmp.Client, error) {
	p.mu.Lock()
	pr := p.profiles[profileID]
	p.mu.Unlock()
	if pr == nil {
		return nil, errors.New("the device has no SNMP profile")
	}
	cfg, err := p.store.SNMPConfig(pr)
	if err != nil {
		return nil, err
	}
	cfg.Timeout = timeout
	return snmp.Dial(netip.AddrPortFrom(addr, uint16(pr.Port)), cfg) // #nosec G115 -- port is 1–65535
}

func (p *Poller) runSNMP(ctx context.Context, c *store.Check, addr netip.Addr, timeout time.Duration, now time.Time, o *outcome, sample func(string, float64)) {
	cl, err := p.snmpClient(c.SNMPProfile, addr, timeout)
	if err != nil {
		o.res.Text = err.Error()
		return
	}
	defer cl.Close()
	start := time.Now()
	r, err := snmpmon.Poll(ctx, cl, c)
	if err != nil {
		o.res.Text = err.Error()
		return
	}
	if c.Type == store.CheckSNMP {
		sample("response_ms", ms(time.Since(start)))
	}
	if r.Params != nil {
		if err := p.store.UpdateCheckParams(ctx, c.ID, *r.Params); err != nil {
			p.log.Warn("poller: cannot save check parameters", "check", c.ID, "err", err)
		}
	}
	for m, v := range r.Gauges {
		sample(m, round2(v))
	}
	if len(r.Counters) > 0 {
		p.mu.Lock()
		prev := p.counters[c.ID]
		p.counters[c.ID] = &counterState{at: now, vals: r.Counters}
		p.mu.Unlock()
		if prev != nil {
			dt := now.Sub(prev.at).Seconds()
			rates := map[string]float64{}
			for m, cur := range r.Counters {
				if old, ok := prev.vals[m]; ok {
					if v, ok := rate(old, cur, dt); ok {
						sample(m, round2(v))
						rates[m] = v
					}
				}
			}
			in, okIn := rates["in_bps"]
			out, okOut := rates["out_bps"]
			if okIn && okOut {
				// the part after " · " is what the port tiles show
				r.Text += " · ↓ " + bitsText(in) + "  ↑ " + bitsText(out)
			}
		}
	}
	o.res.Text = r.Text
	if r.Up {
		o.res.Status = store.StatusUp
	}
}

// rate turns two counter readings into a change per second. A 32-bit
// counter that went backwards wrapped; a 64-bit one was reset (reboot).
func rate(prev, cur snmpmon.Counter, dt float64) (float64, bool) {
	if dt < 1 {
		return 0, false
	}
	var d uint64
	switch {
	case cur.V >= prev.V:
		d = cur.V - prev.V
	case cur.Bits == 32 && prev.V <= math.MaxUint32:
		d = cur.V + (1 << 32) - prev.V
	default:
		return 0, false
	}
	return float64(d) / dt * cur.Mul, true
}

func bitsText(bps float64) string {
	u := []string{"bit/s", "kbit/s", "Mbit/s", "Gbit/s", "Tbit/s"}
	i := 0
	for bps >= 1000 && i < len(u)-1 {
		bps /= 1000
		i++
	}
	return fmt.Sprintf("%.3g %s", bps, u[i])
}

// Refresh reads facts and interfaces of SNMP devices (all when id is 0).
func (p *Poller) Refresh(ctx context.Context, id int64) {
	devs, err := p.store.DevicesWithSNMP(ctx)
	if err != nil {
		p.log.Error("poller: cannot list SNMP devices", "err", err)
		return
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, d := range devs {
		if (id != 0 && d.ID != id) || d.Paused {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			if err := p.refreshDevice(ctx, d); err != nil {
				p.log.Debug("poller: SNMP refresh failed", "device", d.Name, "err", err)
			}
		}()
	}
	wg.Wait()
}

func (p *Poller) refreshDevice(ctx context.Context, d *store.Device) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	addr, err := probe.Resolve(ctx, d.Address)
	if err != nil {
		return err
	}
	cl, err := p.snmpClient(d.SNMPProfile, addr, 3*time.Second)
	if err != nil {
		return err
	}
	defer cl.Close()
	f, err := snmpmon.Facts(ctx, cl)
	if err != nil {
		return err
	}
	f.Updated = p.now()
	if err := p.store.SetFacts(ctx, d.ID, f); err != nil {
		return err
	}
	ifs, err := snmpmon.Interfaces(ctx, cl)
	if err != nil {
		return err
	}
	return p.store.SetInterfaces(ctx, d.ID, ifs)
}
