package web

import (
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/max-zit/watchzit/internal/alert"
	"github.com/max-zit/watchzit/internal/store"
)

// Dashboards: pages of widgets (graphs, big values, device status, problems,
// maps) in a grid, like Grafana boards; they reload every minute.

type widgetView struct {
	Index    int
	W        store.Widget
	Graph    *graphCtx
	Value    string
	ValueOK  string // status class of the value's check
	Check    *store.Check
	Devices  []*store.Device
	Problems []*store.Problem
	Map      *store.Map
	Missing  bool // what it shows was deleted
}

type checkChoice struct {
	ID     int64
	Label  string
	Series []store.Series
}

type dashboardData struct {
	D       *store.Dashboard
	List    []*store.Dashboard
	Views   []widgetView
	Edit    bool
	Range   string
	Ranges  []string
	Checks  []checkChoice
	Groups  []store.Group
	Maps    []*store.Map
	Kinds   []string
	Now     any
	NewName string
}

func (s *Server) handleDashboards(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Dashboards(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "dashboards", &Page{Title: "Dashboards", Nav: "dashboards", Data: &dashboardData{List: list}})
}

func (s *Server) handleDashboardCreate(w http.ResponseWriter, r *http.Request) {
	id, err := s.store.CreateDashboard(r.Context(), r.FormValue("name"), userFrom(r).Username)
	if err != nil {
		s.redirectErr(w, r, "/dashboards", storeErr(err))
		return
	}
	s.audit(r, "dashboard.create", "dashboard:"+strconv.FormatInt(id, 10), "dashboard %q created", r.FormValue("name"))
	s.redirect(w, r, "/dashboards/"+strconv.FormatInt(id, 10)+"?edit=1", "")
}

func (s *Server) loadDashboard(w http.ResponseWriter, r *http.Request) *store.Dashboard {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return nil
	}
	d, err := s.store.DashboardByID(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return nil
	}
	return d
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := s.loadDashboard(w, r)
	if d == nil {
		return
	}
	data := &dashboardData{D: d, Edit: r.FormValue("edit") == "1" && userFrom(r).CanWrite(), Range: r.FormValue("range"), Ranges: graphRanges}
	if !slices.Contains(graphRanges, data.Range) {
		data.Range = "24h"
	}
	maps, _ := s.store.Maps(ctx)
	mapByID := map[int64]*store.Map{}
	for _, m := range maps {
		mapByID[m.ID] = m
	}
	for i, wd := range d.Widgets {
		v := widgetView{Index: i, W: wd}
		switch wd.Kind {
		case store.WidgetGraph, store.WidgetValue:
			c, err := s.store.CheckByID(ctx, wd.CheckID)
			if err != nil {
				v.Missing = true
				break
			}
			v.Check = c
			if wd.Kind == store.WidgetGraph {
				if gs := graphsOf([]*store.Check{c}); len(gs) > 0 {
					v.Graph = &graphCtx{G: gs[0], Range: data.Range, Title: wd.Title}
				} else {
					v.Missing = true
				}
				break
			}
			vals, _ := s.store.CheckValues(ctx, c.ID)
			if x, ok := vals[wd.Metric]; ok {
				v.Value = alert.FormatValue(x, store.MetricUnit(c.Type, wd.Metric, c.Params))
			} else {
				v.Value = "—"
			}
			v.ValueOK = c.Status
		case store.WidgetStatus:
			f := store.DeviceFilter{GroupID: wd.GroupID}
			v.Devices, _ = s.store.Devices(ctx, f)
		case store.WidgetProblems:
			ps, _ := s.store.Problems(ctx, store.ProblemFilter{Open: true, Limit: 12})
			for _, p := range ps {
				if wd.GroupID == 0 || s.deviceInGroup(r, p.DeviceID, wd.GroupID) {
					v.Problems = append(v.Problems, p)
				}
			}
		case store.WidgetMap:
			v.Map = mapByID[wd.MapID]
			v.Missing = v.Map == nil
		}
		data.Views = append(data.Views, v)
	}
	data.Now = s.now()
	if data.Edit {
		if err := s.dashboardChoices(r, data, maps); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	p := &Page{Title: d.Name, Nav: "dashboards", Data: data}
	if !data.Edit {
		p.Refresh = 60
	}
	s.render(w, r, http.StatusOK, "dashboard", p)
}

func (s *Server) deviceInGroup(r *http.Request, deviceID, groupID int64) bool {
	d, err := s.store.DeviceByID(r.Context(), deviceID)
	return err == nil && d.GroupID != nil && *d.GroupID == groupID
}

// dashboardChoices fills the lists of the add-widget form.
func (s *Server) dashboardChoices(r *http.Request, data *dashboardData, maps []*store.Map) error {
	cs, err := s.store.AllChecks(r.Context())
	if err != nil {
		return err
	}
	for _, c := range cs {
		data.Checks = append(data.Checks, checkChoice{ID: c.ID, Label: c.Device + " · " + c.Name, Series: c.Series})
	}
	sort.Slice(data.Checks, func(i, j int) bool {
		return strings.ToLower(data.Checks[i].Label) < strings.ToLower(data.Checks[j].Label)
	})
	if data.Groups, err = s.store.Groups(r.Context()); err != nil {
		return err
	}
	data.Maps = maps
	data.Kinds = store.WidgetKinds
	return nil
}

func (s *Server) saveWidgets(w http.ResponseWriter, r *http.Request, d *store.Dashboard, ws []store.Widget, msg string) {
	back := "/dashboards/" + strconv.FormatInt(d.ID, 10) + "?edit=1"
	if err := s.store.SetWidgets(r.Context(), d.ID, ws); err != nil {
		s.redirectErr(w, r, back, storeErr(err))
		return
	}
	s.audit(r, "dashboard.update", "dashboard:"+strconv.FormatInt(d.ID, 10), "dashboard %q: %s", d.Name, msg)
	s.redirect(w, r, back, "")
}

func (s *Server) handleWidgetAdd(w http.ResponseWriter, r *http.Request) {
	d := s.loadDashboard(w, r)
	if d == nil {
		return
	}
	wd := store.Widget{Kind: r.FormValue("kind"), Title: r.FormValue("title"), Size: r.FormValue("size")}
	switch wd.Kind {
	case store.WidgetGraph:
		wd.CheckID, _ = strconv.ParseInt(r.FormValue("check"), 10, 64)
	case store.WidgetValue:
		// "check|metric"
		if c, m, ok := strings.Cut(r.FormValue("series"), "|"); ok {
			wd.CheckID, _ = strconv.ParseInt(c, 10, 64)
			wd.Metric = m
		}
	case store.WidgetStatus, store.WidgetProblems:
		wd.GroupID, _ = strconv.ParseInt(r.FormValue("group"), 10, 64)
	case store.WidgetMap:
		wd.MapID, _ = strconv.ParseInt(r.FormValue("map"), 10, 64)
	}
	s.saveWidgets(w, r, d, append(d.Widgets, wd), "widget "+wd.Kind+" added")
}

func (s *Server) handleWidgetChange(w http.ResponseWriter, r *http.Request) {
	d := s.loadDashboard(w, r)
	if d == nil {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 || n >= len(d.Widgets) {
		s.notFound(w, r)
		return
	}
	ws := slices.Clone(d.Widgets)
	switch r.FormValue("do") {
	case "up":
		if n > 0 {
			ws[n-1], ws[n] = ws[n], ws[n-1]
		}
	case "down":
		if n < len(ws)-1 {
			ws[n+1], ws[n] = ws[n], ws[n+1]
		}
	case "size":
		ws[n].Size = r.FormValue("size")
	case "delete":
		ws = slices.Delete(ws, n, n+1)
	default:
		s.notFound(w, r)
		return
	}
	s.saveWidgets(w, r, d, ws, "widget "+strconv.Itoa(n+1)+" "+r.FormValue("do"))
}

func (s *Server) handleDashboardUpdate(w http.ResponseWriter, r *http.Request) {
	d := s.loadDashboard(w, r)
	if d == nil {
		return
	}
	back := "/dashboards/" + strconv.FormatInt(d.ID, 10) + "?edit=1"
	if err := s.store.RenameDashboard(r.Context(), d.ID, r.FormValue("name")); err != nil {
		s.redirectErr(w, r, back, storeErr(err))
		return
	}
	s.audit(r, "dashboard.update", "dashboard:"+strconv.FormatInt(d.ID, 10), "dashboard %q renamed to %q", d.Name, r.FormValue("name"))
	s.redirect(w, r, back, "Saved.")
}

func (s *Server) handleDashboardDelete(w http.ResponseWriter, r *http.Request) {
	d := s.loadDashboard(w, r)
	if d == nil {
		return
	}
	if err := s.store.DeleteDashboard(r.Context(), d.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "dashboard.delete", "dashboard:"+strconv.FormatInt(d.ID, 10), "dashboard %q deleted", d.Name)
	s.redirect(w, r, "/dashboards", "Dashboard deleted.")
}
