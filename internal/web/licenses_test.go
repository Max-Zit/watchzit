package web_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/max-zit/watchzit/internal/web"
)

// TestLicensesPage: every signed-in user sees the license and the
// third-party notices from the file the packages install.
func TestLicensesPage(t *testing.T) {
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	old := web.LicenseFiles
	defer func() { web.LicenseFiles = old }()
	web.LicenseFiles = []string{filepath.Join(t.TempDir(), "missing.md")}
	if b := c.get("/licenses").body; !strings.Contains(b, "GNU Affero General Public License") || !strings.Contains(b, "is not on this server") {
		t.Fatal("page without the file")
	}
	p := filepath.Join(t.TempDir(), "THIRD_PARTY_LICENSES.md")
	if err := os.WriteFile(p, []byte("## github.com/coder/websocket (ISC) <b>x</b>"), 0o600); err != nil {
		t.Fatal(err)
	}
	web.LicenseFiles = []string{p}
	b := c.get("/licenses").body
	if !strings.Contains(b, "coder/websocket (ISC)") || strings.Contains(b, "<b>x</b>") {
		t.Fatal("notices missing or not escaped")
	}
	if !strings.Contains(c.get("/").body, `href="/licenses"`) {
		t.Fatal("no link to the licenses")
	}
}
