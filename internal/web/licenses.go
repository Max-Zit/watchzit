package web

import (
	"net/http"
	"os"
)

// The licenses page: WatchZit's own license (AGPLv3) and the licenses of
// the libraries and icons in the programs, from the THIRD_PARTY_LICENSES
// file the packages install next to the documentation.

// LicenseFiles are where the packages put the third-party licenses.
var LicenseFiles = []string{
	"/usr/share/doc/watchzit-server/THIRD_PARTY_LICENSES.md",
	"THIRD_PARTY_LICENSES.md",
}

type licensesData struct {
	Text string // the third-party licenses ("" when the file is missing)
}

func (s *Server) handleLicenses(w http.ResponseWriter, r *http.Request) {
	d := &licensesData{}
	for _, p := range LicenseFiles {
		if b, err := os.ReadFile(p); err == nil { // #nosec G304 -- fixed paths
			d.Text = string(b)
			break
		}
	}
	s.render(w, r, http.StatusOK, "licenses", &Page{Title: "Licenses", Data: d})
}
