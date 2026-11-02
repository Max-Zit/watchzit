package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// Custom fields of devices: defined by administrators under Settings,
// filled in on the device page; the device search finds their values.

// fieldValue checks and normalizes a value of a field.
func fieldValue(f store.CustomField, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if len(v) > 500 || strings.ContainsAny(v, "\x00\r\n") {
		return "", uiErrf("%s: at most 500 characters on one line", f.Name)
	}
	switch f.Kind {
	case "number":
		v = strings.ReplaceAll(v, ",", ".")
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return "", uiErrf("%s: enter a number", f.Name)
		}
	case "date":
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return "", uiErrf("%s: enter a date as YYYY-MM-DD", f.Name)
		}
	}
	return v, nil
}

func (s *Server) handleCustomFields(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.CustomFields(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "customfields", &Page{Title: "Custom fields", Nav: "settings", Data: list})
}

func (s *Server) handleCustomFieldsSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	const back = "/settings/fields"
	switch r.FormValue("action") {
	case "add":
		name, kind := strings.TrimSpace(r.FormValue("name")), r.FormValue("kind")
		if _, err := s.store.AddCustomField(ctx, name, kind); errors.Is(err, store.ErrConflict) {
			s.redirectErr(w, r, back, "A field with this name exists.")
			return
		} else if err != nil {
			s.redirectErr(w, r, back, s.errText(r, err))
			return
		}
		s.audit(r, "fields.add", "settings", "custom field %q (%s) added", name, kind)
		s.redirect(w, r, back, "Saved.")
	case "delete":
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		if err := s.store.DeleteCustomField(ctx, id); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.audit(r, "fields.delete", "settings", "custom field #%d and its values removed", id)
		s.redirect(w, r, back, "Removed.")
	default:
		s.redirectErr(w, r, back, "Unknown action.")
	}
}

func (s *Server) handleDeviceFields(w http.ResponseWriter, r *http.Request) {
	d := s.loadDevice(w, r)
	if d == nil {
		return
	}
	to := "/devices/" + strconv.FormatInt(d.ID, 10)
	fields, err := s.store.CustomFields(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	_ = r.ParseForm()
	vals := map[int64]string{}
	for _, f := range fields {
		raw, ok := r.Form["f"+strconv.FormatInt(f.ID, 10)]
		if !ok || len(raw) == 0 {
			continue
		}
		v, err := fieldValue(f, raw[0])
		if err != nil {
			s.redirectErr(w, r, to, s.errText(r, err))
			return
		}
		vals[f.ID] = v
	}
	if err := s.store.SetFieldValues(r.Context(), d.ID, vals); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "device.fields", "device:"+strconv.FormatInt(d.ID, 10), "custom fields of %q changed", d.Name)
	s.redirect(w, r, to, "Saved.")
}
