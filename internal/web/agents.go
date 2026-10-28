package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
	"github.com/max-zit/watchzit/internal/store"
)

// Agent installers the console offers (files in the downloads folder).
var agentFiles = []struct{ Name, Platform string }{
	{"watchzit-agent-x64.msi", "windows"},
	{"watchzit-agent-windows-amd64.exe", "windows-exe"},
	{"watchzit-agent-linux-amd64", "linux-amd64"},
	{"watchzit-agent-linux-arm64", "linux-arm64"},
}

// agentFile is an installer that is present, with its checksum.
type agentFile struct {
	Name, Platform, SHA256 string
	Size                   int64
}

var sumCache = struct {
	sync.Mutex
	m map[string]string // path|mtime|size → sha256
}{m: map[string]string{}}

func (s *Server) agentDownloads() map[string]agentFile {
	out := map[string]agentFile{}
	if s.DownloadsDir == "" {
		return out
	}
	for _, f := range agentFiles {
		p := filepath.Join(s.DownloadsDir, f.Name)
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			continue
		}
		k := p + "|" + st.ModTime().String() + "|" + strconv.FormatInt(st.Size(), 10)
		sumCache.Lock()
		sum := sumCache.m[k]
		sumCache.Unlock()
		if sum == "" {
			fh, err := os.Open(p) // #nosec G304 -- fixed names in the downloads folder
			if err != nil {
				continue
			}
			h := sha256.New()
			_, err = io.Copy(h, fh)
			_ = fh.Close()
			if err != nil {
				continue
			}
			sum = hex.EncodeToString(h.Sum(nil))
			sumCache.Lock()
			sumCache.m[k] = sum
			sumCache.Unlock()
		}
		out[f.Platform] = agentFile{Name: f.Name, Platform: f.Platform, SHA256: sum, Size: st.Size()}
	}
	return out
}

// serveDownload sends an installer by its fixed name.
func (s *Server) serveDownload(w http.ResponseWriter, r *http.Request, name string) {
	for _, f := range agentFiles {
		if f.Name == name && s.DownloadsDir != "" {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
			http.ServeFile(w, r, filepath.Join(s.DownloadsDir, name))
			return
		}
	}
	http.NotFound(w, r)
}

// handleDownload serves installers in the console (signed in).
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	s.serveDownload(w, r, r.PathValue("name"))
}

// handleAgentDownload serves installers on the agent port without signing
// in, for servers without a browser; the console shows the checksum to
// verify them.
func (s *Server) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	s.serveDownload(w, r, r.PathValue("name"))
}

// agentURL is the address agents use: the configured public URL, or the
// console's host name with the agent port.
func (s *Server) agentURL(r *http.Request) string {
	n, err := s.Network(r.Context())
	if err == nil && n.PublicAgentURL != "" {
		return n.PublicAgentURL
	}
	listen := n.AgentListen
	if s.RunningAgent != "" {
		listen = s.RunningAgent
	}
	_, port, _ := splitListen(listen)
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return "https://" + net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
}

type agentsData struct {
	Tokens     []*store.EnrollToken
	Agents     []*store.AgentInfo
	Groups     []store.Group
	Now        time.Time
	Code       string // a new enrollment code, shown once
	CodeName   string
	AgentURL   string
	AgentLocal bool
	Files      map[string]agentFile
	Name       string
	GroupID    int64
	Days       int
	MaxUses    int
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	s.showAgents(w, r, http.StatusOK, &agentsData{Days: 30}, "")
}

func (s *Server) showAgents(w http.ResponseWriter, r *http.Request, status int, d *agentsData, errMsg string) {
	ctx := r.Context()
	var err error
	if d.Tokens, err = s.store.EnrollTokens(ctx); err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Agents, err = s.store.Agents(ctx); err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Groups, err = s.store.Groups(ctx); err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Now, d.AgentURL, d.Files = s.now(), s.agentURL(r), s.agentDownloads()
	if h, _, err := net.SplitHostPort(strings.TrimPrefix(d.AgentURL, "https://")); err == nil {
		h = strings.Trim(h, "[]")
		ip := net.ParseIP(h)
		d.AgentLocal = h == "localhost" || (ip != nil && ip.IsLoopback())
	}
	p := &Page{Title: "Agents", Nav: "agents", Data: d}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "agents", p)
}

func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	g, _ := strconv.ParseInt(r.FormValue("group"), 10, 64)
	d := &agentsData{Name: r.FormValue("name"), GroupID: g, Days: formInt(r, "days"), MaxUses: formInt(r, "max_uses")}
	u := userFrom(r)
	id, token, err := s.store.CreateEnrollToken(r.Context(), d.Name, g, d.Days, d.MaxUses, u.Username)
	if err != nil {
		s.showAgents(w, r, http.StatusBadRequest, d, storeErr(err))
		return
	}
	s.audit(r, "agent.token.create", "token:"+strconv.FormatInt(id, 10), "enrollment code %q made (valid %d days, up to %d computers; 0 = no limit)", strings.TrimSpace(d.Name), d.Days, d.MaxUses)
	s.showAgents(w, r, http.StatusOK, &agentsData{Days: 30, CodeName: strings.TrimSpace(d.Name),
		Code: proto.MakeEnrollCode(proto.EnrollData{Server: s.agentURL(r), Fingerprint: s.AgentFingerprint, Token: token})}, "")
}

func (s *Server) handleTokenRevoke(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	if err := s.store.RevokeEnrollToken(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r)
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "agent.token.revoke", "token:"+strconv.FormatInt(id, 10), "enrollment code revoked")
	s.redirect(w, r, "/agents", "Enrollment code revoked. Enrolled computers keep working.")
}

// ---- the agent on a device page

// handleAgentServices sets the services the agent watches.
func (s *Server) handleAgentServices(w http.ResponseWriter, r *http.Request) {
	dev := s.loadDevice(w, r)
	if dev == nil {
		return
	}
	ctx := r.Context()
	a, err := s.store.DeviceAgent(ctx, dev.ID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	_ = r.ParseForm()
	want := map[string]bool{}
	known := map[string]string{}
	for _, sv := range a.Inventory.Services {
		known[sv.Name] = sv.Display
	}
	for _, n := range r.Form["service"] {
		if _, ok := known[n]; ok && len(want) < 200 {
			want[n] = true
		}
	}
	cs, err := s.store.DeviceChecks(ctx, dev.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var added, removed []string
	for _, c := range cs {
		if c.Type != store.CheckAgentService {
			continue
		}
		if want[c.Params.Descr] {
			delete(want, c.Params.Descr)
			continue
		}
		ids, err := s.store.DeleteCheck(ctx, c.ID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		_ = s.tsdb.Forget(ctx, ids)
		removed = append(removed, c.Params.Descr)
	}
	for n := range want {
		name := known[n]
		if name == "" {
			name = n
		}
		if _, err := s.store.CreateCheck(ctx, dev.ID, store.CheckInput{Type: store.CheckAgentService, Name: name, Params: store.CheckParams{Descr: n}, IntervalS: AgentInterval}); err != nil {
			s.redirectErr(w, r, "/devices/"+strconv.FormatInt(dev.ID, 10), storeErr(err))
			return
		}
		added = append(added, n)
	}
	go s.rewelcome(dev.ID) // #nosec G118 -- outlives the request on purpose; rewelcome has its own timeout
	s.audit(r, "agent.services", "device:"+strconv.FormatInt(dev.ID, 10), "watched services of %s: added %v, removed %v", dev.Name, added, removed)
	s.redirect(w, r, "/devices/"+strconv.FormatInt(dev.ID, 10), "Saved. The agent reports the services from its next report.")
}

// handleAgentRemove forgets the agent of a device; the computer must
// enroll again. Its data stays.
func (s *Server) handleAgentRemove(w http.ResponseWriter, r *http.Request) {
	dev := s.loadDevice(w, r)
	if dev == nil {
		return
	}
	if err := s.store.ForgetAgent(r.Context(), dev.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.disconnectAgent(dev.ID)
	s.audit(r, "agent.remove", "device:"+strconv.FormatInt(dev.ID, 10), "agent of %s removed; it must enroll again", dev.Name)
	s.redirect(w, r, "/devices/"+strconv.FormatInt(dev.ID, 10), "Agent removed. To use it again, enroll the computer with a new code.")
}

// requestInventory asks a connected agent for a fresh inventory.
func (s *Server) requestInventory(id int64) bool {
	c := s.hub.get(id)
	if c == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return sendAgent(ctx, c.ws, proto.TypeInventoryRequest, nil) == nil
}

func (s *Server) handleAgentRefresh(w http.ResponseWriter, r *http.Request) {
	dev := s.loadDevice(w, r)
	if dev == nil {
		return
	}
	if !s.requestInventory(dev.ID) {
		s.redirectErr(w, r, "/devices/"+strconv.FormatInt(dev.ID, 10), "The agent is offline.")
		return
	}
	time.Sleep(2 * time.Second) // most agents answer by then
	s.redirect(w, r, "/devices/"+strconv.FormatInt(dev.ID, 10), "The agent sends its inventory again.")
}
