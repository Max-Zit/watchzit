package poller

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/testdb"
	"github.com/max-zit/watchzit/internal/tsdb"
)

// TestHypervisorCheck: a Proxmox VE check stores its values and the
// inventory; the second run keeps the guest data read in the first.
func TestHypervisorCheck(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UseSecretKey(bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(st, tsdb.New(st.Pool(), log), log)
	configs := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "PVEAPIToken=wz@pve!mon=tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api2/json/cluster/resources":
			_, _ = io.WriteString(w, `{"data":[{"type":"node","node":"pve","status":"online","cpu":0.1,"maxcpu":4,"mem":1,"maxmem":4},{"type":"qemu","vmid":101,"name":"app","node":"pve","status":"running","cpu":0.3,"maxmem":100,"mem":50}]}`)
		case "/api2/json/nodes/pve/qemu/101/config":
			configs++
			_, _ = io.WriteString(w, `{"data":{"ostype":"win10"}}`)
		default:
			_, _ = io.WriteString(w, `{"data":{}}`)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	dev, _ := st.CreateDevice(ctx, store.DeviceInput{Name: "pve", Address: "127.0.0.1"})
	id, err := st.CreateCheck(ctx, dev, store.CheckInput{Type: store.CheckHypervisor, Params: store.CheckParams{HV: "proxmox", Port: port, User: "wz@pve!mon", Secret: "tok", Insecure: true}})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := st.CheckByID(ctx, id)
	o := p.runCheck(ctx, c)
	if o.res.Status != store.StatusUp || o.res.Values["vms_running"] != 1 || o.res.Values["cpu_pct"] != 10 || !strings.Contains(o.res.Text, "1 of 1 machines running") {
		t.Fatalf("run: %+v", o.res)
	}
	o = p.runCheck(ctx, c)
	inv, err := st.HVInventoryOf(ctx, dev)
	if err != nil || inv == nil || len(inv.VMs) != 1 || inv.VMs[0].OS != "win10" || inv.VMs[0].CPUPct != 30 || configs != 1 {
		t.Fatalf("inventory: %v %+v (configs read %d)", err, inv, configs)
	}
}
