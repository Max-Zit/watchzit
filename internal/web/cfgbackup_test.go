package web_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/max-zit/watchzit/internal/cfgbackup"
	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/web"
)

// TestConfigBackup: a device's configuration is read, kept as a version
// only when it changed, compared with the one before, and the change goes
// to the device's log; the password is sealed and viewers see nothing.
func TestConfigBackup(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "core", "address", "10.0.0.1", "ping", "1")
	conf := "/system identity set name=core\n/ip address add address=10.0.0.1/24 interface=ether1\n"
	var got cfgbackup.Target
	old := web.FetchConfig
	web.FetchConfig = func(_ context.Context, t cfgbackup.Target) (*cfgbackup.Result, error) {
		got = t
		return &cfgbackup.Result{Text: conf, HostKey: "ssh-ed25519 AAAAlab"}, nil
	}
	defer func() { web.FetchConfig = old }()

	if r := c.post("/devices/1/backup", "method", "mikrotik", "user", "backup", "port", "22", "enabled", "1"); !strings.Contains(c.get(r.loc).body, "password or a private key") {
		t.Fatal("a backup without a password was accepted")
	}
	if r := c.post("/devices/1/backup", "method", "mikrotik", "user", "backup", "port", "22", "secret", "s3cret!", "enabled", "1"); r.code != 303 {
		t.Fatalf("settings: %d", r.code)
	}
	var sealed string
	_ = e.st.Pool().QueryRow(ctx, `SELECT secret FROM device_backup WHERE device_id=1`).Scan(&sealed)
	if !strings.HasPrefix(sealed, "v1:") {
		t.Fatal("the password is not sealed")
	}
	c.post("/devices/1/backup/run")
	if got.Password != "s3cret!" || got.Method != "mikrotik" || got.Address != "10.0.0.1" {
		t.Fatalf("target: %+v", got)
	}
	c.post("/devices/1/backup/run") // unchanged: no new version
	conf = strings.Replace(conf, "name=core", "name=core-1", 1)
	c.post("/devices/1/backup/run")
	vs, _ := e.st.ConfigVersions(ctx, 1)
	if len(vs) != 2 || vs[0].Added != 1 || vs[0].Removed != 1 {
		t.Fatalf("versions: %+v", vs)
	}
	b, _ := e.st.DeviceBackupOf(ctx, 1)
	if b.HostKey != "ssh-ed25519 AAAAlab" || b.LastError != "" {
		t.Fatalf("backup state: %+v", b)
	}
	r := c.get("/devices/1/configs/" + strconv.FormatInt(vs[0].ID, 10))
	if !strings.Contains(r.body, "+ /system identity set name=core-1") || !strings.Contains(r.body, "- /system identity set name=core") {
		t.Fatalf("diff page")
	}
	if r := c.get("/devices/1/configs/" + strconv.FormatInt(vs[0].ID, 10) + "?download=1"); r.body != conf || !strings.Contains(r.hdr.Get("Content-Disposition"), "core-") {
		t.Fatalf("download: %q", r.body)
	}
	if r := c.get("/devices/1"); !strings.Contains(r.body, "Configuration backups") || !strings.Contains(r.body, "+1") {
		t.Fatal("device page lacks the backups")
	}
	if ls, _ := e.st.Logs(ctx, store.LogFilter{DeviceID: 1}); len(ls) != 1 || !strings.Contains(ls[0].Message, "configuration changed: +1") {
		t.Fatalf("log: %+v", ls)
	}
	// a failure is recorded and logged
	web.FetchConfig = func(context.Context, cfgbackup.Target) (*cfgbackup.Result, error) { return nil, cfgbackup.ErrHostKey }
	if r := c.post("/devices/1/backup/run"); !strings.Contains(c.get(r.loc).body, "host key of the device changed") {
		t.Fatal("no message for a changed host key")
	}
	if b, _ := e.st.DeviceBackupOf(ctx, 1); b.LastError == "" {
		t.Fatal("the failure was not recorded")
	}

	// viewers neither see configurations nor the backup settings
	c.post("/users", "username", "watcher", "password", "watcher password 1", "role", "viewer")
	v := e.client("192.168.1.30")
	v.post("/login", "username", "watcher", "password", "watcher password 1")
	e.enroll(v)
	v.post("/account/password", "current", "watcher password 1", "password", "new watcher pass 1", "password2", "new watcher pass 1")
	if r := v.get("/devices/1/configs/" + strconv.FormatInt(vs[0].ID, 10)); r.code != 403 {
		t.Fatalf("viewer read a configuration: %d", r.code)
	}
	if r := v.get("/devices/1"); strings.Contains(r.body, "Configuration backups") {
		t.Fatal("viewer sees the backups")
	}
}
