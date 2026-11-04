package poller

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/tsdb"
)

// A remote collector is an agent that runs the checks of the devices of
// another network (a branch office, a customer site behind NAT) and sends
// the results to the console over its channel. It runs this same poller
// without a database: the console sends the checks with their passwords
// and SNMP settings, and gets back what the poller would have stored.

// RemoteConfig is what the console sends a collector (proto.TypeCollectorConfig).
type RemoteConfig struct {
	Checks []RemoteCheck        `json:"checks"`
	SNMP   map[int64]RemoteSNMP `json:"snmp,omitempty"` // by SNMP profile ID
}

// RemoteSNMP is an SNMP profile with its secrets opened.
type RemoteSNMP struct {
	Config snmp.Config `json:"config"`
	Port   int         `json:"port"`
}

// RemoteCheck is a check with its password opened.
type RemoteCheck struct {
	Check  *store.Check `json:"check"`
	Secret string       `json:"secret,omitempty"`
}

// RemoteResult is one run of a check (proto.TypeCollectorResults).
type RemoteResult struct {
	Result  store.CheckResult `json:"result"`
	Samples []tsdb.Sample     `json:"samples,omitempty"`
}

// RemoteResults is a batch of runs.
type RemoteResults struct {
	Seq     int64          `json:"seq,omitempty"` // the console acknowledges it once stored
	Results []RemoteResult `json:"results"`
}

// RemoteAck tells a collector that a batch is stored (proto.TypeCollectorAck).
type RemoteAck struct {
	Seq int64 `json:"seq"`
}

// RemoteType reports the check types a collector runs. Hypervisors keep an
// inventory and agent checks come from the agents themselves, so those
// stay with the console.
func RemoteType(typ string) bool {
	return !store.IsAgent(typ) && typ != store.CheckHypervisor
}

// NewCollector creates a poller without a database; Results gets the runs.
func NewCollector(log *slog.Logger, results func([]RemoteResult)) *Poller {
	p := New(nil, nil, log)
	p.Workers = 16
	p.results = results
	return p
}

// SetRemote replaces the checks of a collector.
func (p *Poller) SetRemote(cfg RemoteConfig) {
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snmpCfg = cfg.SNMP
	p.secrets = map[int64]string{}
	seen := map[int64]bool{}
	for _, rc := range cfg.Checks {
		c := rc.Check
		if c == nil || !RemoteType(c.Type) || c.IntervalS < 10 {
			continue
		}
		seen[c.ID] = true
		p.secrets[c.ID] = rc.Secret
		if e := p.entries[c.ID]; e != nil {
			if e.c.IntervalS != c.IntervalS {
				e.next = now
			}
			e.c = c
			continue
		}
		delay := time.Duration(rand.Int64N(int64(min(c.IntervalS, 5)) * int64(time.Second))) // #nosec G404 -- jitter
		p.entries[c.ID] = &entry{c: c, next: now.Add(delay)}
	}
	for id := range p.entries {
		if !seen[id] {
			delete(p.entries, id)
			delete(p.counters, id)
		}
	}
}

// Remote reports the number of checks a collector runs.
func (p *Poller) Remote() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// sendLoop is the saveLoop of a collector: batches go to Results.
func (p *Poller) sendLoop(ctx context.Context, results <-chan outcome) {
	var batch []RemoteResult
	flush := func() {
		if len(batch) > 0 && p.results != nil {
			p.results(batch)
		}
		batch = nil
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case o, ok := <-results:
			if !ok {
				flush()
				return
			}
			batch = append(batch, RemoteResult{Result: o.res, Samples: o.samples})
			p.done(o.res.CheckID)
			if len(batch) >= 200 {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}

// runRemote is Run of a collector: no database to reload from, no
// topology or SNMP refresh (the console does those).
func (p *Poller) runRemote(ctx context.Context) {
	jobs := make(chan *store.Check, p.Workers)
	results := make(chan outcome, p.Workers*4)
	done := make(chan struct{})
	for i := 0; i < p.Workers; i++ {
		go func() {
			for c := range jobs {
				results <- p.runCheck(ctx, c)
			}
			done <- struct{}{}
		}()
	}
	sent := make(chan struct{})
	go func() {
		p.sendLoop(ctx, results)
		close(sent)
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			close(jobs)
			for i := 0; i < p.Workers; i++ {
				<-done
			}
			close(results)
			<-sent
			return
		case <-tick.C:
			p.dispatch(ctx, jobs)
		}
	}
}
