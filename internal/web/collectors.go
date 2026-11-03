package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

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
	collectorSync.Lock()
	defer collectorSync.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, c := range s.hub.all() {
		if err := s.sendCollectorConfig(ctx, c); err != nil {
			s.log.Warn("cannot update the remote collector", "device", c.deviceID, "err", err)
		}
	}
}

func (s *Server) sendCollectorConfig(ctx context.Context, c *agentConn) error {
	cfg, allowed, err := s.collectorConfig(ctx, c.deviceID)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(cfg)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	c.coll.mu.Lock()
	defer c.coll.mu.Unlock()
	if hash == c.coll.hash || (c.coll.hash == "" && len(cfg.Checks) == 0) {
		return nil // nothing new; an agent that never collected needs no empty list
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
	now := s.now()
	var res []store.CheckResult
	var samples []tsdb.Sample
	for _, r := range rr.Results {
		series, ok := allowed[r.Result.CheckID]
		if !ok || (r.Result.Status != store.StatusUp && r.Result.Status != store.StatusDown) {
			continue
		}
		// the collector's clock may be off: keep times near the console's
		if at := r.Result.At; at.After(now.Add(time.Minute)) || at.Before(now.Add(-time.Hour)) {
			r.Result.At = now
		}
		if len(r.Result.Text) > 500 {
			r.Result.Text = r.Result.Text[:500]
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
	if err != nil || len(cs) == 0 {
		return
	}
	var res []store.CheckResult
	for _, c := range cs {
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
	id, _ := strconv.ParseInt(r.FormValue("collector"), 10, 64)
	if _, err := s.store.SetDeviceCollector(ctx, []int64{d.ID}, id); err != nil {
		s.redirectErr(w, r, back, storeErr(err))
		return
	}
	s.audit(r, "device.collector", "device:"+strconv.FormatInt(d.ID, 10), "checks of %s run by collector %d (0 = console)", d.Name, id)
	s.poller.Reload()
	s.redirect(w, r, back, "Saved.")
}
