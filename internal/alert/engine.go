// Package alert turns check results into problems: every few seconds it
// evaluates the triggers against the latest state of devices and checks,
// opens and resolves problems, applies maintenance windows and device
// dependencies, and hands what changed to the notifier.
package alert

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// Event kinds.
const (
	EventProblem  = "problem"
	EventResolved = "resolved"
)

// Event is a problem to send: it started (or stopped being suppressed) or
// it was resolved.
type Event struct {
	Kind    string
	Problem store.Problem
}

// Engine evaluates triggers.
type Engine struct {
	st  *store.Store
	log *slog.Logger
	now func() time.Time
	// Notify gets the events of an evaluation (may be nil).
	Notify func(ctx context.Context, events []Event)
	// Every is the evaluation period.
	Every time.Duration

	mu      sync.Mutex // one evaluation at a time
	pending map[string]time.Time
	kick    chan struct{}
}

// New creates an engine.
func New(st *store.Store, log *slog.Logger) *Engine {
	return &Engine{st: st, log: log, now: time.Now, Every: 15 * time.Second, pending: map[string]time.Time{}, kick: make(chan struct{}, 1)}
}

// SetClock replaces the clock (tests).
func (e *Engine) SetClock(now func() time.Time) { e.now = now }

// Kick asks for an evaluation soon (a check changed status).
func (e *Engine) Kick() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// Run evaluates until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(e.Every)
	defer t.Stop()
	purge := time.NewTicker(6 * time.Hour)
	defer purge.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-e.kick:
			// let a batch of results land first
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		case <-purge.C:
			if n, err := e.st.PurgeProblems(ctx, e.now().AddDate(-1, 0, 0)); err == nil && n > 0 {
				e.log.Info("old problems deleted", "count", n)
			}
			continue
		}
		if err := e.Evaluate(ctx); err != nil && ctx.Err() == nil {
			e.log.Error("alert evaluation failed", "err", err)
		}
	}
}

// condition is a problem that should be open now.
type condition struct {
	trigger  *store.Trigger
	device   *store.AlertDevice
	check    *store.AlertCheck // nil for device_down
	detail   string
	suppress string
	ha       int64 // HA group of an ha_degraded problem
	log      bool  // a log trigger's problem
}

func (c *condition) key() string {
	if c.ha != 0 {
		return "t" + strconv.FormatInt(c.trigger.ID, 10) + ":ha" + strconv.FormatInt(c.ha, 10)
	}
	k := "t" + strconv.FormatInt(c.trigger.ID, 10) + ":d" + strconv.FormatInt(c.device.ID, 10)
	if c.log {
		return k + ":log"
	}
	if c.check != nil {
		k += ":c" + strconv.FormatInt(c.check.ID, 10)
	}
	return k
}

// Evaluate runs the triggers once.
func (e *Engine) Evaluate(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	triggers, err := e.st.Triggers(ctx)
	if err != nil {
		return err
	}
	devs, checks, err := e.st.AlertInputs(ctx)
	if err != nil {
		return err
	}
	open, err := e.st.OpenProblems(ctx)
	if err != nil {
		return err
	}
	maint, err := e.st.Maintenances(ctx, now)
	if err != nil {
		return err
	}
	byID := make(map[int64]*store.AlertDevice, len(devs))
	for i := range devs {
		byID[devs[i].ID] = &devs[i]
	}
	checksOf := map[int64][]*store.AlertCheck{}
	for i := range checks {
		c := &checks[i]
		checksOf[c.DeviceID] = append(checksOf[c.DeviceID], c)
	}
	openByKey := make(map[string]*store.Problem, len(open))
	for _, p := range open {
		openByKey[p.Key] = p
	}

	// messages for log triggers: as far back as the longest of them looks
	var logs []*store.LogMessage
	logWindow := time.Duration(0)
	for _, t := range triggers {
		if t.Enabled && t.Kind == store.TriggerLog && time.Duration(t.ForS)*time.Second > logWindow {
			logWindow = time.Duration(t.ForS) * time.Second
		}
	}
	if logWindow > 0 {
		if logs, err = e.st.Logs(ctx, store.LogFilter{Since: now.Add(-logWindow), Limit: 5000}); err != nil {
			return err
		}
	}
	logsOf := map[int64][]*store.LogMessage{}
	for _, m := range logs {
		if m.DeviceID != 0 {
			logsOf[m.DeviceID] = append(logsOf[m.DeviceID], m)
		}
	}

	active := map[string]*condition{}
	seenPending := map[string]bool{}
	for _, t := range triggers {
		if !t.Enabled {
			continue
		}
		if t.Kind == store.TriggerHADegraded {
			for _, c := range e.matchHA(ctx, t, byID, now, openByKey, seenPending) {
				active[c.key()] = c
			}
			continue
		}
		for i := range devs {
			d := &devs[i]
			if d.Paused || (t.DeviceID != 0 && t.DeviceID != d.ID) || (t.GroupID != 0 && t.GroupID != d.GroupID) {
				continue
			}
			if t.Kind == store.TriggerLog {
				if c := matchLog(t, d, logsOf[d.ID], now); c != nil {
					active[c.key()] = c
				}
				continue
			}
			for _, c := range e.match(t, d, checksOf[d.ID], now, openByKey, seenPending) {
				active[c.key()] = c
			}
		}
	}
	for k := range e.pending {
		if !seenPending[k] {
			delete(e.pending, k)
		}
	}

	// Suppression: maintenance first, then a down parent somewhere above.
	for _, c := range active {
		switch {
		case inMaintenance(maint, c.device, now):
			c.suppress = "maintenance"
		case parentDown(byID, c.device):
			c.suppress = "dependency"
		}
	}

	var events []Event
	var notified []int64
	for _, p := range open {
		c := active[p.Key]
		if c == nil {
			if err := e.st.ResolveProblem(ctx, p.ID, now); err != nil {
				return err
			}
			if p.NotifiedAt != nil {
				r := *p
				r.ResolvedAt = &now
				events = append(events, Event{Kind: EventResolved, Problem: r})
			}
			continue
		}
		if c.trigger.Severity != p.Severity || c.trigger.Name != p.Title || c.detail != p.Detail || c.suppress != p.Suppressed {
			if err := e.st.UpdateProblem(ctx, p.ID, c.trigger.Severity, c.trigger.Name, c.detail, c.suppress); err != nil {
				return err
			}
			p.Severity, p.Title, p.Detail, p.Suppressed = c.trigger.Severity, c.trigger.Name, c.detail, c.suppress
		}
		if p.Suppressed == "" && p.NotifiedAt == nil {
			// suppressed until now (maintenance over, parent back up)
			events = append(events, Event{Kind: EventProblem, Problem: *p})
			notified = append(notified, p.ID)
		}
	}
	for k, c := range active {
		if openByKey[k] != nil {
			continue
		}
		since := now
		if c.check != nil && c.trigger.Kind == store.TriggerCheckDown {
			since = c.check.StatusSince
		} else if c.trigger.Kind == store.TriggerDeviceDown {
			since = c.device.StatusSince
		} else if p, ok := e.pending[k]; ok {
			since = p
		}
		n := store.NewProblem{Key: k, TriggerID: c.trigger.ID, DeviceID: c.device.ID, Severity: c.trigger.Severity, Title: c.trigger.Name,
			Detail: c.detail, Suppressed: c.suppress, At: since}
		if c.check != nil {
			n.CheckID = c.check.ID
		}
		id, err := e.st.OpenProblem(ctx, n)
		if err != nil {
			return err
		}
		if id == 0 {
			continue
		}
		e.log.Info("problem", "device", c.device.Name, "trigger", c.trigger.Name, "detail", c.detail, "suppressed", c.suppress)
		if c.suppress == "" {
			p := store.Problem{ID: id, Key: k, TriggerID: c.trigger.ID, DeviceID: c.device.ID, Device: c.device.Name, Address: c.device.Address,
				Severity: c.trigger.Severity, Title: c.trigger.Name, Detail: c.detail, StartedAt: since}
			if c.check != nil {
				p.CheckID, p.Check = c.check.ID, c.check.Name
			}
			events = append(events, Event{Kind: EventProblem, Problem: p})
			notified = append(notified, id)
		}
	}
	if err := e.st.MarkNotified(ctx, notified, now); err != nil {
		return err
	}
	if len(events) > 0 && e.Notify != nil {
		e.Notify(ctx, events)
	}
	return nil
}

// match returns the conditions of one trigger on one device.
func (e *Engine) match(t *store.Trigger, d *store.AlertDevice, cs []*store.AlertCheck, now time.Time, open map[string]*store.Problem, seen map[string]bool) []*condition {
	var out []*condition
	switch t.Kind {
	case store.TriggerDeviceDown:
		if d.Status == store.StatusDown && now.Sub(d.StatusSince) >= time.Duration(t.ForS)*time.Second {
			out = append(out, &condition{trigger: t, device: d, detail: downDetail(cs)})
		}
	case store.TriggerCheckDown:
		if d.Status == store.StatusDown {
			return nil // the device-down problem says it all
		}
		for _, c := range cs {
			if c.Paused || c.Status != store.StatusDown || !typeIn(t.CheckTypes, c.Type) {
				continue
			}
			if now.Sub(c.StatusSince) >= time.Duration(t.ForS)*time.Second {
				out = append(out, &condition{trigger: t, device: d, check: c, detail: c.Name + ": " + c.LastText})
			}
		}
	case store.TriggerMetric:
		for _, c := range cs {
			if c.Paused || c.Status != store.StatusUp || !typeIn(t.CheckTypes, c.Type) {
				continue
			}
			v, ok := c.Values[t.Metric]
			if !ok || math.IsNaN(v) {
				continue
			}
			if !(t.Op == ">" && v > t.Threshold || t.Op == "<" && v < t.Threshold) {
				continue
			}
			cond := &condition{trigger: t, device: d, check: c}
			k := cond.key()
			seen[k] = true
			first, ok := e.pending[k]
			if !ok {
				first = now
				e.pending[k] = now
			}
			// an open problem stays open while the value is still out of
			// range (also right after a restart, when nothing is pending)
			if open[k] == nil && now.Sub(first) < time.Duration(t.ForS)*time.Second {
				continue
			}
			unit := store.MetricUnit(c.Type, t.Metric, c.Params)
			cond.detail = fmt.Sprintf("%s: %s %s %s", c.Name, FormatValue(v, unit), t.Op, FormatValue(t.Threshold, unit))
			out = append(out, cond)
		}
	}
	return out
}

func typeIn(types []string, t string) bool {
	if len(types) == 0 {
		return true
	}
	for _, x := range types {
		if x == t {
			return true
		}
	}
	return false
}

func downDetail(cs []*store.AlertCheck) string {
	var parts []string
	for _, c := range cs {
		if !c.Paused && c.Status == store.StatusDown {
			parts = append(parts, c.Name+": "+c.LastText)
		}
		if len(parts) == 3 {
			parts = append(parts, "…")
			break
		}
	}
	return strings.Join(parts, "; ")
}

func inMaintenance(ms []*store.Maintenance, d *store.AlertDevice, now time.Time) bool {
	for _, m := range ms {
		if m.Active(now) && m.Covers(d.ID, d.GroupID) {
			return true
		}
	}
	return false
}

// parentDown reports whether a device above this one is down.
func parentDown(byID map[int64]*store.AlertDevice, d *store.AlertDevice) bool {
	seen := map[int64]bool{d.ID: true}
	for p := byID[d.Parent]; p != nil && !seen[p.ID]; p = byID[p.Parent] {
		if p.Status == store.StatusDown {
			return true
		}
		seen[p.ID] = true
	}
	return false
}

// FormatValue writes a value with its unit for a person.
func FormatValue(v float64, unit string) string {
	switch unit {
	case "%":
		return strconv.FormatFloat(round(v), 'f', -1, 64) + "%"
	case "bit/s":
		u := []string{"bit/s", "kbit/s", "Mbit/s", "Gbit/s", "Tbit/s"}
		i := 0
		for math.Abs(v) >= 1000 && i < len(u)-1 {
			v /= 1000
			i++
		}
		return strconv.FormatFloat(round(v), 'f', -1, 64) + " " + u[i]
	case "B":
		u := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
		i := 0
		for math.Abs(v) >= 1024 && i < len(u)-1 {
			v /= 1024
			i++
		}
		return strconv.FormatFloat(round(v), 'f', -1, 64) + " " + u[i]
	case "":
		return strconv.FormatFloat(round(v), 'f', -1, 64)
	}
	return strconv.FormatFloat(round(v), 'f', -1, 64) + " " + unit
}

func round(v float64) float64 {
	if math.Abs(v) >= 100 {
		return math.Round(v)
	}
	return math.Round(v*100) / 100
}

// matchHA returns the HA groups that run on fewer members than they have:
// some member devices down, not all (all down is the devices' own problem).
// The problem belongs to the first member that is down.
func (e *Engine) matchHA(ctx context.Context, t *store.Trigger, byID map[int64]*store.AlertDevice, now time.Time, open map[string]*store.Problem, seen map[string]bool) []*condition {
	groups, err := e.st.HAGroups(ctx)
	if err != nil {
		e.log.Warn("alerts: HA groups not read", "err", err)
		return nil
	}
	var out []*condition
	for _, g := range groups {
		var down, up []*store.AlertDevice
		scoped := t.DeviceID == 0 && t.GroupID == 0
		for _, id := range g.Members {
			d := byID[id]
			if d == nil || d.Paused {
				continue
			}
			if t.DeviceID == d.ID || (t.GroupID != 0 && t.GroupID == d.GroupID) {
				scoped = true
			}
			if d.Status == store.StatusDown {
				down = append(down, d)
			} else {
				up = append(up, d)
			}
		}
		if !scoped || len(down) == 0 || len(up) == 0 {
			continue
		}
		c := &condition{trigger: t, device: down[0], ha: g.ID}
		k := c.key()
		seen[k] = true
		first, ok := e.pending[k]
		if !ok {
			first = now
			e.pending[k] = now
		}
		if open[k] == nil && now.Sub(first) < time.Duration(t.ForS)*time.Second {
			continue
		}
		name := g.Name
		if name == "" {
			name = g.Map
		}
		c.detail = fmt.Sprintf("%s: %s down, %s still up", name, deviceNames(down), deviceNames(up))
		out = append(out, c)
	}
	return out
}

func deviceNames(ds []*store.AlertDevice) string {
	names := make([]string, len(ds))
	for i, d := range ds {
		names[i] = d.Name
	}
	return strings.Join(names, ", ")
}

// matchLog: the newest message of a device that a log trigger looks for,
// within its window; the problem stays open while such messages keep coming.
func matchLog(t *store.Trigger, d *store.AlertDevice, ms []*store.LogMessage, now time.Time) *condition {
	re := logPattern(t.Metric)
	for _, m := range ms { // newest first
		if now.Sub(m.At) > time.Duration(t.ForS)*time.Second {
			break
		}
		if m.Severity > int(t.Threshold) || (len(t.CheckTypes) > 0 && !typeIn(t.CheckTypes, m.Kind)) {
			continue
		}
		text := m.Tag + " " + m.Message
		if re != nil && re.MatchString(text) || re == nil && strings.Contains(strings.ToLower(text), strings.ToLower(t.Metric)) {
			detail := m.Message
			if len(detail) > 300 {
				detail = detail[:300] + "…"
			}
			return &condition{trigger: t, device: d, detail: detail, log: true}
		}
	}
	return nil
}

var (
	patMu    sync.Mutex
	patCache = map[string]*regexp.Regexp{}
)

// logPattern is the regular expression of a pattern written as /…/ (nil:
// look for the text itself).
func logPattern(p string) *regexp.Regexp {
	if len(p) < 3 || p[0] != '/' || p[len(p)-1] != '/' {
		return nil
	}
	patMu.Lock()
	defer patMu.Unlock()
	if re, ok := patCache[p]; ok {
		return re
	}
	re, err := regexp.Compile("(?i)" + p[1:len(p)-1])
	if err != nil {
		re = regexp.MustCompile(regexp.QuoteMeta(p))
	}
	patCache[p] = re
	return re
}
