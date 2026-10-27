package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/max-zit/watchzit/internal/store"
)

// ---- map list

type mapsData struct {
	Maps []*store.Map
	Name string
}

func (s *Server) handleMaps(w http.ResponseWriter, r *http.Request) {
	s.showMaps(w, r, http.StatusOK, "", "")
}

func (s *Server) showMaps(w http.ResponseWriter, r *http.Request, status int, name, errMsg string) {
	ms, err := s.store.Maps(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p := &Page{Title: "Maps", Nav: "maps", Data: &mapsData{Maps: ms, Name: name}}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "maps", p)
}

func (s *Server) handleMapCreate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	id, err := s.store.CreateMap(r.Context(), name, r.FormValue("notes"))
	if err != nil {
		s.showMaps(w, r, http.StatusBadRequest, name, storeErr(err))
		return
	}
	s.audit(r, "map.create", "map:"+strconv.FormatInt(id, 10), "map %q added", strings.TrimSpace(name))
	http.Redirect(w, r, "/maps/"+strconv.FormatInt(id, 10)+"?edit=1", http.StatusSeeOther)
}

func (s *Server) loadMap(w http.ResponseWriter, r *http.Request) *store.Map {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return nil
	}
	m, err := s.store.MapByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	return m
}

func (s *Server) handleMapUpdate(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	name := r.FormValue("name")
	if err := s.store.UpdateMap(r.Context(), m.ID, name, r.FormValue("notes")); err != nil {
		s.redirectErr(w, r, "/maps", storeErr(err))
		return
	}
	s.audit(r, "map.update", "map:"+strconv.FormatInt(m.ID, 10), "map %q renamed to %q", m.Name, strings.TrimSpace(name))
	s.redirect(w, r, "/maps", "Saved.")
}

func (s *Server) handleMapDelete(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	if err := s.store.DeleteMap(r.Context(), m.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "map.delete", "map:"+strconv.FormatInt(m.ID, 10), "map %q deleted (devices stay)", m.Name)
	s.redirect(w, r, "/maps", "Map deleted.")
}

// ---- the map page; map.js draws it from the JSON below

type mapPageData struct {
	M    *store.Map
	Edit bool
}

func (s *Server) handleMap(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	edit := r.FormValue("edit") == "1" && userFrom(r).CanWrite()
	s.render(w, r, http.StatusOK, "map", &Page{Title: m.Name, Nav: "maps", Data: &mapPageData{M: m, Edit: edit}})
}

// mapText are the words map.js shows, translated on the server.
var mapText = []string{"Edit", "Done", "Zoom in", "Zoom out", "Fit to screen", "Add device", "Add submap", "Add text", "Connect", "Delete",
	"Saved", "Saving…", "Could not save. Reload the page.", "Search", "All devices are on this map.", "Click the first device, then the second.",
	"Open device", "Open web interface", "Open map", "Port", "Not set", "Kind", "Wired", "Wireless", "VPN", "Label", "In", "Out", "Speed",
	"Checks", "down", "Up", "Down", "Warning", "Unknown", "Paused", "No link", "Disabled", "Text", "Ports", "Link", "No other maps yet.",
	"Drag devices to place them. Use Connect to draw a link and choose the ports.", "Address", "Status", "Suggested links", "Add", "Neighbours found over LLDP and MikroTik discovery.",
	"No suggestions.", "Close", "Vendor"}

type mapJSON struct {
	ID      int64             `json:"id"`
	Name    string            `json:"name"`
	CanEdit bool              `json:"can_edit"`
	Layout  *store.MapLayout  `json:"layout"`
	Devices []mapDevice       `json:"devices,omitempty"` // for "Add device"
	Maps    []mapRef          `json:"maps"`              // for submaps and names
	Text    map[string]string `json:"text"`
}

type mapDevice struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Kind    string `json:"kind"`
}

type mapRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleMapLayout(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	ctx := r.Context()
	l, err := s.store.MapLayout(ctx, m.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	u := userFrom(r)
	out := mapJSON{ID: m.ID, Name: m.Name, CanEdit: u.CanWrite(), Layout: l, Maps: []mapRef{}, Text: map[string]string{}}
	lang := s.lang(r, u)
	for _, t := range mapText {
		out.Text[t] = translate(lang, t)
	}
	for k, n := range kindNames {
		out.Text["kind:"+k] = translate(lang, n)
	}
	ms, err := s.store.Maps(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, x := range ms {
		out.Maps = append(out.Maps, mapRef{x.ID, x.Name})
	}
	if out.CanEdit {
		ds, err := s.store.Devices(ctx, store.DeviceFilter{})
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		for _, d := range ds {
			out.Devices = append(out.Devices, mapDevice{d.ID, d.Name, d.Address, d.Kind})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMapLive(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	l, err := s.store.MapLayout(r.Context(), m.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	live, err := s.store.MapLiveData(r.Context(), l)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, live)
}

// handleMapSave stores the editor's layout (JSON body).
func (s *Server) handleMapSave(w http.ResponseWriter, r *http.Request) {
	m := s.loadMap(w, r)
	if m == nil {
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "JSON expected"})
		return
	}
	var in store.MapLayout
	dec := json.NewDecoder(io.LimitReader(r.Body, 4<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid layout"})
		return
	}
	ctx := r.Context()
	before, err := s.store.MapLayout(ctx, m.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	out, err := s.store.SaveMapLayout(ctx, m.ID, &in)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": translate(s.lang(r, userFrom(r)), storeErr(err))})
		return
	}
	// moves are not audited; added or removed nodes and links are
	if structure(before) != structure(out) {
		s.audit(r, "map.save", "map:"+strconv.FormatInt(m.ID, 10), "map %q saved: %d nodes, %d links (before %d, %d)", m.Name, len(out.Nodes), len(out.Links), len(before.Nodes), len(before.Links))
	}
	writeJSON(w, http.StatusOK, out)
}

// structure sums up a layout without positions.
func structure(l *store.MapLayout) string {
	var b strings.Builder
	for _, n := range l.Nodes {
		b.WriteString(n.Kind + strconv.FormatInt(n.DeviceID, 10) + "/" + strconv.FormatInt(n.SubmapID, 10) + n.Label + ";")
	}
	for _, k := range l.Links {
		b.WriteString(strconv.FormatInt(k.A, 10) + "-" + strconv.FormatInt(k.B, 10) + ":" + strconv.Itoa(k.AIf) + "/" + strconv.Itoa(k.BIf) + k.Kind + k.Label + ";")
	}
	return b.String()
}
