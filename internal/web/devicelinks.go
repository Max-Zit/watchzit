package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/max-zit/watchzit/internal/store"
)

// Links on every device: address templates an administrator sets once
// (Settings → Tools), for example the device in RMMZit or the agents of
// BackupZit. {name}, {address} and {id} are replaced, URL-encoded.

const settingDeviceLinks = "device_links"

const maxDeviceLinks = 8

// DeviceLink is a link offered on every device page.
type DeviceLink struct {
	Name string `json:"name"`
	URL  string `json:"url"` // template
}

func (s *Server) deviceLinks(ctx context.Context) []DeviceLink {
	var l []DeviceLink
	_ = s.store.GetSetting(ctx, settingDeviceLinks, &l)
	return l
}

var linkFields = strings.NewReplacer("{name}", "x", "{address}", "x", "{id}", "1")

// cleanDeviceLink checks a link template.
func cleanDeviceLink(l DeviceLink) (DeviceLink, error) {
	l.Name, l.URL = strings.TrimSpace(l.Name), strings.TrimSpace(l.URL)
	u, err := url.Parse(linkFields.Replace(l.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(l.URL) > 500 {
		return l, uiErrf("%s: the address must start with http:// or https://", l.Name)
	}
	if l.Name == "" || len(l.Name) > 60 || strings.ContainsAny(l.Name, "\x00\r\n\t") {
		return l, uiErr("A link needs a name of up to 60 characters.")
	}
	return l, nil
}

// linkFor fills a template for a device.
func linkFor(l DeviceLink, d *store.Device) DeviceLink {
	r := strings.NewReplacer("{name}", url.QueryEscape(d.Name), "{address}", url.QueryEscape(d.Address), "{id}", strconv.FormatInt(d.ID, 10))
	return DeviceLink{Name: l.Name, URL: r.Replace(l.URL)}
}

func (s *Server) handleDeviceLinksSave(w http.ResponseWriter, r *http.Request) {
	const back = "/settings/tools#links"
	_ = r.ParseForm()
	names, urls := r.Form["link_name"], r.Form["link_url"]
	var out []DeviceLink
	for i := range urls {
		l := DeviceLink{URL: urls[i]}
		if i < len(names) {
			l.Name = names[i]
		}
		if strings.TrimSpace(l.URL) == "" {
			continue
		}
		l, err := cleanDeviceLink(l)
		if err != nil {
			s.redirectErr(w, r, back, s.errText(r, err))
			return
		}
		out = append(out, l)
	}
	if len(out) > maxDeviceLinks {
		s.redirectErr(w, r, back, "At most 8 links.")
		return
	}
	if err := s.store.SetSetting(r.Context(), settingDeviceLinks, out); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "settings.device-links", "settings", "%d links on every device", len(out))
	s.redirect(w, r, back, "Saved.")
}
