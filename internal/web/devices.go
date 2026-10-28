package web

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// ---- overview

type overviewData struct {
	Counts   map[string]int
	Total    int
	Problems []*store.Device
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, err := s.store.StatusCounts(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := &overviewData{Counts: c}
	for _, n := range c {
		d.Total += n
	}
	all, err := s.store.Devices(ctx, store.DeviceFilter{})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, x := range all {
		if x.Status == store.StatusDown || x.Status == store.StatusWarning {
			d.Problems = append(d.Problems, x)
		}
	}
	s.render(w, r, http.StatusOK, "overview", &Page{Title: "Overview", Nav: "overview", Data: d})
}

// ---- device list

type devicesData struct {
	Devices []*store.Device
	Groups  []store.Group
	Q       string
	GroupID int64
	Status  string
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := &devicesData{Q: strings.TrimSpace(r.FormValue("q")), Status: r.FormValue("status")}
	d.GroupID, _ = strconv.ParseInt(r.FormValue("group"), 10, 64)
	if len(d.Q) > 100 {
		d.Q = d.Q[:100]
	}
	switch d.Status {
	case store.StatusUp, store.StatusDown, store.StatusWarning, store.StatusUnknown, store.StatusPaused:
	default:
		d.Status = ""
	}
	var err error
	if d.Devices, err = s.store.Devices(ctx, store.DeviceFilter{Query: d.Q, GroupID: d.GroupID, Status: d.Status}); err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Groups, err = s.store.Groups(ctx); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "devices", &Page{Title: "Devices", Nav: "devices", Data: d})
}

// ---- device page

type deviceData struct {
	D          *store.Device
	Checks     []*store.Check
	Graphs     []graphDef
	Range      string
	Ranges     []string
	New        store.CheckInput // the add-check form after an error
	Facts      store.Facts
	Profile    *store.SNMPProfile
	Interfaces []store.Interface
	Traffic    map[int]*store.Check // interface checks by ifIndex
	Agent      *store.AgentInfo
	Watched    map[string]bool // services the agent reports
	CheckTypes []string
}

// graphDef is one graph of the device page: the series of a check that
// share a unit are drawn together (traffic in and out).
type graphDef struct {
	Check  *store.Check
	Title  string // metric name, translated in the template
	Unit   string
	Series []store.Series
}

// hiddenMetrics are kept but not drawn (they say little as a line).
var hiddenMetrics = map[string]bool{"uptime_s": true, "used_b": true, "in_err": true, "out_err": true, "mem_used_b": true, "running": true}

// unitTitles name graphs that hold several metrics.
var unitTitles = map[string]string{"bit/s": "Traffic", "/s": "Errors"}

func graphsOf(cs []*store.Check) []graphDef {
	var out []graphDef
	for _, c := range cs {
		byUnit := map[string]int{}
		for _, s := range c.Series {
			if hiddenMetrics[s.Metric] {
				continue
			}
			if i, ok := byUnit[s.Unit]; ok && unitTitles[s.Unit] != "" {
				out[i].Series = append(out[i].Series, s)
				out[i].Title = unitTitles[s.Unit]
				continue
			}
			byUnit[s.Unit] = len(out)
			out = append(out, graphDef{Check: c, Title: metricNames[s.Metric], Unit: s.Unit, Series: []store.Series{s}})
		}
	}
	return out
}

// graphRanges are the time ranges of the device graphs.
var graphRanges = []string{"1h", "6h", "24h", "7d", "30d", "90d", "1y"}

func rangeDuration(r string) time.Duration {
	switch r {
	case "1h":
		return time.Hour
	case "6h":
		return 6 * time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	case "90d":
		return 90 * 24 * time.Hour
	case "1y":
		return 365 * 24 * time.Hour
	}
	return 24 * time.Hour
}

func (s *Server) loadDevice(w http.ResponseWriter, r *http.Request) *store.Device {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return nil
	}
	d, err := s.store.DeviceByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	return d
}

func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	if d := s.loadDevice(w, r); d != nil {
		s.showDevice(w, r, http.StatusOK, d, store.CheckInput{Type: store.CheckTCP}, "")
	}
}

func (s *Server) showDevice(w http.ResponseWriter, r *http.Request, status int, d *store.Device, in store.CheckInput, errMsg string) {
	cs, err := s.store.DeviceChecks(r.Context(), d.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rg := r.FormValue("range")
	ok := false
	for _, x := range graphRanges {
		ok = ok || x == rg
	}
	if !ok {
		rg = "24h"
	}
	data := &deviceData{D: d, Checks: cs, Graphs: graphsOf(cs), Range: rg, Ranges: graphRanges, New: in, CheckTypes: store.CheckTypes, Traffic: map[int]*store.Check{}}
	if d.SNMPProfile != 0 {
		if data.Profile, err = s.store.DeviceSNMP(r.Context(), d.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.serverError(w, r, err)
			return
		}
	}
	if data.Facts, err = s.store.DeviceFacts(r.Context(), d.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if data.Interfaces, err = s.store.Interfaces(r.Context(), d.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if a, err := s.store.DeviceAgent(r.Context(), d.ID); err == nil {
		a.Online = s.hub.get(d.ID) != nil
		data.Agent = a
	}
	data.Watched = map[string]bool{}
	for _, c := range cs {
		if c.Type == store.CheckSNMPIf {
			data.Traffic[c.Params.IfIndex] = c
		}
		if c.Type == store.CheckAgentNet {
			data.Traffic[c.Params.IfIndex] = c
		}
		if c.Type == store.CheckAgentService {
			data.Watched[c.Params.Descr] = true
		}
	}
	p := &Page{Title: d.Name, Nav: "devices", Data: data}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "device", p)
}

// ---- device form

type deviceFormData struct {
	ID       int64
	In       store.DeviceInput
	Groups   []store.Group
	Profiles []*store.SNMPProfile
	// New devices get a ping check and optional TCP ports.
	Ping  bool
	Ports string
}

func (s *Server) handleDeviceForm(w http.ResponseWriter, r *http.Request) {
	d := &deviceFormData{In: store.DeviceInput{Kind: "other"}, Ping: true}
	if g, err := strconv.ParseInt(r.FormValue("group"), 10, 64); err == nil {
		d.In.GroupID = g
	}
	if r.PathValue("id") != "" {
		dev := s.loadDevice(w, r)
		if dev == nil {
			return
		}
		d.ID = dev.ID
		d.In = store.DeviceInput{GroupID: deref(dev.GroupID), Name: dev.Name, Address: dev.Address, Kind: dev.Kind, WebURL: dev.WebURL, Notes: dev.Notes, Paused: dev.Paused, SNMP: dev.SNMPProfile}
	}
	s.showDeviceForm(w, r, http.StatusOK, d, "")
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func (s *Server) showDeviceForm(w http.ResponseWriter, r *http.Request, status int, d *deviceFormData, errMsg string) {
	var err error
	if d.Groups, err = s.store.Groups(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Profiles, err = s.store.SNMPProfiles(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	title := "Add a device"
	if d.ID != 0 {
		title = "Edit device"
	}
	p := &Page{Title: title, Nav: "devices", Data: d}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "deviceform", p)
}

func deviceInput(r *http.Request) store.DeviceInput {
	g, _ := strconv.ParseInt(r.FormValue("group"), 10, 64)
	sp, _ := strconv.ParseInt(r.FormValue("snmp"), 10, 64)
	return store.DeviceInput{GroupID: g, SNMP: sp, Name: r.FormValue("name"), Address: r.FormValue("address"), Kind: r.FormValue("kind"),
		WebURL: r.FormValue("web_url"), Notes: r.FormValue("notes"), Paused: r.FormValue("paused") == "1"}
}

// parsePorts reads "22, 80 443" into ports.
func parsePorts(s string) ([]int, error) {
	var out []int
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		p, err := strconv.Atoi(f)
		if err != nil || p < 1 || p > 65535 {
			return nil, errors.New("the ports must be numbers 1–65535, separated by commas")
		}
		out = append(out, p)
	}
	if len(out) > 20 {
		return nil, errors.New("at most 20 ports at once")
	}
	return out, nil
}

func (s *Server) handleDeviceCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := &deviceFormData{In: deviceInput(r), Ping: r.FormValue("ping") == "1", Ports: r.FormValue("ports")}
	ports, err := parsePorts(d.Ports)
	if err != nil {
		s.showDeviceForm(w, r, http.StatusBadRequest, d, err.Error())
		return
	}
	id, err := s.store.CreateDevice(ctx, d.In)
	if err != nil {
		s.showDeviceForm(w, r, http.StatusBadRequest, d, storeErr(err))
		return
	}
	var checks []string
	if d.Ping {
		if _, err := s.store.CreateCheck(ctx, id, store.CheckInput{Type: store.CheckICMP}); err != nil {
			s.serverError(w, r, err)
			return
		}
		checks = append(checks, "ping")
	}
	for _, p := range ports {
		if _, err := s.store.CreateCheck(ctx, id, store.CheckInput{Type: store.CheckTCP, Params: store.CheckParams{Port: p}}); err != nil {
			s.serverError(w, r, err)
			return
		}
		checks = append(checks, "tcp "+strconv.Itoa(p))
	}
	s.poller.Reload()
	s.audit(r, "device.create", "device:"+strconv.FormatInt(id, 10), "added %s (%s), checks: %s", d.In.Name, d.In.Address, strings.Join(checks, ", "))
	if d.In.SNMP != 0 {
		s.redirect(w, r, "/devices/"+strconv.FormatInt(id, 10)+"/discover", "Device added. Choose what to monitor over SNMP.")
		return
	}
	s.redirect(w, r, "/devices/"+strconv.FormatInt(id, 10), "Device added. The first results come within a minute.")
}

func (s *Server) handleDeviceUpdate(w http.ResponseWriter, r *http.Request) {
	dev := s.loadDevice(w, r)
	if dev == nil {
		return
	}
	d := &deviceFormData{ID: dev.ID, In: deviceInput(r)}
	if err := s.store.UpdateDevice(r.Context(), dev.ID, d.In); err != nil {
		s.showDeviceForm(w, r, http.StatusBadRequest, d, storeErr(err))
		return
	}
	if d.In.SNMP != 0 && d.In.SNMP != dev.SNMPProfile {
		go s.poller.Refresh(context.WithoutCancel(r.Context()), dev.ID)
	}
	s.poller.Reload()
	s.audit(r, "device.update", "device:"+strconv.FormatInt(dev.ID, 10), "%s: address %s, kind %s, paused %v", d.In.Name, d.In.Address, d.In.Kind, d.In.Paused)
	s.redirect(w, r, "/devices/"+strconv.FormatInt(dev.ID, 10), "Saved.")
}

func (s *Server) handleDeviceDelete(w http.ResponseWriter, r *http.Request) {
	dev := s.loadDevice(w, r)
	if dev == nil {
		return
	}
	ids, err := s.store.DeleteDevice(r.Context(), dev.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.disconnectAgent(dev.ID)
	if err := s.tsdb.Forget(r.Context(), ids); err != nil {
		s.log.Error("cannot delete the samples of a device", "device", dev.ID, "err", err)
	}
	s.poller.Reload()
	s.audit(r, "device.delete", "device:"+strconv.FormatInt(dev.ID, 10), "deleted %s (%s) with its checks and data", dev.Name, dev.Address)
	s.redirect(w, r, "/devices", "Device deleted.")
}

// ---- checks

func checkInput(r *http.Request) store.CheckInput {
	return store.CheckInput{Type: r.FormValue("type"), Name: r.FormValue("name"), IntervalS: formInt(r, "interval"), Paused: r.FormValue("paused") == "1",
		Params: store.CheckParams{Count: formInt(r, "count"), TimeoutMS: formInt(r, "timeout_ms"), Port: formInt(r, "port"),
			OID: r.FormValue("oid"), Rate: r.FormValue("rate") == "1", Unit: r.FormValue("unit"), Scale: formFloat(r, "scale")}}
}

func formFloat(r *http.Request, k string) float64 {
	f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(r.FormValue(k)), ",", "."), 64)
	if err != nil {
		return 0
	}
	return f
}

func (s *Server) handleCheckCreate(w http.ResponseWriter, r *http.Request) {
	dev := s.loadDevice(w, r)
	if dev == nil {
		return
	}
	in := checkInput(r)
	id, err := s.store.CreateCheck(r.Context(), dev.ID, in)
	if err != nil {
		s.showDevice(w, r, http.StatusBadRequest, dev, in, storeErr(err))
		return
	}
	s.poller.Reload()
	s.audit(r, "check.create", "device:"+strconv.FormatInt(dev.ID, 10), "check #%d %s (%s) added to %s", id, in.Name, in.Type, dev.Name)
	s.redirect(w, r, "/devices/"+strconv.FormatInt(dev.ID, 10)+"#check-"+strconv.FormatInt(id, 10), "Check added.")
}

func (s *Server) loadCheck(w http.ResponseWriter, r *http.Request) *store.Check {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return nil
	}
	c, err := s.store.CheckByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	return c
}

func devURL(c *store.Check) string { return "/devices/" + strconv.FormatInt(c.DeviceID, 10) }

func (s *Server) handleCheckUpdate(w http.ResponseWriter, r *http.Request) {
	c := s.loadCheck(w, r)
	if c == nil {
		return
	}
	in := checkInput(r)
	if err := s.store.UpdateCheck(r.Context(), c.ID, in); err != nil {
		s.redirectErr(w, r, devURL(c)+"#check-"+strconv.FormatInt(c.ID, 10), storeErr(err))
		return
	}
	s.poller.Reload()
	s.audit(r, "check.update", "device:"+strconv.FormatInt(c.DeviceID, 10), "check #%d %s: interval %ds, paused %v", c.ID, in.Name, in.IntervalS, in.Paused)
	s.redirect(w, r, devURL(c)+"#check-"+strconv.FormatInt(c.ID, 10), "Saved.")
}

func (s *Server) handleCheckDelete(w http.ResponseWriter, r *http.Request) {
	c := s.loadCheck(w, r)
	if c == nil {
		return
	}
	ids, err := s.store.DeleteCheck(r.Context(), c.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.tsdb.Forget(r.Context(), ids); err != nil {
		s.log.Error("cannot delete the samples of a check", "check", c.ID, "err", err)
	}
	s.poller.Reload()
	s.audit(r, "check.delete", "device:"+strconv.FormatInt(c.DeviceID, 10), "check #%d %s of %s deleted with its data", c.ID, c.Name, c.Device)
	s.redirect(w, r, devURL(c), "Check deleted.")
}

func (s *Server) handleCheckRun(w http.ResponseWriter, r *http.Request) {
	c := s.loadCheck(w, r)
	if c == nil {
		return
	}
	s.poller.RunNow(c.ID)
	s.redirect(w, r, devURL(c)+"#check-"+strconv.FormatInt(c.ID, 10), "The check runs now; reload the page in a few seconds.")
}

// ---- series data for the graphs (JSON)

type seriesJSON struct {
	Step int64      `json:"step"`
	From int64      `json:"from"`
	To   int64      `json:"to"`
	T    []int64    `json:"t"`
	Min  []*float64 `json:"min"`
	Avg  []*float64 `json:"avg"`
	Max  []*float64 `json:"max"`
	Unit string     `json:"unit"`
}

func num(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	v = math.Round(v*1000) / 1000
	return &v
}

// handleSeries returns the points of a series: ?range=24h&points=600, or
// ?from=&to= in Unix seconds.
func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok || id > math.MaxInt32 {
		s.notFound(w, r)
		return
	}
	sid := int32(id) // #nosec G115 -- checked against MaxInt32 above
	unit, err := s.store.SeriesUnit(r.Context(), sid)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	to := s.now().Add(time.Second) // the newest sample is included
	from := to.Add(-rangeDuration(r.FormValue("range")))
	if f, err := strconv.ParseInt(r.FormValue("from"), 10, 64); err == nil {
		if t, err := strconv.ParseInt(r.FormValue("to"), 10, 64); err == nil && t > f && t-f <= 5*366*86400 {
			from, to = time.Unix(f, 0), time.Unix(t, 0)
		}
	}
	pts := formInt(r, "points")
	if pts < 50 || pts > 3000 {
		pts = 600
	}
	res, err := s.tsdb.Query(r.Context(), sid, from, to, pts)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	out := seriesJSON{Step: res.Step, From: from.Unix(), To: to.Unix(), Unit: unit, T: []int64{}, Min: []*float64{}, Avg: []*float64{}, Max: []*float64{}}
	for _, p := range res.Points {
		out.T = append(out.T, p.T)
		out.Avg = append(out.Avg, num(p.Avg))
		if res.Step > 0 {
			out.Min = append(out.Min, num(p.Min))
			out.Max = append(out.Max, num(p.Max))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

// ---- groups

type groupsData struct {
	Groups []store.Group
	Name   string
}

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	s.showGroups(w, r, http.StatusOK, "", "")
}

func (s *Server) showGroups(w http.ResponseWriter, r *http.Request, status int, name, errMsg string) {
	gs, err := s.store.Groups(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p := &Page{Title: "Groups", Nav: "groups", Data: &groupsData{Groups: gs, Name: name}}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "groups", p)
}

func (s *Server) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	id, err := s.store.CreateGroup(r.Context(), name, r.FormValue("notes"))
	if err != nil {
		s.showGroups(w, r, http.StatusBadRequest, name, storeErr(err))
		return
	}
	s.audit(r, "group.create", "group:"+strconv.FormatInt(id, 10), "group %q added", strings.TrimSpace(name))
	s.redirect(w, r, "/groups", "Group added.")
}

func (s *Server) handleGroupUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	name := r.FormValue("name")
	if err := s.store.UpdateGroup(r.Context(), id, name, r.FormValue("notes")); err != nil {
		s.redirectErr(w, r, "/groups", storeErr(err))
		return
	}
	s.audit(r, "group.update", "group:"+strconv.FormatInt(id, 10), "group renamed to %q", strings.TrimSpace(name))
	s.redirect(w, r, "/groups", "Saved.")
}

func (s *Server) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return
	}
	if err := s.store.DeleteGroup(r.Context(), id); err != nil {
		s.redirectErr(w, r, "/groups", storeErr(err))
		return
	}
	s.audit(r, "group.delete", "group:"+strconv.FormatInt(id, 10), "group deleted; its devices have no group now")
	s.redirect(w, r, "/groups", "Group deleted. Its devices stay, without a group.")
}
