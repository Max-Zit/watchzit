package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/coder/websocket"

	"github.com/max-zit/watchzit/internal/poller"
	"github.com/max-zit/watchzit/internal/proto"
)

// Agent runs the channel to the server.
type Agent struct {
	Dir string
	Cfg *Config
	Log *slog.Logger
	// Sample and Collect are replaced in tests.
	Sample  func(ctx context.Context, services []string) *proto.Report
	Collect func(ctx context.Context) *proto.Inventory
	// Procs reports the watched processes (nil: a ProcWatcher).
	Procs func(ctx context.Context, names []string) []proto.Process
	// Events returns new error messages of the system logs (nil: an
	// EventReader).
	Events func(ctx context.Context) []proto.Event

	key           ed25519.PrivateKey
	lastInventory time.Time
	coll          *collectorRun // remote collector (lives across channels)
}

var errUnknownAgent = errors.New("unknown agent")

// Run connects and reconnects until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	key, err := LoadOrCreateKey(a.Dir)
	if err != nil {
		return err
	}
	a.key = key
	cleanUpdates(filepath.Join(a.Dir, "updates"), time.Now())
	if a.Sample == nil {
		s := NewSampler()
		a.Sample = s.Sample
	}
	if a.Collect == nil {
		a.Collect = CollectInventory
	}
	if a.Procs == nil {
		a.Procs = NewProcWatcher().States
	}
	if a.Events == nil {
		a.Events = NewEventReader().Read
	}
	a.coll = newCollectorRun()
	defer a.coll.close()
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := a.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errUnknownAgent) {
			a.Log.Error("the server does not know this agent (device removed?); enroll it again", "err", err)
			backoff = 10 * time.Minute
		} else {
			a.Log.Warn("connection to the server lost", "err", err)
			if time.Since(start) > 5*time.Minute {
				backoff = 2 * time.Second
			}
		}
		// Jitter, so many agents do not reconnect at the same moment.
		wait := backoff + time.Duration(rand.Int64N(int64(backoff/2)+1)) // #nosec G404 -- jitter only
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		if backoff < time.Minute {
			backoff = min(backoff*2, time.Minute)
		}
	}
	return nil
}

func sendMsg(ctx context.Context, ws *websocket.Conn, typ string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(proto.Envelope{Type: typ, Data: raw})
	wctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return ws.Write(wctx, websocket.MessageText, b)
}

// session is one connection: hello, then reports and inventory.
func (a *Agent) session(ctx context.Context) error {
	cert, err := clientCert(a.key)
	if err != nil {
		return err
	}
	hc := httpClient(pinnedTLS(a.Cfg.Fingerprint, cert))
	hc.Timeout = 0 // the channel is long-lived
	dctx, dcancel := context.WithTimeout(ctx, 30*time.Second)
	ws, resp, err := websocket.Dial(dctx, a.Cfg.ServerURL+proto.PathConnect, &websocket.DialOptions{
		HTTPClient: hc, HTTPHeader: http.Header{"User-Agent": {"watchzit-agent/" + Version}},
		CompressionMode: websocket.CompressionContextTakeover,
	})
	dcancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return errUnknownAgent
		}
		return err
	}
	defer ws.CloseNow()
	ws.SetReadLimit(proto.MaxMessageSize)
	host, _ := os.Hostname()
	if err := sendMsg(ctx, ws, proto.TypeHello, proto.Hello{Protocol: proto.Version, AgentVersion: Version, Hostname: host, OS: osName(), BootTime: bootTime(), Arch: runtime.GOARCH}); err != nil {
		return err
	}
	a.Log.Info("connected to the server", "server", a.Cfg.ServerURL)

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	msgs := make(chan proto.Envelope, 8)
	readErr := make(chan error, 1)
	go func() {
		for {
			typ, data, err := ws.Read(sctx)
			if err != nil {
				readErr <- err
				return
			}
			if typ != websocket.MessageText {
				continue
			}
			var env proto.Envelope
			if json.Unmarshal(data, &env) == nil {
				select {
				case msgs <- env:
				case <-sctx.Done():
					return
				}
			}
		}
	}()

	// remote collector: results queued while the channel was down go
	// first; jobs (a discovery scan, an SNMP read) answer through jobDone
	jobDone := make(chan poller.RemoteJobResult, 4)
	// consoles since 1.3.3 acknowledge stored batches (Welcome.Acks); the
	// unacknowledged ones go back to the queue when the channel ends
	acks, welcomed := false, false
	defer a.coll.requeue()
	sendQueued := func() error {
		if !welcomed {
			return nil // the welcome says whether the console acknowledges
		}
		for {
			seq, rs := a.coll.next(collectorBatch, acks)
			if len(rs) == 0 {
				return nil
			}
			if err := sendMsg(sctx, ws, proto.TypeCollectorResults, poller.RemoteResults{Seq: seq, Results: rs}); err != nil {
				if !acks {
					a.coll.putBack(rs)
				}
				return err
			}
		}
	}
	collTick := time.NewTicker(5 * time.Second)
	defer collTick.Stop()
	interval := 60 * time.Second
	var services, processes []string
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	first := time.After(3 * time.Second)
	invEvery := 24 * time.Hour
	invTimer := time.NewTimer(5 * time.Second)
	defer invTimer.Stop()
	report := func() error {
		r := a.Sample(sctx, services)
		if len(processes) > 0 {
			r.Processes = a.Procs(sctx, processes)
		}
		r.Events = a.Events(sctx)
		return sendMsg(sctx, ws, proto.TypeReport, r)
	}
	inventory := func() error {
		cctx, ccancel := context.WithTimeout(sctx, 2*time.Minute)
		inv := a.Collect(cctx)
		ccancel()
		a.lastInventory = time.Now()
		return sendMsg(sctx, ws, proto.TypeInventory, inv)
	}
	for {
		select {
		case <-ctx.Done():
			_ = ws.Close(websocket.StatusNormalClosure, "agent stopping")
			return ctx.Err()
		case err := <-readErr:
			return err
		case env := <-msgs:
			switch env.Type {
			case proto.TypeWelcome:
				var w proto.Welcome
				if json.Unmarshal(env.Data, &w) == nil {
					if w.IntervalSeconds >= 10 && w.IntervalSeconds <= 3600 {
						interval = time.Duration(w.IntervalSeconds) * time.Second
						ticker.Reset(interval)
					}
					services, processes = w.Services, w.Processes
					acks = w.Acks
					welcomed = true
					if first == nil { // a later welcome (watched services changed): report now
						if err := report(); err != nil {
							return err
						}
					}
				}
			case proto.TypeUpdate:
				var u proto.AgentUpdate
				if json.Unmarshal(env.Data, &u) == nil {
					dc := httpClient(pinnedTLS(a.Cfg.Fingerprint, cert))
					dc.Timeout = 15 * time.Minute
					go func() {
						if err := a.selfUpdate(sctx, dc, u); err != nil {
							a.Log.Warn("agent update failed", "version", u.Version, "err", err)
						}
					}()
				}
			case proto.TypeCollectorConfig:
				var cfg poller.RemoteConfig
				if json.Unmarshal(env.Data, &cfg) != nil {
					break
				}
				a.coll.configure(ctx, a, cfg)
				if n := a.coll.waiting(); n > 0 {
					a.Log.Info("sending results kept while the console was away", "count", n)
				}
				if err := sendQueued(); err != nil {
					return err
				}
			case proto.TypeCollectorAck:
				var ack poller.RemoteAck
				if json.Unmarshal(env.Data, &ack) == nil {
					a.coll.ack(ack.Seq)
				}
			case proto.TypeCollectorJob:
				var j poller.RemoteJob
				if json.Unmarshal(env.Data, &j) != nil {
					break
				}
				a.Log.Info("remote collector job", "kind", j.Kind, "range", j.CIDR, "address", j.Address)
				go func() {
					r := poller.RunJob(sctx, j)
					select {
					case jobDone <- r:
					case <-sctx.Done():
					}
				}()
			case proto.TypeInventoryRequest:
				if err := inventory(); err != nil {
					return err
				}
			}
		case <-a.coll.notify:
			if err := sendQueued(); err != nil {
				return err
			}
		case <-collTick.C:
			if err := sendQueued(); err != nil {
				return err
			}
		case r := <-jobDone:
			if err := sendMsg(sctx, ws, proto.TypeCollectorJobDone, r); err != nil {
				return err
			}
		case <-first:
			first = nil
			if err := report(); err != nil {
				return err
			}
		case <-ticker.C:
			if err := report(); err != nil {
				return err
			}
		case <-invTimer.C:
			if time.Since(a.lastInventory) >= invEvery {
				if err := inventory(); err != nil {
					return err
				}
			}
			invTimer.Reset(time.Hour)
		}
	}
}
