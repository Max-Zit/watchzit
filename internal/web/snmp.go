package web

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/probe"
	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/snmpmon"
	"github.com/max-zit/watchzit/internal/store"
)

// ---- SNMP profiles (Settings → SNMP)

type snmpData struct {
	Profiles  []*store.SNMPProfile
	Edit      *store.SNMPProfile // the profile being edited
	In        store.SNMPProfileInput
	AuthProts []string
	PrivProts []string
}

func (s *Server) handleSNMPProfiles(w http.ResponseWriter, r *http.Request) {
	s.showSNMP(w, r, http.StatusOK, nil, store.SNMPProfileInput{Version: 2, Port: 161}, "")
}

func (s *Server) showSNMP(w http.ResponseWriter, r *http.Request, status int, edit *store.SNMPProfile, in store.SNMPProfileInput, errMsg string) {
	ps, err := s.store.SNMPProfiles(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// passwords never go back into the form
	in.Community, in.AuthPass, in.PrivPass = "", "", ""
	p := &Page{Title: "SNMP", Nav: "settings", Data: &snmpData{Profiles: ps, Edit: edit, In: in, AuthProts: snmp.AuthProtocols, PrivProts: snmp.PrivProtocols}}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "snmp", p)
}

func snmpInput(r *http.Request) store.SNMPProfileInput {
	return store.SNMPProfileInput{Name: r.FormValue("name"), Version: formInt(r, "version"), Port: formInt(r, "port"), Community: r.FormValue("community"),
		User: r.FormValue("user"), AuthProto: r.FormValue("auth_proto"), AuthPass: r.FormValue("auth_pass"),
		PrivProto: r.FormValue("priv_proto"), PrivPass: r.FormValue("priv_pass"), Context: r.FormValue("context")}
}

func (s *Server) handleSNMPCreate(w http.ResponseWriter, r *http.Request) {
	in := snmpInput(r)
	id, err := s.store.CreateSNMPProfile(r.Context(), in)
	if err != nil {
		s.showSNMP(w, r, http.StatusBadRequest, nil, in, storeErr(err))
		return
	}
	s.poller.Reload()
	s.audit(r, "snmp.create", "snmp:"+strconv.FormatInt(id, 10), "SNMP profile %q (v%d) added", strings.TrimSpace(in.Name), in.Version)
	s.redirect(w, r, "/settings/snmp", "SNMP profile added.")
}

func (s *Server) loadProfile(w http.ResponseWriter, r *http.Request) *store.SNMPProfile {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return nil
	}
	p, err := s.store.SNMPProfileByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	return p
}

func profileInput(p *store.SNMPProfile) store.SNMPProfileInput {
	return store.SNMPProfileInput{Name: p.Name, Version: p.Version, Port: p.Port, User: p.User, AuthProto: p.AuthProto, PrivProto: p.PrivProto, Context: p.Context}
}

func (s *Server) handleSNMPProfile(w http.ResponseWriter, r *http.Request) {
	if p := s.loadProfile(w, r); p != nil {
		s.showSNMP(w, r, http.StatusOK, p, profileInput(p), "")
	}
}

func (s *Server) handleSNMPUpdate(w http.ResponseWriter, r *http.Request) {
	p := s.loadProfile(w, r)
	if p == nil {
		return
	}
	in := snmpInput(r)
	if err := s.store.UpdateSNMPProfile(r.Context(), p.ID, in); err != nil {
		s.showSNMP(w, r, http.StatusBadRequest, p, in, storeErr(err))
		return
	}
	s.poller.Reload()
	changed := []string{}
	if in.Community != "" {
		changed = append(changed, "community")
	}
	if in.AuthPass != "" {
		changed = append(changed, "authentication password")
	}
	if in.PrivPass != "" {
		changed = append(changed, "privacy password")
	}
	s.audit(r, "snmp.update", "snmp:"+strconv.FormatInt(p.ID, 10), "SNMP profile %q saved (v%d); new secrets: %s", strings.TrimSpace(in.Name), in.Version, strings.Join(append(changed, "none")[:max(1, len(changed))], ", "))
	s.redirect(w, r, "/settings/snmp", "Saved.")
}

func (s *Server) handleSNMPDelete(w http.ResponseWriter, r *http.Request) {
	p := s.loadProfile(w, r)
	if p == nil {
		return
	}
	if err := s.store.DeleteSNMPProfile(r.Context(), p.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.poller.Reload()
	s.audit(r, "snmp.delete", "snmp:"+strconv.FormatInt(p.ID, 10), "SNMP profile %q deleted (%d devices lose SNMP)", p.Name, p.Devices)
	s.redirect(w, r, "/settings/snmp", "SNMP profile deleted.")
}

// ---- discovery: what can be monitored on a device

type discoverData struct {
	D         *store.Device
	Profile   *store.SNMPProfile
	Facts     store.Facts
	Proposals []proposalRow
	Err       string
	Took      time.Duration
}

type proposalRow struct {
	snmpmon.Proposal
	Key    string // form value
	Name   string
	Exists bool // the device already has this check
}

// proposalKey encodes a proposal for the form ("snmp_if:3:<name>").
func proposalKey(c store.CheckInput) string {
	idx, name := 0, ""
	switch c.Type {
	case store.CheckSNMPIf:
		idx, name = c.Params.IfIndex, c.Params.IfName
	case store.CheckSNMPStor:
		idx, name = c.Params.Index, c.Params.Descr
	}
	return c.Type + ":" + strconv.Itoa(idx) + ":" + base64.RawURLEncoding.EncodeToString([]byte(name))
}

func parseProposalKey(k string) (store.CheckInput, bool) {
	parts := strings.SplitN(k, ":", 3)
	if len(parts) != 3 {
		return store.CheckInput{}, false
	}
	idx, err := strconv.Atoi(parts[1])
	name, err2 := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || err2 != nil || idx < 0 {
		return store.CheckInput{}, false
	}
	in := store.CheckInput{Type: parts[0]}
	switch in.Type {
	case store.CheckSNMP, store.CheckSNMPCPU:
	case store.CheckSNMPIf:
		in.Params = store.CheckParams{IfIndex: idx, IfName: string(name)}
	case store.CheckSNMPStor:
		in.Params = store.CheckParams{Index: idx, Descr: string(name)}
	default:
		return in, false
	}
	return in, true
}

// sameCheck tells whether a device already has the check a proposal makes.
func sameCheck(c *store.Check, in store.CheckInput) bool {
	if c.Type != in.Type {
		return false
	}
	switch c.Type {
	case store.CheckSNMPIf:
		return c.Params.IfIndex == in.Params.IfIndex
	case store.CheckSNMPStor:
		return c.Params.Descr == in.Params.Descr
	}
	return true
}

func (s *Server) discover(ctx context.Context, d *store.Device, pr *store.SNMPProfile) (store.Facts, []store.Interface, []snmpmon.Proposal, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	addr, err := probe.Resolve(ctx, d.Address)
	if err != nil {
		return store.Facts{}, nil, nil, err
	}
	cfg, err := s.store.SNMPConfig(pr)
	if err != nil {
		return store.Facts{}, nil, nil, err
	}
	cfg.Timeout = 3 * time.Second
	snmp.Forget(netip.AddrPortFrom(addr, uint16(pr.Port)))              // #nosec G115 -- port is 1–65535; fresh engine data for a test
	c, err := snmp.Dial(netip.AddrPortFrom(addr, uint16(pr.Port)), cfg) // #nosec G115 -- as above
	if err != nil {
		return store.Facts{}, nil, nil, err
	}
	defer c.Close()
	return snmpmon.Discover(ctx, c)
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	d := s.loadDevice(w, r)
	if d == nil {
		return
	}
	ctx := r.Context()
	data := &discoverData{D: d}
	p := &Page{Title: "SNMP discovery", Nav: "devices", Data: data}
	if d.SNMPProfile == 0 {
		data.Err = translate(s.lang(r, userFrom(r)), "The device has no SNMP profile. Choose one in Edit.")
		s.render(w, r, http.StatusOK, "discover", p)
		return
	}
	var err error
	if data.Profile, err = s.store.DeviceSNMP(ctx, d.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	start := time.Now()
	facts, ifs, props, err := s.discover(ctx, d, data.Profile)
	data.Took = time.Since(start).Round(time.Millisecond)
	if err != nil && len(props) == 0 {
		data.Err = err.Error()
		s.render(w, r, http.StatusOK, "discover", p)
		return
	}
	facts.Updated = s.now()
	data.Facts = facts
	if err := s.store.SetFacts(ctx, d.ID, facts); err != nil {
		s.serverError(w, r, err)
		return
	}
	if ifs != nil {
		if err := s.store.SetInterfaces(ctx, d.ID, ifs); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	existing, err := s.store.DeviceChecks(ctx, d.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, pr := range props {
		row := proposalRow{Proposal: pr, Key: proposalKey(pr.Check)}
		in := pr.Check
		_ = in.Clean()
		row.Name = in.Name
		for _, c := range existing {
			row.Exists = row.Exists || sameCheck(c, pr.Check)
		}
		data.Proposals = append(data.Proposals, row)
	}
	s.render(w, r, http.StatusOK, "discover", p)
}

func (s *Server) handleDiscoverSave(w http.ResponseWriter, r *http.Request) {
	d := s.loadDevice(w, r)
	if d == nil {
		return
	}
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.redirectErr(w, r, "/devices/"+strconv.FormatInt(d.ID, 10)+"/discover", "The form could not be read.")
		return
	}
	existing, err := s.store.DeviceChecks(ctx, d.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	interval := formInt(r, "interval")
	var added []string
	for _, k := range r.Form["add"] {
		in, ok := parseProposalKey(k)
		if !ok || len(added) >= 500 {
			continue
		}
		dup := false
		for _, c := range existing {
			dup = dup || sameCheck(c, in)
		}
		if dup {
			continue
		}
		in.IntervalS = interval
		if _, err := s.store.CreateCheck(ctx, d.ID, in); err != nil {
			s.redirectErr(w, r, "/devices/"+strconv.FormatInt(d.ID, 10)+"/discover", storeErr(err))
			return
		}
		added = append(added, in.Type+" "+in.Params.IfName+in.Params.Descr)
	}
	s.poller.Reload()
	if len(added) > 0 {
		s.audit(r, "check.create", "device:"+strconv.FormatInt(d.ID, 10), "%d SNMP checks added to %s: %s", len(added), d.Name, truncateList(added, 20))
	}
	s.redirect(w, r, "/devices/"+strconv.FormatInt(d.ID, 10), "SNMP checks added. Traffic is shown after the second poll.")
}

func truncateList(xs []string, n int) string {
	for i := range xs {
		xs[i] = strings.TrimSpace(xs[i])
	}
	if len(xs) <= n {
		return strings.Join(xs, ", ")
	}
	return strings.Join(xs[:n], ", ") + ", …"
}
