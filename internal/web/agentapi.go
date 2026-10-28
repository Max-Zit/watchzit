package web

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/max-zit/watchzit/internal/proto"
	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/tsdb"
)

// AgentInterval is how often agents report (seconds).
const AgentInterval = 60

// hub keeps the open agent channels.
type hub struct {
	mu    sync.Mutex
	conns map[int64]*agentConn
}

type agentConn struct {
	deviceID int64
	ws       *websocket.Conn
	cancel   context.CancelFunc
	ifsHash  string // the interfaces last stored for the map
	vlanHash string // the VLANs last stored
}

func newHub() *hub { return &hub{conns: map[int64]*agentConn{}} }

func (h *hub) add(c *agentConn) {
	h.mu.Lock()
	old := h.conns[c.deviceID]
	h.conns[c.deviceID] = c
	h.mu.Unlock()
	if old != nil {
		old.cancel()
		_ = old.ws.Close(websocket.StatusPolicyViolation, "replaced by a newer connection")
	}
}

func (h *hub) remove(c *agentConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[c.deviceID] == c {
		delete(h.conns, c.deviceID)
		return true
	}
	return false
}

func (h *hub) get(id int64) *agentConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[id]
}

// disconnectAgent closes the channel of a device (agent removed).
func (s *Server) disconnectAgent(id int64) {
	if c := s.hub.get(id); c != nil {
		c.cancel()
		_ = c.ws.Close(websocket.StatusPolicyViolation, "agent removed")
	}
}

// AgentHandler serves the agent endpoint (its own listener and port).
func (s *Server) AgentHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+proto.PathEnroll, s.handleAgentEnroll)
	mux.HandleFunc("GET "+proto.PathConnect, s.handleAgentConnect)
	mux.HandleFunc("GET /agent/download/{name}", s.handleAgentDownload)
	mux.HandleFunc("GET /agent/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","product":"watchzit"}`))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		s.aclCheck(s.AgentACL, mux).ServeHTTP(w, r)
	})
}

func agentError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(proto.Error{Error: msg})
}

// peerKey is the ed25519 key of the agent's TLS client certificate; the
// handshake proved that the agent holds the private key.
func peerKey(r *http.Request) ed25519.PublicKey {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil
	}
	k, _ := r.TLS.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	return k
}

func (s *Server) handleAgentEnroll(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if until := s.guard.blocked(r.Context(), ip); !until.IsZero() {
		agentError(w, http.StatusTooManyRequests, "too many failed enrollments from this address")
		return
	}
	pub := peerKey(r)
	if pub == nil {
		agentError(w, http.StatusUnauthorized, "an ed25519 client certificate is required")
		return
	}
	var req proto.EnrollRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, proto.MaxEnrollBody)).Decode(&req); err != nil {
		agentError(w, http.StatusBadRequest, "bad request")
		return
	}
	if req.Protocol != proto.Version {
		agentError(w, http.StatusBadRequest, "unsupported protocol version")
		return
	}
	a, err := s.store.EnrollAgent(r.Context(), req.Token, pub, req.Hostname, req.OS, req.AgentVersion, req.Addresses, ip)
	switch {
	case errors.Is(err, store.ErrTokenInvalid):
		s.guard.fail(r.Context(), ip, "", "enrollment token")
		s.log.Warn("agent enrollment refused", "ip", ip, "hostname", req.Hostname)
		agentError(w, http.StatusForbidden, "the enrollment code is invalid, expired or used up")
		return
	case errors.Is(err, store.ErrConflict):
		agentError(w, http.StatusConflict, "this agent key is already enrolled")
		return
	case err != nil:
		s.log.Error("enrollment failed", "err", err)
		agentError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.auditAs(r, "agent", "agent.enroll", "device:"+strconv.FormatInt(a.DeviceID, 10), "%q enrolled as device %q (agent %s, %s)", req.Hostname, a.Device, req.AgentVersion, req.OS)
	s.log.Info("agent enrolled", "device", a.DeviceID, "name", a.Device, "ip", ip)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(proto.EnrollResponse{AgentID: a.AgentID, DeviceID: a.DeviceID, Device: a.Device})
}

func (s *Server) handleAgentConnect(w http.ResponseWriter, r *http.Request) {
	pub := peerKey(r)
	if pub == nil {
		agentError(w, http.StatusUnauthorized, "an ed25519 client certificate is required")
		return
	}
	a, err := s.store.AgentByKey(r.Context(), pub)
	if errors.Is(err, store.ErrNotFound) {
		agentError(w, http.StatusUnauthorized, "unknown agent; enroll it again")
		return
	}
	if err != nil {
		agentError(w, http.StatusInternalServerError, "internal error")
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		return
	}
	ws.SetReadLimit(proto.MaxMessageSize)
	ctx, cancel := context.WithCancel(context.Background())
	c := &agentConn{deviceID: a.DeviceID, ws: ws, cancel: cancel}
	s.hub.add(c)
	ip := clientIP(r)
	defer func() {
		cancel()
		if s.hub.remove(c) {
			s.agentOffline(a.DeviceID)
		}
		_ = ws.CloseNow()
	}()
	s.log.Info("agent connected", "device", a.DeviceID, "name", a.Device, "ip", ip)
	for {
		rctx, rcancel := context.WithTimeout(ctx, 3*AgentInterval*time.Second)
		typ, data, err := ws.Read(rctx)
		rcancel()
		if err != nil {
			s.log.Info("agent disconnected", "device", a.DeviceID, "name", a.Device, "reason", err.Error())
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var env proto.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			_ = ws.Close(websocket.StatusUnsupportedData, "bad message")
			return
		}
		if err := s.agentMessage(ctx, c, env, ip); err != nil {
			s.log.Warn("agent message failed", "device", a.DeviceID, "type", env.Type, "err", err)
		}
	}
}

func sendAgent(ctx context.Context, ws *websocket.Conn, typ string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(proto.Envelope{Type: typ, Data: raw})
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return ws.Write(wctx, websocket.MessageText, b)
}

// welcome tells an agent its interval and the services to watch.
func (s *Server) welcome(ctx context.Context, c *agentConn) error {
	cs, err := s.store.DeviceChecks(ctx, c.deviceID)
	if err != nil {
		return err
	}
	w := proto.Welcome{DeviceID: c.deviceID, IntervalSeconds: AgentInterval}
	for _, x := range cs {
		if x.Type == store.CheckAgentService && !x.Paused {
			w.Services = append(w.Services, x.Params.Descr)
		}
	}
	return sendAgent(ctx, c.ws, proto.TypeWelcome, w)
}

// rewelcome sends a new welcome to a connected agent (watched services changed).
func (s *Server) rewelcome(deviceID int64) {
	if c := s.hub.get(deviceID); c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.welcome(ctx, c); err != nil {
			s.log.Warn("cannot update the agent", "device", deviceID, "err", err)
		}
	}
}

func (s *Server) agentMessage(ctx context.Context, c *agentConn, env proto.Envelope, ip string) error {
	switch env.Type {
	case proto.TypeHello:
		var h proto.Hello
		if err := json.Unmarshal(env.Data, &h); err != nil {
			return err
		}
		if err := s.store.AgentSeen(ctx, c.deviceID, h.AgentVersion, h.OS, ip, true); err != nil {
			return err
		}
		return s.welcome(ctx, c)
	case proto.TypeReport:
		var r proto.Report
		if err := json.Unmarshal(env.Data, &r); err != nil {
			return err
		}
		if err := s.store.AgentSeen(ctx, c.deviceID, "", "", ip, true); err != nil {
			return err
		}
		return s.agentReport(ctx, c, &r)
	case proto.TypeInventory:
		var inv proto.Inventory
		if err := json.Unmarshal(env.Data, &inv); err != nil {
			return err
		}
		return s.store.SetAgentInventory(ctx, c.deviceID, &inv)
	}
	return fmt.Errorf("unknown message type %q", env.Type)
}

// agentOffline marks the agent check down when the channel closes.
func (s *Server) agentOffline(deviceID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = s.store.AgentSeen(ctx, deviceID, "", "", "", false)
	cs, err := s.store.DeviceChecks(ctx, deviceID)
	if err != nil {
		return
	}
	var res []store.CheckResult
	for _, c := range cs {
		if c.Type == store.CheckAgent {
			res = append(res, store.CheckResult{CheckID: c.ID, At: s.now(), Status: store.StatusDown, Text: "agent offline", Values: map[string]float64{}})
		}
	}
	if _, err := s.store.SaveResults(ctx, res); err != nil {
		s.log.Warn("cannot mark the agent offline", "device", deviceID, "err", err)
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func pct(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return round2(float64(used) / float64(total) * 100)
}

// agentChecks are the checks the agent's report fills; missing ones are
// made automatically (interfaces only when they have a link or traffic).
func (s *Server) agentReport(ctx context.Context, c *agentConn, r *proto.Report) error {
	cs, err := s.store.DeviceChecks(ctx, c.deviceID)
	if err != nil {
		return err
	}
	byKey := map[string]*store.Check{}
	key := func(typ string, p store.CheckParams) string {
		switch typ {
		case store.CheckAgentDisk, store.CheckAgentService:
			return typ + "|" + p.Descr
		case store.CheckAgentNet:
			return typ + "|" + strconv.Itoa(p.IfIndex)
		}
		return typ
	}
	for _, x := range cs {
		if store.IsAgent(x.Type) {
			byKey[key(x.Type, x.Params)] = x
		}
	}
	created := false
	ensure := func(typ string, p store.CheckParams, name string) *store.Check {
		if x := byKey[key(typ, p)]; x != nil {
			return x
		}
		id, err := s.store.CreateCheck(ctx, c.deviceID, store.CheckInput{Type: typ, Name: name, Params: p, IntervalS: AgentInterval})
		if err != nil {
			s.log.Warn("cannot add an agent check", "device", c.deviceID, "type", typ, "err", err)
			return nil
		}
		x, err := s.store.CheckByID(ctx, id)
		if err != nil {
			return nil
		}
		byKey[key(typ, p)] = x
		created = true
		return x
	}
	at := s.now()
	var res []store.CheckResult
	var samples []tsdb.Sample
	put := func(x *store.Check, status, text string, vals map[string]float64) {
		if x == nil || x.Paused {
			return
		}
		res = append(res, store.CheckResult{CheckID: x.ID, At: at, Status: status, Text: text, Values: vals})
		for _, se := range x.Series {
			if v, ok := vals[se.Metric]; ok {
				samples = append(samples, tsdb.Sample{Series: se.ID, T: at, V: v})
			}
		}
	}
	up := time.Duration(r.Uptime) * time.Second
	put(ensure(store.CheckAgent, store.CheckParams{}, "Agent"), store.StatusUp,
		fmt.Sprintf("online, up %dd %02dh", int(up.Hours())/24, int(up.Hours())%24), map[string]float64{"uptime_s": math.Round(r.Uptime)})
	cpu := map[string]float64{"cpu_pct": round2(r.CPU)}
	cpuText := fmt.Sprintf("%.0f%%", r.CPU)
	if r.Load1 != nil {
		cpu["load1"] = round2(*r.Load1)
		cpuText += fmt.Sprintf(", load %.2f", *r.Load1)
	}
	put(ensure(store.CheckAgentCPU, store.CheckParams{}, "CPU"), store.StatusUp, cpuText, cpu)
	mem := map[string]float64{"mem_pct": pct(r.MemUsed, r.MemTotal), "mem_used_b": float64(r.MemUsed)}
	memText := fmt.Sprintf("%.0f%% of %s", pct(r.MemUsed, r.MemTotal), bytesText(r.MemTotal))
	if r.SwapTotal > 0 {
		mem["swap_pct"] = pct(r.SwapUsed, r.SwapTotal)
		memText += fmt.Sprintf(", swap %.0f%%", mem["swap_pct"])
	}
	put(ensure(store.CheckAgentMem, store.CheckParams{}, "Memory"), store.StatusUp, memText, mem)
	for i, d := range r.Disks {
		if i >= 64 || d.Total < 64<<20 {
			continue // tiny boot partitions say little
		}
		p := store.CheckParams{Descr: d.Mount}
		put(ensure(store.CheckAgentDisk, p, d.Mount), store.StatusUp, fmt.Sprintf("%.0f%% of %s", pct(d.Used, d.Total), bytesText(d.Total)),
			map[string]float64{"used_pct": pct(d.Used, d.Total), "used_b": float64(d.Used)})
	}
	var ifs []store.Interface
	var vlans []store.VLANPort
	for i, n := range r.Nets {
		if i >= 64 {
			break
		}
		ifs = append(ifs, store.Interface{Index: n.Index, Name: n.Name, Descr: n.Alias, MAC: n.MAC, Speed: n.Speed, AdminUp: true, OperUp: n.Up})
		for j, id := range n.VLANs {
			if j < 64 {
				vlans = append(vlans, store.VLANPort{VLAN: id, IfIndex: n.Index, Tagged: true})
			}
		}
		x := byKey[key(store.CheckAgentNet, store.CheckParams{IfIndex: n.Index})]
		if x == nil && (n.Up || (n.InBps != nil && *n.InBps > 0)) && n.MAC != "" {
			x = ensure(store.CheckAgentNet, store.CheckParams{IfIndex: n.Index, IfName: n.Name}, n.Name)
		}
		vals := map[string]float64{}
		text := "link down"
		status := store.StatusDown
		if n.Up {
			status, text = store.StatusUp, "up"
			if n.Speed > 0 {
				text += ", " + speedText(n.Speed)
			}
		}
		if n.InBps != nil && n.OutBps != nil {
			vals["in_bps"], vals["out_bps"] = round2(*n.InBps), round2(*n.OutBps)
			text += " · ↓ " + bitsText(*n.InBps) + "  ↑ " + bitsText(*n.OutBps)
		}
		if n.InErr != nil && n.OutErr != nil {
			vals["in_err"], vals["out_err"] = round2(*n.InErr), round2(*n.OutErr)
		}
		if len(vals) == 0 {
			vals = nil // first report after a start: keep the last values
		}
		put(x, status, text, vals)
	}
	for _, sv := range r.Services {
		x := byKey[key(store.CheckAgentService, store.CheckParams{Descr: sv.Name})]
		status, v := store.StatusDown, 0.0
		if sv.Running {
			status, v = store.StatusUp, 1
		}
		put(x, status, sv.State, map[string]float64{"running": v})
	}
	if _, err := s.store.SaveResults(ctx, res); err != nil {
		return err
	}
	s.tsdb.Add(samples...)
	// the map links ports through the interfaces
	sort.Slice(ifs, func(i, j int) bool { return ifs[i].Index < ifs[j].Index })
	h, _ := json.Marshal(ifs)
	if string(h) != c.ifsHash {
		if err := s.store.SetInterfaces(ctx, c.deviceID, ifs); err != nil {
			return err
		}
		c.ifsHash = string(h)
	}
	if v, _ := json.Marshal(vlans); string(v) != c.vlanHash {
		if err := s.store.SetDeviceVLANs(ctx, c.deviceID, store.VLANFromAgent, vlans); err != nil {
			return err
		}
		c.vlanHash = string(v)
	}
	if created {
		s.log.Info("agent checks added", "device", c.deviceID)
	}
	return nil
}

func bytesText(b uint64) string {
	u := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	v := float64(b)
	i := 0
	for v >= 1024 && i < len(u)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, u[i])
}

func speedText(bps int64) string {
	switch {
	case bps >= 1e9 && bps%1e9 == 0:
		return fmt.Sprintf("%d Gbit/s", bps/1e9)
	case bps >= 1e6:
		return fmt.Sprintf("%d Mbit/s", bps/1e6)
	}
	return fmt.Sprintf("%d kbit/s", bps/1e3)
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
