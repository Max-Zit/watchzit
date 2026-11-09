package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/discovery"
	"github.com/max-zit/watchzit/internal/poller"
	"github.com/max-zit/watchzit/internal/proto"
	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/tsdb"
)

// Remote collectors: an agent marked as collector gets the checks of the
// devices assigned to it (with their passwords and SNMP settings, over its
// pinned TLS channel), runs them with the poller and sends the results.
// It has no web interface of its own; everything is set here.

// collectorState is what a connection was last told to run.
type collectorState struct {
	mu      sync.Mutex
	hash    string
	allowed map[int64]map[int32]bool // check → its series
}

var collectorSync sync.Mutex

// syncCollectors sends each connected agent the checks it runs as a
// collector when they changed (none: it stops collecting).
func (s *Server) syncCollectors() {
	// many changes in a row (a bulk edit, agents reconnecting) make one run
	// plus at most one more
	collectorSyncState.Lock()
	if collectorSyncState.running {
		collectorSyncState.again = true
		collectorSyncState.Unlock()
		return
	}
	collectorSyncState.running = true
	collectorSyncState.Unlock()
	for {
		collectorSync.Lock()
		for _, c := range s.hub.all() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := s.sendCollectorConfig(ctx, c, false); err != nil {
				s.log.Warn("cannot update the remote collector", "device", c.deviceID, "err", err)
			}
			cancel()
		}
		collectorSync.Unlock()
		collectorSyncState.Lock()
		if !collectorSyncState.again {
			collectorSyncState.running = false
			collectorSyncState.Unlock()
			return
		}
		collectorSyncState.again = false
		collectorSyncState.Unlock()
	}
}

var collectorSyncState struct {
	sync.Mutex
	running, again bool
}

// configHash covers what a collector runs, not the state of the checks
// (status and last values change with every result).
func configHash(cfg poller.RemoteConfig) string {
	type key struct {
		ID, Profile       int64
		Type, Address     string
		Params            store.CheckParams
		Interval          int
		Paused, DevPaused bool
		Secret            string
	}
	ks := make([]key, 0, len(cfg.Checks))
	for _, c := range cfg.Checks {
		ks = append(ks, key{c.Check.ID, c.Check.SNMPProfile, c.Check.Type, c.Check.Address, c.Check.Params, c.Check.IntervalS, c.Check.Paused, c.Check.DevPaused, c.Secret})
	}
	raw, _ := json.Marshal(struct {
		K []key
		S map[int64]poller.RemoteSNMP
	}{ks, cfg.SNMP})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Server) sendCollectorConfig(ctx context.Context, c *agentConn, hello bool) error {
	cfg, allowed, err := s.collectorConfig(ctx, c.deviceID)
	if err != nil {
		return err
	}
	hash := configHash(cfg)
	c.coll.mu.Lock()
	defer c.coll.mu.Unlock()
	// after hello always: an agent that was a collector before (and kept
	// running while away) must hear that it is not any more
	if !hello && (hash == c.coll.hash || (c.coll.hash == "" && len(cfg.Checks) == 0)) {
		return nil
	}
	if err := sendAgent(ctx, c.ws, proto.TypeCollectorConfig, cfg); err != nil {
		return err
	}
	c.coll.hash, c.coll.allowed = hash, allowed
	return nil
}

// collectorConfig is what a collector runs: the checks of its devices with
// passwords and SNMP settings opened.
func (s *Server) collectorConfig(ctx context.Context, deviceID int64) (poller.RemoteConfig, map[int64]map[int32]bool, error) {
	cfg := poller.RemoteConfig{Checks: []poller.RemoteCheck{}}
	allowed := map[int64]map[int32]bool{}
	d, err := s.store.DeviceByID(ctx, deviceID)
	if err != nil || !d.Collector {
		return cfg, allowed, nil
	}
	cs, err := s.store.CollectorChecks(ctx, deviceID)
	if err != nil {
		return cfg, nil, err
	}
	// and, as a backup, the checks of devices whose own collector is offline
	if bs, err := s.store.BackupChecks(ctx, deviceID); err == nil {
		for _, c := range bs {
			if s.hub.get(c.Collector) == nil {
				cs = append(cs, c)
			}
		}
	}
	for _, c := range cs {
		if !poller.RemoteType(c.Type) {
			continue
		}
		secret, err := s.store.CheckSecret(c)
		if err != nil {
			secret = ""
		}
		cfg.Checks = append(cfg.Checks, poller.RemoteCheck{Check: c, Secret: secret})
		allowed[c.ID] = map[int32]bool{}
		for _, x := range c.Series {
			allowed[c.ID][x.ID] = true
		}
		if c.SNMPProfile != 0 && store.IsSNMP(c.Type) {
			if _, ok := cfg.SNMP[c.SNMPProfile]; ok {
				continue
			}
			pr, err := s.store.SNMPProfileByID(ctx, c.SNMPProfile)
			if err != nil {
				continue
			}
			sc, err := s.store.SNMPConfig(pr)
			if err != nil {
				continue
			}
			if cfg.SNMP == nil {
				cfg.SNMP = map[int64]poller.RemoteSNMP{}
			}
			cfg.SNMP[c.SNMPProfile] = poller.RemoteSNMP{Config: sc, Port: pr.Port}
		}
	}
	return cfg, allowed, nil
}

// collectorResults stores what a collector ran — only for checks it was
// given, with their own series.
func (s *Server) collectorResults(ctx context.Context, c *agentConn, rr *poller.RemoteResults) error {
	c.coll.mu.Lock()
	allowed := c.coll.allowed
	c.coll.mu.Unlock()
	if allowed == nil {
		return errNotReady // no checks sent yet: not acknowledged, the collector sends it again
	}
	var ids []int64
	for _, r := range rr.Results {
		ids = append(ids, r.Result.CheckID)
	}
	// older than what is stored already (a backup ran it meanwhile, or a
	// batch came twice): not taken
	last, err := s.store.LastRuns(ctx, ids)
	if err != nil {
		return err
	}
	now := s.now()
	var res []store.CheckResult
	var samples []tsdb.Sample
	for _, r := range rr.Results {
		series, ok := allowed[r.Result.CheckID]
		if !ok || (r.Result.Status != store.StatusUp && r.Result.Status != store.StatusDown) {
			continue
		}
		// results kept while the channel was down are up to a day old; a
		// clock far off is set to the console's
		clamped := false
		if at := r.Result.At; at.After(now.Add(time.Minute)) || at.Before(now.Add(-25*time.Hour)) {
			r.Result.At, clamped = now, true
		}
		if t, ok := last[r.Result.CheckID]; ok && !clamped && !r.Result.At.After(t) {
			continue
		}
		last[r.Result.CheckID] = r.Result.At
		if len(r.Result.Text) > 500 {
			r.Result.Text = r.Result.Text[:500]
		}
		if len(r.Result.Values) > 32 {
			continue // no check has that many values
		}
		if r.Result.Path != nil {
			r.Result.Path = store.CleanPath(r.Result.Path)
		}
		res = append(res, r.Result)
		for _, x := range r.Samples {
			if series[x.Series] {
				x.T = r.Result.At
				samples = append(samples, x)
			}
		}
	}
	if len(res) == 0 {
		return nil
	}
	changes, err := s.store.SaveResults(ctx, res)
	if err != nil {
		return err
	}
	if s.tsdb != nil {
		s.tsdb.Add(samples...)
	}
	if len(changes) > 0 && s.poller != nil && s.poller.OnChange != nil {
		s.poller.OnChange(changes)
	}
	return nil
}

// collectorOffline: the devices of a collector that lost its channel are
// unknown until it is back (not down: the site may be fine).
func (s *Server) collectorOffline(ctx context.Context, deviceID int64) {
	cs, err := s.store.CollectorChecks(ctx, deviceID)
	if err != nil {
		return
	}
	// the devices it was running for another collector that is offline too
	if bs, err := s.store.BackupChecks(ctx, deviceID); err == nil {
		for _, c := range bs {
			if s.hub.get(c.Collector) == nil {
				cs = append(cs, c)
			}
		}
	}
	var res []store.CheckResult
	for _, c := range cs {
		// a connected backup takes them over
		if c.Collector == deviceID && c.Backup != 0 && s.hub.get(c.Backup) != nil {
			continue
		}
		if poller.RemoteType(c.Type) {
			res = append(res, store.CheckResult{CheckID: c.ID, At: s.now(), Status: store.StatusUnknown, Text: "the remote collector is offline"})
		}
	}
	if _, err := s.store.SaveResults(ctx, res); err != nil {
		s.log.Warn("cannot mark the collector's checks", "device", deviceID, "err", err)
	}
}

// handleDeviceCollector: the device page turns an agent into a collector
// or back ("role"), or chooses where a device's checks run ("collector").
func (s *Server) handleDeviceCollector(w http.ResponseWriter, r *http.Request) {
	d := s.loadDevice(w, r)
	if d == nil {
		return
	}
	ctx := r.Context()
	back := "/devices/" + strconv.FormatInt(d.ID, 10)
	if role := r.FormValue("role"); role != "" {
		on := role == "on"
		if err := s.store.SetCollector(ctx, d.ID, on); err != nil {
			s.redirectErr(w, r, back, storeErr(err))
			return
		}
		s.audit(r, "device.collector", "device:"+strconv.FormatInt(d.ID, 10), "remote collector %v on %s", on, d.Name)
		s.poller.Reload()
		if on {
			s.redirect(w, r, back, "This agent is now a remote collector. Choose it under Monitored from on the devices of its network.")
		} else {
			s.redirect(w, r, back, "This agent is no longer a remote collector; the console checks its devices again.")
		}
		return
	}
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("collector"), 10, 64)
	if _, err := s.store.SetDeviceCollector(ctx, []int64{d.ID}, id); err != nil {
		s.redirectErr(w, r, back, storeErr(err))
		return
	}
	if r.Form.Has("backup") {
		b, _ := strconv.ParseInt(r.FormValue("backup"), 10, 64)
		if _, err := s.store.SetDeviceBackup(ctx, []int64{d.ID}, b); err != nil {
			s.redirectErr(w, r, back, storeErr(err))
			return
		}
	}
	s.audit(r, "device.collector", "device:"+strconv.FormatInt(d.ID, 10), "checks of %s run by collector %d (0 = console)", d.Name, id)
	s.poller.Reload()
	s.redirect(w, r, back, "Saved.")
}

// ---- jobs: discovery scans and SNMP reads done by a collector

var collectorJobs = struct {
	sync.Mutex
	wait map[string]chan poller.RemoteJobResult
	from map[string]int64 // job → the collector that must answer it
}{wait: map[string]chan poller.RemoteJobResult{}, from: map[string]int64{}}

// snmpProfiles opens the SNMP profiles for a collector (all of them, or
// just one).
func (s *Server) snmpProfiles(ctx context.Context, only int64) map[int64]poller.RemoteSNMP {
	out := map[int64]poller.RemoteSNMP{}
	prs, err := s.store.SNMPProfiles(ctx)
	if err != nil {
		return out
	}
	for _, pr := range prs {
		if only != 0 && pr.ID != only {
			continue
		}
		if sc, err := s.store.SNMPConfig(pr); err == nil {
			out[pr.ID] = poller.RemoteSNMP{Config: sc, Port: pr.Port}
		}
	}
	return out
}

// collectorJob gives a job to a connected collector and waits for its answer.
func (s *Server) collectorJob(ctx context.Context, collectorID int64, j poller.RemoteJob, timeout time.Duration) (poller.RemoteJobResult, error) {
	if d, err := s.store.DeviceByID(ctx, collectorID); err != nil || !d.Collector {
		return poller.RemoteJobResult{}, errors.New("that device is not a remote collector")
	}
	c := s.hub.get(collectorID)
	if c == nil {
		return poller.RemoteJobResult{}, errors.New("the remote collector is offline")
	}
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return poller.RemoteJobResult{}, err
	}
	j.ID = hex.EncodeToString(b[:])
	ch := make(chan poller.RemoteJobResult, 1)
	collectorJobs.Lock()
	collectorJobs.wait[j.ID], collectorJobs.from[j.ID] = ch, collectorID
	collectorJobs.Unlock()
	defer func() {
		collectorJobs.Lock()
		delete(collectorJobs.wait, j.ID)
		delete(collectorJobs.from, j.ID)
		collectorJobs.Unlock()
	}()
	if err := sendAgent(ctx, c.ws, proto.TypeCollectorJob, j); err != nil {
		return poller.RemoteJobResult{}, err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		if r.Error != "" {
			return r, errors.New(r.Error)
		}
		return r, nil
	case <-t.C:
		return poller.RemoteJobResult{}, errors.New("the remote collector did not answer in time")
	case <-ctx.Done():
		return poller.RemoteJobResult{}, ctx.Err()
	}
}

// collectorJobDone takes a collector's answer (only to a job it was given).
func (s *Server) collectorJobDone(c *agentConn, r poller.RemoteJobResult) {
	collectorJobs.Lock()
	ch, ok := collectorJobs.wait[r.ID]
	if ok && collectorJobs.from[r.ID] != c.deviceID {
		ok = false
	}
	collectorJobs.Unlock()
	if !ok {
		return
	}
	if len(r.Hosts) > discovery.MaxHosts {
		r.Hosts = r.Hosts[:discovery.MaxHosts]
	}
	select {
	case ch <- r:
	default:
	}
}

// remoteRefresh reads a device through its collector (poller.RemoteRefresh).
func (s *Server) remoteRefresh(ctx context.Context, d *store.Device) error {
	if d.SNMPProfile == 0 {
		return nil
	}
	r, err := s.collectorJob(ctx, s.collectorOf(d), poller.RemoteJob{Kind: poller.JobRead, Address: d.Address, Profile: d.SNMPProfile, SNMP: s.snmpProfiles(ctx, d.SNMPProfile)}, 3*time.Minute)
	if err != nil {
		return err
	}
	if r.Read == nil {
		return errors.New("the remote collector read nothing")
	}
	return s.poller.SaveDeviceRead(ctx, d, r.Read)
}

// collectorOf is the collector that runs a device now: its own, or its
// backup while the own one is offline.
func (s *Server) collectorOf(d *store.Device) int64 {
	if d.CollectorID != 0 && s.hub.get(d.CollectorID) == nil && d.BackupID != 0 && s.hub.get(d.BackupID) != nil {
		return d.BackupID
	}
	return d.CollectorID
}

var errNotReady = errors.New("results before the collector got its checks")

// RunCollectorSweep marks the checks of devices whose collector (and its
// backup) is not connected as unknown once they are overdue — also when a
// collector never came back after a console restart, or was assigned while
// it was offline.
func (s *Server) RunCollectorSweep(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.SweepCollectors(ctx)
		}
	}
}

// SweepCollectors is one pass of RunCollectorSweep.
func (s *Server) SweepCollectors(ctx context.Context) {
	cs, err := s.store.RemoteChecks(ctx)
	if err != nil {
		s.log.Warn("collector sweep", "err", err)
		return
	}
	now := s.now()
	var res []store.CheckResult
	for _, c := range cs {
		if !poller.RemoteType(c.Type) || c.Paused || c.DevPaused || c.Status == store.StatusUnknown {
			continue
		}
		if s.hub.get(c.Collector) != nil || (c.Backup != 0 && s.hub.get(c.Backup) != nil) {
			continue
		}
		grace := time.Duration(2*c.IntervalS)*time.Second + time.Minute
		if c.LastRun != nil && now.Sub(*c.LastRun) < grace {
			continue
		}
		res = append(res, store.CheckResult{CheckID: c.ID, At: now, Status: store.StatusUnknown, Text: "the remote collector is offline"})
	}
	if len(res) == 0 {
		return
	}
	if _, err := s.store.SaveResults(ctx, res); err != nil {
		s.log.Warn("collector sweep", "err", err)
	}
}
