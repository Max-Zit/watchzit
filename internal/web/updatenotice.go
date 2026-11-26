package web

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/update"
)

// The update notice: administrators (the only role that may install
// updates) see a newer WatchZit version at the top of every page. The
// version comes from the regular, signature-checked release check; nothing
// else is fetched. The × hides it for that user for five days, stored in
// the database so it holds in every browser; a version newer than the
// hidden one shows it again at once.

const updateNoticePause = 5 * 24 * time.Hour

// updateNotice is what the layout shows.
type updateNotice struct {
	Version   string // the newer release
	Installed string
	HelperOK  bool // the console can install it (Linux packages with the helper)
	NotesURL  string
}

// releaseNotesURL is the public page of a release. Every release the
// console accepts carries the WatchZit signature, so it is published there.
func releaseNotesURL(version string) string {
	return "https://github.com/Max-Zit/watchzit/releases/tag/v" + version
}

// showUpdateNotice decides whether a release is shown to a user who hid
// the notice for hiddenVersion until hiddenUntil.
func showUpdateNotice(available, hiddenVersion string, hiddenUntil *time.Time, now time.Time) bool {
	switch {
	case available == "":
		return false
	case hiddenVersion == "" || hiddenUntil == nil:
		return true
	case update.Newer(available, hiddenVersion):
		return true
	default:
		return !now.Before(*hiddenUntil)
	}
}

// availableRelease is the newer release the last check found, if any.
func (s *Server) availableRelease() string {
	s.upd.mu.Lock()
	m := s.upd.latest
	s.upd.mu.Unlock()
	if m == nil || !update.Newer(m.Version, s.Version) {
		return ""
	}
	return m.Version
}

// updateNoticeFor returns the notice for u on the page at path, or nil.
func (s *Server) updateNoticeFor(ctx context.Context, u *store.User, path string) *updateNotice {
	if !u.IsAdmin() || path == "/settings/version" {
		return nil
	}
	v := s.availableRelease()
	if v == "" {
		return nil
	}
	hv, until, err := s.store.UpdateNoticeHidden(ctx, u.ID)
	if err != nil {
		s.log.Warn("update notice", "err", err)
		hv, until = "", nil
	}
	if !showUpdateNotice(v, hv, until, s.now()) {
		return nil
	}
	return &updateNotice{Version: v, Installed: s.Version, HelperOK: s.helperOK(), NotesURL: releaseNotesURL(v)}
}

// handleUpdateNoticeHide serves POST /settings/version/hide-notice and
// returns to the page the notice was on (the form names it; else the Referer).
func (s *Server) handleUpdateNoticeHide(w http.ResponseWriter, r *http.Request) {
	if u, v := userFrom(r), s.availableRelease(); u != nil && v != "" {
		if err := s.store.HideUpdateNotice(r.Context(), u.ID, v, s.now().Add(updateNoticePause)); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	back := refererPath(r)
	if b := r.FormValue("back"); b != "" {
		back = safeNext(b)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// refererPath is the local path of the page a form was sent from: only a
// Referer of this console counts, anything else returns to the overview
// (no open redirect).
func refererPath(r *http.Request) string {
	ref, err := url.Parse(r.Referer())
	if err != nil || ref.Host != r.Host || ref.Path == "" {
		return "/"
	}
	p := ref.EscapedPath()
	if ref.RawQuery != "" {
		p += "?" + ref.RawQuery
	}
	return safeNext(p)
}
