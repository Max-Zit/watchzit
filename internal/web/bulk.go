package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/max-zit/watchzit/internal/store"
)

// handleDevicesBulk changes the devices ticked in the device list: group,
// SNMP profile, the device they depend on, type, monitoring on or off, a
// template, or deletes them.
func (s *Server) handleDevicesBulk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_ = r.ParseForm()
	back := "/devices"
	if q := r.FormValue("back"); strings.HasPrefix(q, "/devices") && !strings.HasPrefix(q, "//") {
		back = q
	}
	var ids []int64
	for _, v := range r.Form["id"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 && len(ids) < 5000 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		s.redirectErr(w, r, back, "Tick the devices to change.")
		return
	}
	lang := s.lang(r, userFrom(r))
	action := r.FormValue("action")
	num := func(k string) int64 { n, _ := strconv.ParseInt(r.FormValue(k), 10, 64); return n }
	var n int
	var err error
	switch action {
	case store.BulkGroup, store.BulkSNMP, store.BulkParent:
		n, err = s.store.BulkUpdate(ctx, ids, action, num(action), "")
	case store.BulkKind:
		n, err = s.store.BulkUpdate(ctx, ids, action, 0, r.FormValue("kind"))
	case "pause", "resume":
		v := int64(0)
		if action == "pause" {
			v = 1
		}
		n, err = s.store.BulkUpdate(ctx, ids, store.BulkPause, v, "")
	case "template":
		t, terr := s.store.TemplateByID(ctx, num("template"))
		if terr != nil {
			s.redirectErr(w, r, back, "Choose a template.")
			return
		}
		added, done := s.applyTemplate(r, t, ids)
		s.redirect(w, r, back, translate(lang, "Applied to %d devices; %d checks added.", done, added))
		return
	case "delete":
		if !userFrom(r).IsAdmin() {
			s.forbidden(w, r)
			return
		}
		for _, id := range ids {
			d, derr := s.store.DeviceByID(ctx, id)
			if derr != nil {
				continue
			}
			series, derr := s.store.DeleteDevice(ctx, id)
			if derr != nil {
				err = derr
				break
			}
			s.disconnectAgent(id)
			_ = s.tsdb.Forget(ctx, series)
			s.audit(r, "device.delete", "device:"+strconv.FormatInt(id, 10), "deleted %s (%s) with its checks and data (bulk)", d.Name, d.Address)
			n++
		}
	default:
		s.redirectErr(w, r, back, "Unknown action.")
		return
	}
	s.poller.Reload()
	if err != nil {
		s.redirectErr(w, r, back, storeErr(err))
		return
	}
	if action != "delete" {
		s.audit(r, "device.bulk", "devices", "%s changed on %d devices (%v)", action, n, ids)
	}
	s.redirect(w, r, back, translate(lang, "%d devices changed.", n))
}
