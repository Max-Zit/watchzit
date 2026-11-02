package web_test

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiagnostics: the page shows the newest log lines and the zip has the
// logs and lists, both without secrets; only administrators get them.
func TestDiagnostics(t *testing.T) {
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	e.srv.DataDir = t.TempDir()
	logs := filepath.Join(e.srv.DataDir, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	secrets := []string{"hunter2pw", "s3cretcomm", "WZ1-abcdefghijklmnop", "Ab12Cd34Ef56Gh78Ij90Kl12Mn34Op56Qr"}
	line := "time=x level=WARN msg=\"login\" password=hunter2pw community=s3cretcomm code=WZ1-abcdefghijklmnop key Ab12Cd34Ef56Gh78Ij90Kl12Mn34Op56Qr\n" +
		"time=x level=INFO msg=\"poller started\"\n"
	if err := os.WriteFile(filepath.Join(logs, "server.log"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	c.post("/devices/new", "name", "router", "address", "10.0.0.1")

	p := c.get("/settings/diagnostics?level=warn")
	if p.code != http.StatusOK || !strings.Contains(p.body, "msg=&#34;login&#34;") || strings.Contains(p.body, "poller started") {
		t.Fatalf("page: %d %s", p.code, p.body)
	}
	z := c.get("/settings/diagnostics/zip")
	if z.code != http.StatusOK {
		t.Fatalf("zip: %d", z.code)
	}
	zr, err := zip.NewReader(bytes.NewReader([]byte(z.body)), int64(len(z.body)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	for _, n := range []string{"README.txt", "info.txt", "server.log", "devices.csv", "agents.csv", "problems.csv", "audit.csv"} {
		if _, ok := files[n]; !ok {
			t.Fatalf("%s missing from the zip", n)
		}
	}
	if !strings.Contains(files["devices.csv"], "router,10.0.0.1") || !strings.Contains(files["server.log"], "poller started") {
		t.Fatalf("zip content: %q / %q", files["devices.csv"], files["server.log"])
	}
	for _, s := range secrets {
		if strings.Contains(p.body, s) {
			t.Fatalf("secret %q on the page", s)
		}
		for n, b := range files {
			if strings.Contains(b, s) {
				t.Fatalf("secret %q in %s", s, n)
			}
		}
	}

	// not for other roles
	if r := e.client("192.168.1.11").get("/settings/diagnostics/zip"); r.code == http.StatusOK && strings.HasPrefix(r.body, "PK") {
		t.Fatal("zip without signing in")
	}
}
