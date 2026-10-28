package web

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/max-zit/watchzit/internal/store"
)

// ---- Settings → Kiosk displays

type kiosksData struct {
	Kiosks []*store.Kiosk
	Maps   []*store.Map
	Edit   *store.Kiosk
	In     store.KioskInput
	Link   string // a new display's link, shown once
}

func (s *Server) showKiosks(w http.ResponseWriter, r *http.Request, status int, d *kiosksData, errMsg string) {
	var err error
	if d.Kiosks, err = s.store.Kiosks(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Maps, err = s.store.Maps(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	p := &Page{Title: "Kiosk displays", Nav: "settings", Data: d}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "kiosks", p)
}

func (s *Server) handleKiosks(w http.ResponseWriter, r *http.Request) {
	s.showKiosks(w, r, http.StatusOK, &kiosksData{In: store.KioskInput{RotateS: 30, Theme: "dark"}}, "")
}

func kioskInput(r *http.Request) store.KioskInput {
	_ = r.ParseForm()
	in := store.KioskInput{Name: r.FormValue("name"), RotateS: formInt(r, "rotate"), Theme: r.FormValue("theme")}
	for _, v := range r.Form["map"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && !slices.Contains(in.Maps, id) {
			in.Maps = append(in.Maps, id)
		}
	}
	return in
}

// kioskURL is the link of a display on this console.
func kioskURL(r *http.Request, token string) string {
	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	return scheme + "://" + r.Host + "/kiosk/" + token
}

func (s *Server) handleKioskCreate(w http.ResponseWriter, r *http.Request) {
	in := kioskInput(r)
	id, token, err := s.store.CreateKiosk(r.Context(), in)
	if err != nil {
		s.showKiosks(w, r, http.StatusBadRequest, &kiosksData{In: in}, storeErr(err))
		return
	}
	s.audit(r, "kiosk.create", "kiosk:"+strconv.FormatInt(id, 10), "kiosk display %q added for %d maps", strings.TrimSpace(in.Name), len(in.Maps))
	s.showKiosks(w, r, http.StatusOK, &kiosksData{In: store.KioskInput{RotateS: 30, Theme: "dark"}, Link: kioskURL(r, token)}, "")
}

func (s *Server) loadKiosk(w http.ResponseWriter, r *http.Request) *store.Kiosk {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return nil
	}
	k, err := s.store.KioskByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	return k
}

func (s *Server) handleKiosk(w http.ResponseWriter, r *http.Request) {
	if k := s.loadKiosk(w, r); k != nil {
		s.showKiosks(w, r, http.StatusOK, &kiosksData{Edit: k, In: store.KioskInput{Name: k.Name, Maps: k.Maps, RotateS: k.RotateS, Theme: k.Theme}}, "")
	}
}

func (s *Server) handleKioskUpdate(w http.ResponseWriter, r *http.Request) {
	k := s.loadKiosk(w, r)
	if k == nil {
		return
	}
	in := kioskInput(r)
	if err := s.store.UpdateKiosk(r.Context(), k.ID, in); err != nil {
		s.showKiosks(w, r, http.StatusBadRequest, &kiosksData{Edit: k, In: in}, storeErr(err))
		return
	}
	s.audit(r, "kiosk.update", "kiosk:"+strconv.FormatInt(k.ID, 10), "kiosk display %q saved (%d maps)", strings.TrimSpace(in.Name), len(in.Maps))
	s.redirect(w, r, "/settings/kiosks", "Saved.")
}

func (s *Server) handleKioskDelete(w http.ResponseWriter, r *http.Request) {
	k := s.loadKiosk(w, r)
	if k == nil {
		return
	}
	if err := s.store.DeleteKiosk(r.Context(), k.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "kiosk.delete", "kiosk:"+strconv.FormatInt(k.ID, 10), "kiosk display %q deleted; its link no longer works", k.Name)
	s.redirect(w, r, "/settings/kiosks", "Kiosk display deleted. Its link no longer works.")
}

// ---- the display itself (no sign-in: the secret link is the key; the
// console's allow-list still applies)

func (s *Server) kioskFrom(w http.ResponseWriter, r *http.Request) *store.Kiosk {
	k, err := s.store.KioskByToken(r.Context(), r.PathValue("token"), clientIP(r))
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("kiosk lookup", "err", err)
		}
		http.Error(w, "This kiosk link is not valid (any more).", http.StatusNotFound)
		return nil
	}
	return k
}

type kioskPageData struct {
	Base   string
	Maps   string
	Rotate int
	Theme  string
	Name   string
}

func (s *Server) handleKioskPage(w http.ResponseWriter, r *http.Request) {
	k := s.kioskFrom(w, r)
	if k == nil {
		return
	}
	ids := make([]string, len(k.Maps))
	for i, id := range k.Maps {
		ids[i] = strconv.FormatInt(id, 10)
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.render(w, r, http.StatusOK, "kiosk", &Page{Title: k.Name, Data: &kioskPageData{Base: "/kiosk/" + r.PathValue("token"), Maps: strings.Join(ids, ","), Rotate: k.RotateS, Theme: k.Theme, Name: k.Name}})
}

func (s *Server) kioskMap(w http.ResponseWriter, r *http.Request) (*store.Kiosk, *store.Map) {
	k := s.kioskFrom(w, r)
	if k == nil {
		return nil, nil
	}
	id, ok := pathID(r, "map")
	if !ok || !slices.Contains(k.Maps, id) {
		http.NotFound(w, r)
		return nil, nil
	}
	m, err := s.store.MapByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return nil, nil
	}
	return k, m
}

func (s *Server) handleKioskLayout(w http.ResponseWriter, r *http.Request) {
	_, m := s.kioskMap(w, r)
	if m == nil {
		return
	}
	s.writeMapJSON(w, r, m, false, s.lang(r, nil))
}

func (s *Server) handleKioskLive(w http.ResponseWriter, r *http.Request) {
	_, m := s.kioskMap(w, r)
	if m == nil {
		return
	}
	s.writeMapLive(w, r, m, false)
}

// handleKioskAsset serves the pictures used on the display's maps.
func (s *Server) handleKioskAsset(w http.ResponseWriter, r *http.Request) {
	k := s.kioskFrom(w, r)
	if k == nil {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	ref := "asset:" + strconv.FormatInt(id, 10)
	for _, mid := range k.Maps {
		l, err := s.store.MapLayout(r.Context(), mid)
		if err != nil {
			continue
		}
		used := l.Background != nil && l.Background.Asset == id
		for _, n := range l.Nodes {
			used = used || n.Icon == ref
		}
		if used {
			s.serveAsset(w, r, id)
			return
		}
	}
	http.NotFound(w, r)
}
