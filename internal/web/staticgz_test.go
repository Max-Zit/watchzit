package web

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The static text files go out gzip-compressed when the browser accepts it.
func TestStaticGzip(t *testing.T) {
	h := gzipStatic(os.DirFS("static"), http.FileServer(http.Dir("static")))
	r := httptest.NewRequest("GET", "/devices/index.json", nil)
	r.Header.Set("Accept-Encoding", "gzip, deflate")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Content-Encoding") != "gzip" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("headers %v", w.Header())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil || !json.Valid(raw) {
		t.Fatalf("body: %v", err)
	}
	// without gzip the file server answers as before
	r = httptest.NewRequest("GET", "/devices/index.json", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Content-Encoding") != "" || !json.Valid(w.Body.Bytes()) {
		t.Fatal("plain request was compressed")
	}
}

func TestVLANRanges(t *testing.T) {
	if got := vlanRanges([]int{1, 2, 3, 4, 10, 20, 21, 22, 30}); got != "1-4, 10, 20-22, 30" {
		t.Fatal(got)
	}
	if vlanRanges(nil) != "" {
		t.Fatal("empty")
	}
}
