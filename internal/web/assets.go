package web

import (
	"bytes"
	"errors"
	"image"
	_ "image/gif"  // decoders for the size of uploaded pictures
	_ "image/jpeg" // (the pictures themselves are served as uploaded)
	_ "image/png"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/max-zit/watchzit/internal/store"
)

var svgSizeRE = regexp.MustCompile(`(?is)<svg[^>]*?\sviewBox\s*=\s*["']\s*[-\d.]+[\s,]+[-\d.]+[\s,]+([\d.]+)[\s,]+([\d.]+)`)
var svgWHRE = regexp.MustCompile(`(?is)<svg[^>]*?\swidth\s*=\s*["']([\d.]+)(?:px)?["'][^>]*?\sheight\s*=\s*["']([\d.]+)(?:px)?["']`)

// pictureType checks an upload: PNG, JPEG, GIF, WebP or SVG. SVG files are
// shown only as images (no scripts run there) and served sandboxed.
func pictureType(data []byte) (mime string, w, h int, err error) {
	ct := http.DetectContentType(data)
	switch {
	case ct == "image/png" || ct == "image/jpeg" || ct == "image/gif":
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return "", 0, 0, errors.New("the picture cannot be read")
		}
		return ct, cfg.Width, cfg.Height, nil
	case ct == "image/webp":
		return ct, 0, 0, nil
	}
	head := strings.ToLower(string(data[:min(len(data), 4096)]))
	if strings.Contains(head, "<svg") && (strings.HasPrefix(strings.TrimSpace(head), "<?xml") || strings.HasPrefix(strings.TrimSpace(head), "<svg") || strings.HasPrefix(strings.TrimSpace(head), "<!--")) {
		if m := svgSizeRE.FindSubmatch(data); m != nil {
			w, _ := strconv.ParseFloat(string(m[1]), 64)
			h, _ := strconv.ParseFloat(string(m[2]), 64)
			return "image/svg+xml", int(w), int(h), nil
		}
		if m := svgWHRE.FindSubmatch(data); m != nil {
			w, _ := strconv.ParseFloat(string(m[1]), 64)
			h, _ := strconv.ParseFloat(string(m[2]), 64)
			return "image/svg+xml", int(w), int(h), nil
		}
		return "image/svg+xml", 0, 0, nil
	}
	return "", 0, 0, errors.New("use a PNG, JPEG, GIF, WebP or SVG picture")
}

// handleAssetUpload stores a background or icon (multipart form).
func (s *Server) handleAssetUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, store.MaxAssetSize+64<<10)
	lang := s.lang(r, userFrom(r))
	fail := func(msg string) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": translate(lang, msg)})
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		fail("the picture must be at most 8 MB")
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		fail("choose a picture")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, store.MaxAssetSize+1))
	if err != nil || len(data) > store.MaxAssetSize {
		fail("the picture must be at most 8 MB")
		return
	}
	mime, pw, ph, err := pictureType(data)
	if err != nil {
		fail(err.Error())
		return
	}
	kind := r.FormValue("kind")
	name := strings.TrimSuffix(hdr.Filename, filepathExt(hdr.Filename))
	id, err := s.store.CreateAsset(r.Context(), kind, name, mime, data, pw, ph)
	if err != nil {
		fail(storeErr(err))
		return
	}
	s.audit(r, "asset.create", "asset:"+strconv.FormatInt(id, 10), "%s picture %q uploaded (%s, %d bytes)", kind, name, mime, len(data))
	writeJSON(w, http.StatusOK, store.Asset{ID: id, Kind: kind, Name: name, Mime: mime, Width: pw, Height: ph, Size: len(data)})
}

func filepathExt(n string) string {
	if i := strings.LastIndexByte(n, '.'); i > 0 && len(n)-i <= 6 {
		return n[i:]
	}
	return ""
}

func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	kind := r.FormValue("kind")
	if kind != "icon" && kind != "background" {
		kind = "icon"
	}
	as, err := s.store.Assets(r.Context(), kind)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, as)
}

// serveAsset sends a picture. SVG gets a sandbox so that opening it on its
// own cannot run anything in the console's origin.
func (s *Server) serveAsset(w http.ResponseWriter, r *http.Request, id int64) {
	a, data, err := s.store.AssetData(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.Mime)
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Disposition", "inline")
	h.Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(data)
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.serveAsset(w, r, id)
}

func (s *Server) handleAssetDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := s.store.DeleteAsset(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrInUse):
		writeJSON(w, http.StatusConflict, map[string]string{"error": translate(s.lang(r, userFrom(r)), "the picture is still used on a map")})
		return
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.audit(r, "asset.delete", "asset:"+strconv.FormatInt(id, 10), "picture deleted")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
