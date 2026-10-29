package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/max-zit/watchzit/internal/store"
)

// The picture library: device pictures uploaded or copied from the
// built-in ones, with their ports set in the picture editor.

type picturesData struct {
	Pictures []store.Asset
	Types    []string
}

type pictureData struct {
	Pic   *store.Asset
	Ports string // the ports as JSON for the editor
	Types []string
}

// pictureTypeNames name the picture types in the forms.
var pictureTypeNames = map[string]string{"switch": "Switch", "router": "Router", "firewall": "Firewall", "server": "Server", "storage": "Storage / NAS",
	"accesspoint": "Access point", "ups": "UPS", "power": "Power", "camera": "Camera", "pc": "Computer", "printer": "Printer", "rack": "Rack",
	"logical": "Logical", "other": "Other"}

func (s *Server) handlePictures(w http.ResponseWriter, r *http.Request) {
	as, err := s.store.Assets(r.Context(), "icon")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "pictures", &Page{Title: "Picture library", Nav: "pictures", Flash: s.takeFlash(w, r), Data: &picturesData{Pictures: as, Types: store.PictureTypes}})
}

// handlePictureUpload stores an uploaded device picture and opens it in
// the editor.
func (s *Server) handlePictureUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, store.MaxAssetSize+64<<10)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		s.redirectErr(w, r, "/pictures", "the picture must be at most 8 MB")
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		s.redirectErr(w, r, "/pictures", "choose a picture")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, store.MaxAssetSize+1))
	if err != nil || len(data) > store.MaxAssetSize {
		s.redirectErr(w, r, "/pictures", "the picture must be at most 8 MB")
		return
	}
	mime, pw, ph, err := pictureType(data)
	if err != nil {
		s.redirectErr(w, r, "/pictures", err.Error())
		return
	}
	name := strings.TrimSuffix(hdr.Filename, filepathExt(hdr.Filename))
	id, err := s.store.CreatePicture(r.Context(), name, mime, data, pw, ph, store.PictureMeta{})
	if err != nil {
		s.redirectErr(w, r, "/pictures", storeErr(err))
		return
	}
	s.audit(r, "asset.create", "asset:"+strconv.FormatInt(id, 10), "icon picture %q uploaded (%s, %d bytes)", name, mime, len(data))
	http.Redirect(w, r, "/pictures/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// libIcon is an entry of the built-in picture index.
type libIcon struct {
	Name, Label, Cat, Kind, Vendor, Type string
	W, H, Show                           float64
	U, Fiber                             int
	Ports                                [][2]float64
}

var libNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,120}$`)

// libPicture finds a built-in picture and its drawing.
func libPicture(name string) (*libIcon, []byte, error) {
	if !libNameRE.MatchString(name) {
		return nil, nil, store.ErrNotFound
	}
	b, err := staticFS.ReadFile("static/devices/index.json")
	if err != nil {
		return nil, nil, err
	}
	var list []libIcon
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, nil, err
	}
	for i := range list {
		if list[i].Name == name {
			svg, err := staticFS.ReadFile("static/devices/" + name + ".svg")
			return &list[i], svg, err
		}
	}
	return nil, nil, store.ErrNotFound
}

// libType is the picture type of a built-in picture.
func libType(ic *libIcon) string {
	switch {
	case ic.Type != "":
		return ic.Type
	case ic.Kind != "" && ic.Kind != "other":
		return ic.Kind
	case ic.Cat == "rack" || ic.Cat == "logical" || ic.Cat == "power":
		return ic.Cat
	}
	return "other"
}

// handlePictureCopy copies a built-in picture into the library, to change it.
func (s *Server) handlePictureCopy(w http.ResponseWriter, r *http.Request) {
	ic, svg, err := libPicture(r.FormValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m := store.PictureMeta{Vendor: ic.Vendor, Type: libType(ic), U: ic.U, Show: ic.Show, Ports: ic.Ports, Fiber: ic.Fiber}
	id, err := s.store.CreatePicture(r.Context(), ic.Label, "image/svg+xml", svg, int(ic.W), int(ic.H), m)
	if err != nil {
		s.redirectErr(w, r, "/pictures", storeErr(err))
		return
	}
	s.audit(r, "asset.create", "asset:"+strconv.FormatInt(id, 10), "built-in picture %s copied", ic.Name)
	http.Redirect(w, r, "/pictures/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// loadPicture reads the device picture of the path.
func (s *Server) loadPicture(w http.ResponseWriter, r *http.Request) *store.Asset {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return nil
	}
	a, _, err := s.store.AssetData(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && a.Kind != "icon") {
		http.NotFound(w, r)
		return nil
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	return a
}

func (s *Server) handlePicture(w http.ResponseWriter, r *http.Request) {
	a := s.loadPicture(w, r)
	if a == nil {
		return
	}
	ports, _ := json.Marshal(a.Meta.Ports)
	if a.Meta.Ports == nil {
		ports = []byte("[]")
	}
	s.render(w, r, http.StatusOK, "picture", &Page{Title: "Picture library", Nav: "pictures", Flash: s.takeFlash(w, r),
		Data: &pictureData{Pic: a, Ports: string(ports), Types: store.PictureTypes}})
}

func (s *Server) handlePictureSave(w http.ResponseWriter, r *http.Request) {
	a := s.loadPicture(w, r)
	if a == nil {
		return
	}
	to := "/pictures/" + strconv.FormatInt(a.ID, 10)
	m := store.PictureMeta{Vendor: r.FormValue("vendor"), Type: r.FormValue("type"), U: formInt(r, "u"), Fiber: formInt(r, "fiber")}
	if v, err := strconv.ParseFloat(r.FormValue("show"), 64); err == nil {
		m.Show = v
	}
	if err := json.Unmarshal([]byte(r.FormValue("ports")), &m.Ports); err != nil && r.FormValue("ports") != "" {
		s.redirectErr(w, r, to, "the ports could not be read")
		return
	}
	if err := s.store.UpdatePicture(r.Context(), a.ID, r.FormValue("name"), m); err != nil {
		s.redirectErr(w, r, to, storeErr(err))
		return
	}
	s.audit(r, "asset.update", "asset:"+strconv.FormatInt(a.ID, 10), "picture %q: %d ports, %dU", r.FormValue("name"), len(m.Ports), m.U)
	s.redirect(w, r, to, "Saved.")
}

func (s *Server) handlePictureClone(w http.ResponseWriter, r *http.Request) {
	a := s.loadPicture(w, r)
	if a == nil {
		return
	}
	id, err := s.store.ClonePicture(r.Context(), a.ID, truncateName(a.Name+" "+translate(s.lang(r, userFrom(r)), "(copy)")))
	if err != nil {
		s.redirectErr(w, r, "/pictures", storeErr(err))
		return
	}
	s.audit(r, "asset.create", "asset:"+strconv.FormatInt(id, 10), "picture %q copied", a.Name)
	http.Redirect(w, r, "/pictures/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) handlePictureDelete(w http.ResponseWriter, r *http.Request) {
	a := s.loadPicture(w, r)
	if a == nil {
		return
	}
	if err := s.store.DeleteAsset(r.Context(), a.ID); err != nil {
		s.redirectErr(w, r, "/pictures", storeErr(err))
		return
	}
	s.audit(r, "asset.delete", "asset:"+strconv.FormatInt(a.ID, 10), "picture %q deleted", a.Name)
	s.redirect(w, r, "/pictures", "Picture deleted.")
}

// truncateName keeps a name within 100 bytes.
func truncateName(n string) string {
	r := []rune(n)
	for len(string(r)) > 100 {
		r = r[:len(r)-1]
	}
	return string(r)
}
