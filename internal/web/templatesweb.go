package web

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/max-zit/watchzit/internal/store"
)

// Templates: a set of checks for a kind of device, applied to one device,
// a group, all devices of its kinds, or by itself to new devices.

type templatesData struct {
	Templates []*store.Template
	Kinds     []string
}

type templateData struct {
	T          *store.Template
	Kinds      []string
	CheckTypes []string
	Groups     []store.Group
	Devices    map[int64]string // devices it was applied to
	New        store.CheckInput // the add-check form after an error
}

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	ts, err := s.store.Templates(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "templates", &Page{Title: "Templates", Nav: "templates", Data: &templatesData{Templates: ts, Kinds: store.DeviceKinds}})
}

func (s *Server) handleTemplateCreate(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id, err := s.store.SaveTemplate(r.Context(), 0, r.FormValue("name"), r.Form["kind"], r.FormValue("auto") == "1")
	if errors.Is(err, store.ErrConflict) {
		s.redirectErr(w, r, "/templates", "A template with this name exists.")
		return
	}
	if err != nil {
		s.redirectErr(w, r, "/templates", storeErr(err))
		return
	}
	s.audit(r, "template.create", "template:"+strconv.FormatInt(id, 10), "template %q added", r.FormValue("name"))
	s.redirect(w, r, "/templates/"+strconv.FormatInt(id, 10), "Template added. Now add its checks.")
}

func (s *Server) loadTemplate(w http.ResponseWriter, r *http.Request) *store.Template {
	id, ok := pathID(r, "id")
	if !ok {
		s.notFound(w, r)
		return nil
	}
	t, err := s.store.TemplateByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return nil
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	return t
}

func (s *Server) handleTemplate(w http.ResponseWriter, r *http.Request) {
	if t := s.loadTemplate(w, r); t != nil {
		s.showTemplate(w, r, http.StatusOK, t, store.CheckInput{Type: store.CheckICMP}, "")
	}
}

func (s *Server) showTemplate(w http.ResponseWriter, r *http.Request, status int, t *store.Template, in store.CheckInput, errMsg string) {
	ctx := r.Context()
	d := &templateData{T: t, Kinds: store.DeviceKinds, CheckTypes: store.TemplateCheckTypes, New: in, Devices: map[int64]string{}}
	d.Groups, _ = s.store.Groups(ctx)
	if ids, err := s.store.TemplateDevices(ctx, t.ID); err == nil && len(ids) > 0 {
		devs, _ := s.store.Devices(ctx, store.DeviceFilter{})
		for _, dv := range devs {
			if slices.Contains(ids, dv.ID) {
				d.Devices[dv.ID] = dv.Name
			}
		}
	}
	p := &Page{Title: t.Name, Nav: "templates", Data: d}
	if errMsg != "" {
		p.Error = translate(s.lang(r, userFrom(r)), errMsg)
	}
	s.render(w, r, status, "template", p)
}

func (s *Server) handleTemplateSave(w http.ResponseWriter, r *http.Request) {
	t := s.loadTemplate(w, r)
	if t == nil {
		return
	}
	_ = r.ParseForm()
	back := "/templates/" + strconv.FormatInt(t.ID, 10)
	if _, err := s.store.SaveTemplate(r.Context(), t.ID, r.FormValue("name"), r.Form["kind"], r.FormValue("auto") == "1"); err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.redirectErr(w, r, back, "A template with this name exists.")
			return
		}
		s.redirectErr(w, r, back, storeErr(err))
		return
	}
	s.audit(r, "template.update", "template:"+strconv.FormatInt(t.ID, 10), "template %q changed", r.FormValue("name"))
	s.redirect(w, r, back, "Saved.")
}

func (s *Server) handleTemplateDelete(w http.ResponseWriter, r *http.Request) {
	t := s.loadTemplate(w, r)
	if t == nil {
		return
	}
	if err := s.store.DeleteTemplate(r.Context(), t.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "template.delete", "template:"+strconv.FormatInt(t.ID, 10), "template %q removed (its checks stay on the devices)", t.Name)
	s.redirect(w, r, "/templates", "Template removed. The checks it added stay on the devices.")
}

func (s *Server) handleTemplateCheckAdd(w http.ResponseWriter, r *http.Request) {
	t := s.loadTemplate(w, r)
	if t == nil {
		return
	}
	in := checkInput(r)
	if _, err := s.store.AddTemplateCheck(r.Context(), t.ID, in); err != nil {
		s.showTemplate(w, r, http.StatusBadRequest, t, in, storeErr(err))
		return
	}
	s.audit(r, "template.check", "template:"+strconv.FormatInt(t.ID, 10), "%s check added to template %q", in.Type, t.Name)
	s.redirect(w, r, "/templates/"+strconv.FormatInt(t.ID, 10), "Check added to the template. Apply the template to add it to devices.")
}

func (s *Server) handleTemplateCheckDelete(w http.ResponseWriter, r *http.Request) {
	t := s.loadTemplate(w, r)
	if t == nil {
		return
	}
	cid, ok := pathID(r, "cid")
	if !ok {
		s.notFound(w, r)
		return
	}
	if err := s.store.DeleteTemplateCheck(r.Context(), t.ID, cid); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "template.check", "template:"+strconv.FormatInt(t.ID, 10), "check #%d removed from template %q", cid, t.Name)
	s.redirect(w, r, "/templates/"+strconv.FormatInt(t.ID, 10), "Removed from the template. Devices keep their checks.")
}

// handleTemplateApply applies a template to a group, to all devices of its
// kinds, or again to the devices that have it (after it changed).
func (s *Server) handleTemplateApply(w http.ResponseWriter, r *http.Request) {
	t := s.loadTemplate(w, r)
	if t == nil {
		return
	}
	ctx := r.Context()
	back := "/templates/" + strconv.FormatInt(t.ID, 10)
	devs, err := s.store.Devices(ctx, store.DeviceFilter{})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	linked, _ := s.store.TemplateDevices(ctx, t.ID)
	scope := r.FormValue("scope")
	var targets []int64
	for _, d := range devs {
		switch {
		case scope == "kinds" && slices.Contains(t.Kinds, d.Kind),
			scope == "linked" && slices.Contains(linked, d.ID),
			len(scope) > 1 && scope[0] == 'g' && d.GroupID != nil && strconv.FormatInt(*d.GroupID, 10) == scope[1:]:
			targets = append(targets, d.ID)
		}
	}
	added, n := s.applyTemplate(r, t, targets)
	s.redirect(w, r, back, translate(s.lang(r, userFrom(r)), "Applied to %d devices; %d checks added.", n, added))
}

// applyTemplate applies a template to devices and records it in the audit
// log; it returns the checks added and the devices it was applied to.
func (s *Server) applyTemplate(r *http.Request, t *store.Template, ids []int64) (int, int) {
	added, n := 0, 0
	for _, id := range ids {
		a, err := s.store.ApplyTemplate(r.Context(), t.ID, id)
		added += a
		if err != nil {
			s.log.Warn("template", "template", t.ID, "device", id, "err", err)
			continue
		}
		n++
	}
	if added > 0 {
		s.poller.Reload()
	}
	s.audit(r, "template.apply", "template:"+strconv.FormatInt(t.ID, 10), "template %q applied to %d devices, %d checks added", t.Name, n, added)
	return added, n
}

// handleDeviceTemplate applies a template to one device.
func (s *Server) handleDeviceTemplate(w http.ResponseWriter, r *http.Request) {
	dev := s.loadDevice(w, r)
	if dev == nil {
		return
	}
	back := "/devices/" + strconv.FormatInt(dev.ID, 10)
	tid, _ := strconv.ParseInt(r.FormValue("template"), 10, 64)
	t, err := s.store.TemplateByID(r.Context(), tid)
	if err != nil {
		s.redirectErr(w, r, back, "Choose a template.")
		return
	}
	added, _ := s.applyTemplate(r, t, []int64{dev.ID})
	s.redirect(w, r, back, translate(s.lang(r, userFrom(r)), "Template applied; %d checks added.", added))
}
