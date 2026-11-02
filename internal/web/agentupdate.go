package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

// Agent self-update: an agent older than the server gets the agent of the
// server's version (the installers the server package carries). Automatic
// when an agent connects (at most once an hour per computer), or for all
// online agents from the Agents page.

const settingAgentUpdates = "agent_updates"

// AgentUpdateSettings switch the automatic agent updates.
type AgentUpdateSettings struct {
	Manual bool `json:"manual"` // automatic updates are off
}

func (s *Server) agentUpdateSettings(ctx context.Context) AgentUpdateSettings {
	var a AgentUpdateSettings
	_ = s.store.GetSetting(ctx, settingAgentUpdates, &a)
	return a
}

var agentOffers = struct {
	sync.Mutex
	at map[int64]time.Time
}{at: map[int64]time.Time{}}

// agentPackage is the installer for an agent's system ("" when the server
// has none or cannot tell).
func (s *Server) agentPackage(osName, arch string) (agentFile, bool) {
	files := s.agentDownloads()
	switch {
	case strings.Contains(strings.ToLower(osName), "windows"):
		f, ok := files["windows"]
		return f, ok
	case arch == "amd64" || arch == "arm64":
		f, ok := files["linux-"+arch]
		return f, ok
	}
	return agentFile{}, false
}

// agentOutdated tells whether an agent is older than the server's agent.
func (s *Server) agentOutdated(version string) bool {
	return proto.ValidVersion(s.Version) && version != "" && proto.CompareVersions(s.Version, version) > 0
}

// offerAgentUpdate sends the update to an outdated agent; force skips the
// setting and the once-an-hour limit.
func (s *Server) offerAgentUpdate(ctx context.Context, c *agentConn, force bool) bool {
	if !s.agentOutdated(c.version) || (!force && s.agentUpdateSettings(ctx).Manual) {
		return false
	}
	f, ok := s.agentPackage(c.os, c.arch)
	if !ok {
		return false
	}
	agentOffers.Lock()
	last := agentOffers.at[c.deviceID]
	if !force && time.Since(last) < time.Hour {
		agentOffers.Unlock()
		return false
	}
	agentOffers.at[c.deviceID] = time.Now()
	agentOffers.Unlock()
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := sendAgent(sctx, c.ws, proto.TypeUpdate, proto.AgentUpdate{Version: s.Version, Name: f.Name, SHA256: f.SHA256, Size: f.Size}); err != nil {
		return false
	}
	by := "system"
	if force {
		by = "admin"
	}
	_ = s.store.Audit(ctx, by, "", "agent.update", "device:"+strconv.FormatInt(c.deviceID, 10), "agent "+c.version+" asked to update to "+s.Version+" ("+f.Name+")")
	return true
}

func (s *Server) handleAgentUpdates(w http.ResponseWriter, r *http.Request) {
	const back = "/agents#updates"
	if r.FormValue("do") == "now" {
		n := 0
		for _, c := range s.hub.all() {
			if s.offerAgentUpdate(r.Context(), c, true) {
				n++
			}
		}
		s.redirect(w, r, back, translate(s.lang(r, userFrom(r)), "%d agents are updating.", n))
		return
	}
	a := AgentUpdateSettings{Manual: r.FormValue("auto") != "1"}
	if err := s.store.SetSetting(r.Context(), settingAgentUpdates, a); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "settings.agent-updates", "settings", "automatic agent updates: %v", !a.Manual)
	s.redirect(w, r, back, "Saved.")
}

// all lists the connected agents.
func (h *hub) all() []*agentConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*agentConn, 0, len(h.conns))
	for _, c := range h.conns {
		out = append(out, c)
	}
	return out
}
