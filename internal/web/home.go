package web

import (
	"net/http"
	"strconv"
	"strings"
)

// homePage is where "/" takes a user who chose a map or a dashboard as
// the start page ("" = the overview; also when that map or dashboard is
// gone).
func (s *Server) homePage(r *http.Request) string {
	u := userFrom(r)
	if u == nil || u.Home == "" {
		return ""
	}
	kind, num, _ := strings.Cut(u.Home, ":")
	id, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return ""
	}
	switch kind {
	case "map":
		if _, err := s.store.MapByID(r.Context(), id); err == nil {
			return "/maps/" + num
		}
	case "dashboard":
		if _, err := s.store.DashboardByID(r.Context(), id); err == nil {
			return "/dashboards/" + num
		}
	}
	return ""
}

// homeChoices are the start pages a user can pick on My account.
type homeChoice struct {
	Value, Name string
}

func (s *Server) homeChoices(r *http.Request) (maps, dashboards []homeChoice) {
	if ms, err := s.store.Maps(r.Context()); err == nil {
		for _, m := range ms {
			maps = append(maps, homeChoice{"map:" + strconv.FormatInt(m.ID, 10), m.Name})
		}
	}
	if ds, err := s.store.Dashboards(r.Context()); err == nil {
		for _, d := range ds {
			dashboards = append(dashboards, homeChoice{"dashboard:" + strconv.FormatInt(d.ID, 10), d.Name})
		}
	}
	return maps, dashboards
}

func (s *Server) handleAccountHome(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	home := r.FormValue("home")
	if err := s.store.SetHome(r.Context(), u.ID, home); err != nil {
		s.redirectErr(w, r, "/account", storeErr(err))
		return
	}
	u.Home = home
	if s.homePage(r) == "" && home != "" {
		_ = s.store.SetHome(r.Context(), u.ID, "")
		s.redirectErr(w, r, "/account", "That map or dashboard does not exist.")
		return
	}
	s.redirect(w, r, "/account", "Start page saved.")
}
