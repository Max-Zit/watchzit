package web_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/backup"
	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/testdb"
)

// Console backups: the daily schedule, an encrypted file to download, the
// number kept, and a restore of the file into an empty database.
func TestConsoleBackup(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.srv.DataDir = dir
	_ = os.MkdirAll(filepath.Join(dir, "tls"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "tls", "agent.key"), []byte("AGENT KEY"), 0o600)
	admin, _ := e.setupAdmin("192.168.1.10")
	ctx := context.Background()
	admin.post("/devices/new", "name", "backed-up-router", "address", "10.9.9.9")
	// metric samples in an hour partition and in the default one
	if _, err := e.st.Pool().Exec(ctx, `CREATE TABLE samples_hot_p2099010100 PARTITION OF samples_hot FOR VALUES FROM ('2099-01-01 00:00Z') TO ('2099-01-01 01:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Pool().Exec(ctx, `INSERT INTO samples_hot VALUES (1, '2099-01-01 00:30Z', 1.5), (2, '2000-01-01 00:00Z', 2.5)`); err != nil {
		t.Fatal(err)
	}
	const pass = "a long backup passphrase"
	hour := strconv.Itoa(e.now.Local().Hour())
	if r := admin.post("/settings/console-backup", "enabled", "1", "hour", hour, "keep", "2", "passphrase", "short", "passphrase2", "short"); !strings.Contains(admin.get(r.loc).body, "at least 12 characters") {
		t.Fatal("short passphrase accepted")
	}
	admin.post("/settings/console-backup", "enabled", "1", "hour", hour, "keep", "2", "passphrase", pass, "passphrase2", pass)

	names := regexp.MustCompile(`watchzit-backup-\d{8}-\d{6}\.wzb`)
	backups := func() []string {
		return uniq(names.FindAllString(admin.get("/settings/console-backup").body, -1))
	}
	e.srv.RunConsoleBackups(ctx) // the scheduled hour
	waitFor(t, "the scheduled backup", func() bool { return len(backups()) == 1 })
	e.srv.RunConsoleBackups(ctx) // once a day
	time.Sleep(300 * time.Millisecond)
	for i := 0; i < 2; i++ {
		e.now = e.now.Add(time.Second)
		admin.post("/settings/console-backup/run")
		want := min(i+2, 2)
		waitFor(t, "a manual backup", func() bool {
			b := admin.get("/settings/console-backup").body
			return !strings.Contains(b, "A backup is running") && len(backups()) == want && strings.Contains(b, e.now.Local().Format("20060102-150405"))
		})
	}
	list := backups()
	if len(list) != 2 {
		t.Fatalf("kept %v", list)
	}
	r := admin.get("/settings/console-backup/file/" + list[0])
	if r.code != 200 || bytes.Contains([]byte(r.body), []byte("AGENT KEY")) {
		t.Fatalf("download %d, encrypted %v", r.code, !strings.Contains(r.body, "AGENT KEY"))
	}
	if admin.get("/settings/console-backup/file/..%2Fsecret.key").code != 404 {
		t.Fatal("a name outside the list was served")
	}
	if _, err := backup.Inspect(strings.NewReader(r.body), "wrong passphrase"); !errors.Is(err, backup.ErrPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}

	url := testdb.URL(t)
	dst := t.TempDir()
	m, err := backup.Restore(ctx, strings.NewReader(r.body), pass, dst, func(ctx context.Context, rd io.Reader) error {
		return store.RestoreDump(ctx, url, rd)
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "tls", "agent.key")); string(b) != "AGENT KEY" || len(m.Files) != 1 {
		t.Fatalf("files %v", m.Files)
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ds, err := st.Devices(ctx, store.DeviceFilter{})
	if err != nil || len(ds) != 1 || ds[0].Name != "backed-up-router" {
		t.Fatalf("devices after restore: %+v %v", ds, err)
	}
	if ts, _ := st.Triggers(ctx); len(ts) == 0 {
		t.Fatal("no triggers after restore")
	}
	var n int
	var sum float64
	if err := st.Pool().QueryRow(ctx, `SELECT count(*), COALESCE(sum(value),0) FROM samples_hot`).Scan(&n, &sum); err != nil || n != 2 || sum != 4 {
		t.Fatalf("samples after restore: %d %v %v", n, sum, err)
	}

	admin.post("/settings/console-backup/delete", "name", list[1])
	if got := backups(); len(got) != 1 {
		t.Fatalf("after delete %v", got)
	}
	if b := admin.get("/audit").body; !strings.Contains(b, "backup.download") || !strings.Contains(b, "backup.create") {
		t.Fatal("backups not audited")
	}
}

func uniq(list []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range list {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
