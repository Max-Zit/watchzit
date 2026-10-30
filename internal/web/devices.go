package web

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/cfgbackup"
	"github.com/max-zit/watchzit/internal/launch"
	"github.com/max-zit/watchzit/internal/store"
)

// ---- overview

type overviewData struct {
	Counts   map[string]int
	Total    int
	Problems []*store.Problem
	Now      time.Time
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
	if d.Problems, err = s.store.Problems(ctx, store.ProblemFilter{Open: true, Limit: 20}); err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Now = s.now()
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
	GraphEdit  template.JS // JSON for the graph editor (HTML-safe, from json.Marshal): the graphs as panels and every series
	Range      string
	Ranges     []string
	New        store.CheckInput // the add-check form after an error
	Facts      store.Facts
	Profile    *store.SNMPProfile
	Interfaces []store.Interface
	VLANs      []store.VLANSummary
	PortModes  []store.PortMode
	HA         *store.HARole
	VXLANs     []store.VXLANInfo
	Traffic    map[int]*store.Check // interface checks by ifIndex
	Agent      *store.AgentInfo
	Watched    map[string]bool // services the agent reports
	Problems   []*store.Problem
	Now        time.Time
	Tools      []deviceTool
	AccessUser string
	CheckTypes []string
	// configuration backups (operators and administrators)
	Backup   *store.DeviceBackup
	Versions []*store.ConfigVersion
	Methods  []string
}

// graphDef is one graph of the device page: the series of a check that
// share a unit are drawn together (traffic in and out).
type graphDef struct {
	Check  *store.Check
	Title  string // metric name, translated in the template
	Unit   string
	Series []store.Series
	// arranged by the user (empty for automatic graphs)
	ID     string
	Name   string   // the title the user gave
	Labels []string // series labels when they come from several checks
	Panel  *store.GraphPanel
	// data source queries: their addresses and the colours of their first series
	Queries []string
	QColors []string
}

// hiddenMetrics are kept but not drawn (they say little as a line).
var hiddenMetrics = map[string]bool{"uptime_s": true, "used_b": true, "in_err": true, "out_err": true, "mem_used_b": true, "running": true, "status_code": true}

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
	layout, err := s.store.DeviceGraphLayout(r.Context(), d.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := &deviceData{D: d, Checks: cs, Graphs: arrangeGraphs(d.ID, cs, layout), Range: rg, Ranges: graphRanges, New: in, CheckTypes: store.CheckTypes, Traffic: map[int]*store.Check{}}
	data.Problems = s.deviceProblems(r.Context(), d.ID)
	if u := userFrom(r); u != nil && u.CanWrite() {
		srcs, _ := s.store.DataSources(r.Context())
		data.GraphEdit = graphEditJSON(s.lang(r, u), cs, data.Graphs, layout, srcs)
		data.Backup, _ = s.store.DeviceBackupOf(r.Context(), d.ID)
		data.Versions, _ = s.store.ConfigVersions(r.Context(), d.ID)
		data.Methods = cfgbackup.Methods
	}
	data.Now = s.now()
	if a, err := s.store.DeviceAccess(r.Context(), d.ID); err == nil {
		data.Tools, data.AccessUser = s.toolsFor(r.Context(), d.Kind, a), a.User
	}
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
	if data.VLANs, err = s.store.DeviceVLANs(r.Context(), d.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if data.PortModes, err = s.store.DevicePortModes(r.Context(), d.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if data.HA, err = s.store.DeviceHA(r.Context(), d.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if data.VXLANs, err = s.store.DeviceVXLANs(r.Context(), d.ID); err != nil {
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
	p := &Page{Title: d.Name, Nav: "devices", Data: data, Device: d.Address}
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
	Devices  []*store.Device // possible parents
	Access   store.DeviceAccess
	Tools    []*store.Tool // enabled tools, for port overrides
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
		d.In = store.DeviceInput{GroupID: deref(dev.GroupID), Name: dev.Name, Address: dev.Address, Kind: dev.Kind, WebURL: dev.WebURL, WebName: dev.WebName, WebLinks: dev.WebLinks, Notes: dev.Notes, Paused: dev.Paused, SNMP: dev.SNMPProfile, Parent: dev.Parent}
		var err error
		if d.Access, err = s.store.DeviceAccess(r.Context(), dev.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
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
	if ts, err := s.store.Tools(r.Context()); err == nil {
		for _, t := range ts {
			if t.Enabled {
				d.Tools = append(d.Tools, t)
			}
		}
	}
	all, err := s.store.Devices(r.Context(), store.DeviceFilter{})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, x := range all {
		if x.ID != d.ID {
			d.Devices = append(d.Devices, x)
		}
	}
	sort.Slice(d.Devices, func(i, j int) bool { return strings.ToLower(d.Devices[i].Name) < strings.ToLower(d.Devices[j].Name) })
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
	_ = r.ParseForm() // for the repeated link fields
	g, _ := strconv.ParseInt(r.FormValue("group"), 10, 64)
	sp, _ := strconv.ParseInt(r.FormValue("snmp"), 10, 64)
	pa, _ := strconv.ParseInt(r.FormValue("parent"), 10, 64)
	var links []store.WebLink
	names, urls := r.Form["link_name"], r.Form["link_url"]
	for i := 0; i < len(urls) && i < 20; i++ {
		l := store.WebLink{URL: urls[i]}
		if i < len(names) {
			l.Name = names[i]
		}
		links = append(links, l)
	}
	return store.DeviceInput{GroupID: g, SNMP: sp, Parent: pa, Name: r.FormValue("name"), Address: r.FormValue("address"), Kind: r.FormValue("kind"),
		WebURL: r.FormValue("web_url"), Notes: r.FormValue("notes"), Paused: r.FormValue("paused") == "1", WebName: r.FormValue("web_name"), WebLinks: links}
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
	d.Access = accessFromForm(r)
	if !launch.ValidUser(d.Access.User) {
		s.showDeviceForm(w, r, http.StatusBadRequest, d, "the user name may contain letters, digits and . _ @ \\ - only")
		return
	}
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
	if err := s.store.SetDeviceAccess(ctx, id, d.Access); err != nil {
		s.serverError(w, r, err)
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
	d.Access = accessFromForm(r)
	if !launch.ValidUser(d.Access.User) {
		s.showDeviceForm(w, r, http.StatusBadRequest, d, "the user name may contain letters, digits and . _ @ \\ - only")
		return
	}
	if err := s.store.UpdateDevice(r.Context(), dev.ID, d.In); err != nil {
		s.showDeviceForm(w, r, http.StatusBadRequest, d, storeErr(err))
		return
	}
	if err := s.store.SetDeviceAccess(r.Context(), dev.ID, d.Access); err != nil {
		s.serverError(w, r, err)
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
			OID: r.FormValue("oid"), Rate: r.FormValue("rate") == "1", Unit: r.FormValue("unit"), Scale: formFloat(r, "scale"),
			URL: r.FormValue("url"), Expect: formInt(r, "expect"), Contains: r.FormValue("contains"), Insecure: r.FormValue("insecure") == "1",
			Query: r.FormValue("query"), RType: r.FormValue("rtype"), User: r.FormValue("user"), Secret: r.FormValue("secret"),
			Database: r.FormValue("database"), Path: r.FormValue("path"), Auth: r.FormValue("auth"), TLS: r.FormValue("tls") == "1"}}
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
	Step  int64      `json:"step"`
	From  int64      `json:"from"`
	To    int64      `json:"to"`
	T     []int64    `json:"t"`
	Min   []*float64 `json:"min"`
	Avg   []*float64 `json:"avg"`
	Max   []*float64 `json:"max"`
	Unit  string     `json:"unit"`
	Label string     `json:"label,omitempty"` // series of a data source
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
	sla, _ := strconv.ParseFloat(strings.Replace(strings.TrimSpace(r.FormValue("sla")), ",", ".", 1), 64)
	if err := s.store.SetGroupSLA(r.Context(), id, sla); err != nil {
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

// arrangeGraphs applies the user's arrangement to the graphs of a device:
// the panels in their order, then automatic graphs of the series no panel
// shows and that are not hidden.
func arrangeGraphs(devID int64, cs []*store.Check, l *store.GraphLayout) []graphDef {
	auto := graphsOf(cs)
	for i := range auto {
		auto[i].ID = "a" + strconv.FormatInt(auto[i].Check.ID, 10) + "-" + strconv.Itoa(i)
	}
	if l == nil {
		return auto
	}
	type owned struct {
		c *store.Check
		s store.Series
	}
	byID := map[int32]owned{}
	for _, c := range cs {
		for _, s := range c.Series {
			byID[s.ID] = owned{c, s}
		}
	}
	used := map[int32]bool{}
	for _, id := range l.Hidden {
		used[id] = true
	}
	var out []graphDef
	for i := range l.Panels {
		p := &l.Panels[i]
		g := graphDef{ID: p.ID, Name: p.Title, Panel: p}
		checks := map[int64]bool{}
		for _, sid := range p.Series {
			o, ok := byID[sid]
			if !ok {
				continue
			}
			used[sid] = true
			if g.Check == nil {
				g.Check, g.Unit, g.Title = o.c, o.s.Unit, metricNames[o.s.Metric]
			}
			checks[o.c.ID] = true
			g.Series = append(g.Series, o.s)
		}
		for qi, q := range p.Queries {
			g.Queries = append(g.Queries, "/devices/"+strconv.FormatInt(devID, 10)+"/graphs/"+p.ID+"/q/"+strconv.Itoa(qi))
			g.QColors = append(g.QColors, p.Colors["q"+strconv.Itoa(qi)])
			if g.Unit == "" {
				g.Unit = q.Unit
			}
		}
		if len(g.Series)+len(g.Queries) == 0 {
			continue
		}
		if len(checks) > 1 {
			for _, sid := range p.Series {
				if o, ok := byID[sid]; ok {
					g.Labels = append(g.Labels, o.c.Name+" · "+metricNames[o.s.Metric])
				}
			}
		} else if len(g.Series) > 1 && unitTitles[g.Unit] != "" {
			g.Title = unitTitles[g.Unit]
		}
		out = append(out, g)
	}
	for _, a := range auto {
		var rest []store.Series
		for _, s := range a.Series {
			if !used[s.ID] {
				rest = append(rest, s)
			}
		}
		if len(rest) == 0 {
			continue
		}
		a.Series = rest
		out = append(out, a)
	}
	return out
}

// graphEditJSON is what the graph editor of a device page works on: the
// graphs as shown (automatic ones as panels too), the hidden series and
// every series of the device with its name.
func graphEditJSON(lang string, cs []*store.Check, gs []graphDef, l *store.GraphLayout, srcs []*store.DataSource) template.JS {
	type series struct {
		ID    int32  `json:"id"`
		Label string `json:"label"`
		Unit  string `json:"unit"`
	}
	type source struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	out := struct {
		Panels  []store.GraphPanel `json:"panels"`
		Hidden  []int32            `json:"hidden"`
		Series  []series           `json:"series"`
		Sources []source           `json:"sources"`
	}{Panels: []store.GraphPanel{}, Hidden: []int32{}, Series: []series{}, Sources: []source{}}
	for _, d := range srcs {
		out.Sources = append(out.Sources, source{d.ID, d.Name, d.Kind})
	}
	for _, g := range gs {
		p := store.GraphPanel{ID: g.ID}
		if g.Panel != nil {
			p = *g.Panel
		}
		p.Series = []int32{}
		for _, s := range g.Series {
			p.Series = append(p.Series, s.ID)
		}
		out.Panels = append(out.Panels, p)
	}
	if l != nil {
		out.Hidden = append(out.Hidden, l.Hidden...)
	}
	for _, c := range cs {
		for _, s := range c.Series {
			if hiddenMetrics[s.Metric] {
				continue
			}
			out.Series = append(out.Series, series{s.ID, c.Name + " · " + translate(lang, metricNames[s.Metric]), s.Unit})
		}
	}
	b, _ := json.Marshal(out) // escapes <, > and & for HTML
	return template.JS(b)     // #nosec G203 -- JSON of our own data, HTML-escaped by json.Marshal
}

// handleDeviceGraphs stores how the graphs of a device are arranged (JSON),
// or goes back to the automatic graphs (reset).
func (s *Server) handleDeviceGraphs(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.DeviceByID(r.Context(), id); err != nil {
		http.NotFound(w, r)
		return
	}
	lang := s.lang(r, userFrom(r))
	if r.URL.Query().Get("reset") == "1" {
		if err := s.store.ResetDeviceGraphs(r.Context(), id); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.audit(r, "device.graphs", "device:"+strconv.FormatInt(id, 10), "graphs back to automatic")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	var l store.GraphLayout
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&l); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": translate(lang, "the graphs could not be read")})
		return
	}
	if err := s.store.SetDeviceGraphLayout(r.Context(), id, &l); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": translate(lang, storeErr(err))})
		return
	}
	s.audit(r, "device.graphs", "device:"+strconv.FormatInt(id, 10), "graphs arranged: %d", len(l.Panels))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
