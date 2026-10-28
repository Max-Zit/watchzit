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

// TestServiceChecks runs an HTTP and a PostgreSQL check through the poller;
// the database password is stored sealed and kept when a form leaves it empty.
func TestServiceChecks(t *testing.T) {
	ctx := context.Background()
	dbURL := testdb.URL(t)
	st, err := store.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UseSecretKey(bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := New(st, tsdb.New(st.Pool(), log), log)

	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("status: green")) }))
	defer web.Close()
	dev, _ := st.CreateDevice(ctx, store.DeviceInput{Name: "local", Address: "127.0.0.1"})
	hid, err := st.CreateCheck(ctx, dev, store.CheckInput{Type: store.CheckHTTP, Params: store.CheckParams{URL: web.URL + "/health", Contains: "green"}})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dbURL)
	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()
	pid, err := st.CreateCheck(ctx, dev, store.CheckInput{Type: store.CheckPostgres, Params: store.CheckParams{Port: port, User: u.User.Username(),
		Secret: pw, Database: strings.TrimPrefix(u.Path, "/")}})
	if err != nil {
		t.Fatal(err)
	}
	pc, _ := st.CheckByID(ctx, pid)
	if pc.Params.Secret == "" || pc.Params.Secret == pw {
		t.Fatal("the password is not sealed")
	}
	if pc.Name != "PostgreSQL" {
		t.Fatalf("name %q", pc.Name)
	}
	// saving the form without a password keeps the stored one
	if err := st.UpdateCheck(ctx, pid, store.CheckInput{Name: "DB", IntervalS: 60, Params: store.CheckParams{Port: port, User: u.User.Username(),
		Database: strings.TrimPrefix(u.Path, "/"), TimeoutMS: 5000}}); err != nil {
		t.Fatal(err)
	}
	pc, _ = st.CheckByID(ctx, pid)
	o := p.runCheck(ctx, pc)
	if o.res.Status != store.StatusUp || o.res.Values["connections"] < 1 || len(o.samples) != 2 {
		t.Fatalf("postgres: %+v", o.res)
	}
	hc, _ := st.CheckByID(ctx, hid)
	o = p.runCheck(ctx, hc)
	if o.res.Status != store.StatusUp || o.res.Values["status_code"] != 200 || !strings.HasPrefix(hc.Name, "HTTP 127.0.0.1") {
		t.Fatalf("http: %+v %q", o.res, hc.Name)
	}
	// bad settings are refused
	for _, in := range []store.CheckInput{
		{Type: store.CheckHTTP, Params: store.CheckParams{URL: "ftp://x"}},
		{Type: store.CheckDNS, Params: store.CheckParams{Query: "bad name!"}},
		{Type: store.CheckDNS, Params: store.CheckParams{Query: "example.com", RType: "ANY"}},
		{Type: store.CheckPostgres, Params: store.CheckParams{}},
		{Type: store.CheckJSON, Params: store.CheckParams{URL: "https://x/", Path: "a..b"}},
		{Type: store.CheckJSON, Params: store.CheckParams{URL: "https://x/", Path: "a", Auth: "magic"}},
	} {
		if _, err := st.CreateCheck(ctx, dev, in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
}
