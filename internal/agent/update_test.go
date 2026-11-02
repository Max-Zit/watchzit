package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCleanUpdates: installers older than an hour go, newer ones and
// recent installer logs stay.
func TestCleanUpdates(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	files := map[string]time.Duration{
		"watchzit-agent-x64.msi":     2 * time.Hour,
		"watchzit-agent-linux-amd64": 10 * time.Minute,
		"update-1.2.0.log":           48 * time.Hour,
		"update-1.0.0.log":           40 * 24 * time.Hour,
	}
	for n, age := range files {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	cleanUpdates(dir, now)
	for n, keep := range map[string]bool{"watchzit-agent-x64.msi": false, "watchzit-agent-linux-amd64": true, "update-1.2.0.log": true, "update-1.0.0.log": false} {
		if _, err := os.Stat(filepath.Join(dir, n)); (err == nil) != keep {
			t.Errorf("%s: kept %v, want %v", n, err == nil, keep)
		}
	}
	cleanUpdates(filepath.Join(dir, "missing"), now) // no folder: nothing to do
}
