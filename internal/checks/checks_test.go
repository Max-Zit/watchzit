package checks

import (
	"context"
	"fmt"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/max-zit/watchzit/internal/store"
	"github.com/max-zit/watchzit/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

var local = Target{Host: "127.0.0.1", Addr: netip.MustParseAddr("127.0.0.1")}

func run(typ string, p store.CheckParams, t Target) Result {
	return Run(context.Background(), typ, p, t, 5*time.Second)
}

func TestHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("<h1>Welcome to the shop</h1>"))
	}))
	defer srv.Close()
	r := run(store.CheckHTTP, store.CheckParams{URL: srv.URL + "/", Contains: "shop"}, local)
	if !r.Up || r.Values["status_code"] != 200 || !strings.HasPrefix(r.Text, "200 OK") {
		t.Fatalf("page: %+v", r)
	}
	if r := run(store.CheckHTTP, store.CheckParams{URL: srv.URL + "/", Contains: "basket"}, local); r.Up || !strings.Contains(r.Text, `"basket" not found`) {
		t.Fatalf("missing text: %+v", r)
	}
	if r := run(store.CheckHTTP, store.CheckParams{URL: srv.URL + "/missing"}, local); r.Up || r.Values["status_code"] != 404 {
		t.Fatalf("404: %+v", r)
	}
	if r := run(store.CheckHTTP, store.CheckParams{URL: srv.URL + "/missing", Expect: 404}, local); !r.Up {
		t.Fatalf("expected 404: %+v", r)
	}
	// HTTPS with a certificate the system does not trust
	tsrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tsrv.Close()
	if r := run(store.CheckHTTP, store.CheckParams{URL: tsrv.URL}, local); r.Up || !strings.Contains(r.Text, "certificate") {
		t.Fatalf("untrusted: %+v", r)
	}
	r = run(store.CheckHTTP, store.CheckParams{URL: tsrv.URL, Insecure: true}, local)
	if !r.Up || r.Values["cert_days"] < 300 {
		t.Fatalf("insecure: %+v", r)
	}
	// nothing listening
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close()
	if r := run(store.CheckHTTP, store.CheckParams{URL: "http://" + addr}, local); r.Up || r.Text == "" {
		t.Fatalf("closed: %+v", r)
	}
}

func TestTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	port, _ := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	r := run(store.CheckTLS, store.CheckParams{Port: port}, local)
	if r.Up || !strings.Contains(r.Text, "not trusted") || r.Values["cert_days"] < 300 {
		t.Fatalf("untrusted: %+v", r)
	}
	if r := run(store.CheckTLS, store.CheckParams{Port: port, Insecure: true}, local); !r.Up || !strings.Contains(r.Text, "valid until") {
		t.Fatalf("insecure: %+v", r)
	}
}

// dnsServer answers A queries for one name.
func dnsServer(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var q dnsmessage.Message
			if q.Unpack(buf[:n]) != nil || len(q.Questions) == 0 {
				continue
			}
			resp := dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true}, Questions: q.Questions}
			if strings.EqualFold(q.Questions[0].Name.String(), "shop.example.") {
				resp.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body: &dnsmessage.AResource{A: [4]byte{192, 0, 2, 7}}}}
			} else {
				resp.RCode = dnsmessage.RCodeNameError
			}
			out, _ := resp.Pack()
			_, _ = pc.WriteTo(out, from)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

func TestDNS(t *testing.T) {
	port := dnsServer(t)
	r := run(store.CheckDNS, store.CheckParams{Port: port, Query: "shop.example", RType: "A", Contains: "192.0.2.7"}, local)
	if !r.Up || !strings.HasPrefix(r.Text, "192.0.2.7") {
		t.Fatalf("A: %+v", r)
	}
	if r := run(store.CheckDNS, store.CheckParams{Port: port, Query: "shop.example", RType: "A", Contains: "10.0.0.1"}, local); r.Up {
		t.Fatalf("wrong answer accepted: %+v", r)
	}
	if r := run(store.CheckDNS, store.CheckParams{Port: port, Query: "nope.example", RType: "A"}, local); r.Up || !strings.Contains(r.Text, "NameError") {
		t.Fatalf("nxdomain: %+v", r)
	}
}

func TestPostgres(t *testing.T) {
	u, _ := url.Parse(testdb.URL(t))
	port, _ := strconv.Atoi(u.Port())
	pw, _ := u.User.Password()
	p := store.CheckParams{Port: port, User: u.User.Username(), Database: strings.TrimPrefix(u.Path, "/")}
	r := run(store.CheckPostgres, p, Target{Host: "127.0.0.1", Addr: local.Addr, Secret: pw})
	if !r.Up || r.Values["connections"] < 1 || !strings.HasPrefix(r.Text, "PostgreSQL ") {
		t.Fatalf("postgres: %+v", r)
	}
	if r := run(store.CheckPostgres, p, Target{Host: "127.0.0.1", Addr: local.Addr, Secret: "wrong"}); r.Up || !strings.Contains(r.Text, "password") {
		t.Fatalf("wrong password: %+v", r)
	}
}

func TestMySQLRefused(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	if r := run(store.CheckMySQL, store.CheckParams{Port: port, User: "monitor"}, local); r.Up || r.Text == "" {
		t.Fatalf("mysql: %+v", r)
	}
}

func TestRouterOS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		if u != "monitor" || p != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/rest/system/resource":
			_ = json.NewEncoder(w).Encode(map[string]string{"cpu-load": "7", "free-memory": "805306368", "total-memory": "1073741824",
				"uptime": "1w2d3h4m5s", "version": "7.25 (stable)", "board-name": "CHR"})
		case "/rest/system/health":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "cpu-temperature", "value": "41", "type": "C"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	p := store.CheckParams{Port: port, User: "monitor"}
	r := run(store.CheckRouterOS, p, Target{Host: "127.0.0.1", Secret: "s3cret"})
	if !r.Up || r.Values["cpu_pct"] != 7 || r.Values["mem_pct"] != 25 || r.Values["uptime_s"] != 788645 || r.Values["temperature"] != 41 {
		t.Fatalf("routeros: %+v", r)
	}
	if !strings.Contains(r.Text, "RouterOS 7.25 CHR") {
		t.Fatalf("text %q", r.Text)
	}
	if r := run(store.CheckRouterOS, p, Target{Host: "127.0.0.1", Secret: "x"}); r.Up || !strings.Contains(r.Text, "wrong user or password") {
		t.Fatalf("bad password: %+v", r)
	}
}

func TestRouterOSVLANs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/interface/bridge/vlan":
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"bridge": "br1", "vlan-ids": "10", "current-tagged": "br1,ether2,sfp1", "current-untagged": "", "disabled": "false"},
				{"bridge": "br1", "vlan-ids": "20,30-31", "current-tagged": "ether2", "current-untagged": "ether3", "comment": "phones", "disabled": "false"},
				{"bridge": "br1", "vlan-ids": "99", "current-tagged": "", "current-untagged": "", "disabled": "false", "dynamic": "true", "comment": "added by pvid"},
				{"bridge": "br1", "vlan-ids": "50", "current-tagged": "ether2", "disabled": "true"},
			})
		case "/rest/interface/vlan":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "mgmt", "vlan-id": "10", "interface": "br1"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	vs, err := RouterOSVLANs(context.Background(), store.CheckParams{Port: port, User: "monitor"}, Target{Host: "127.0.0.1", Secret: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(vs)
	want := "[{10 mgmt ether2 true} {10 mgmt sfp1 true} {20 phones ether2 true} {20 phones ether3 false} {30 phones ether2 true} {30 phones ether3 false} {31 phones ether2 true} {31 phones ether3 false} {99   false}]"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if fmt.Sprint(rosIDs("1,5-7,0,5000,x")) != "[1 5 6 7]" {
		t.Fatal(rosIDs("1,5-7,0,5000,x"))
	}
}

func TestJSONValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"results":{"cpu":[{"current":"12.5"},{"current":30}]},"ok":true}`))
	}))
	defer srv.Close()
	p := store.CheckParams{URL: srv.URL, Auth: "bearer", Path: "results.cpu.1.current", Unit: "%"}
	r := run(store.CheckJSON, p, Target{Secret: "tok"})
	if !r.Up || r.Values["value"] != 30 || r.Text != "30 %" {
		t.Fatalf("json: %+v", r)
	}
	p.Path, p.Scale = "results.cpu.0.current", 2
	if r := run(store.CheckJSON, p, Target{Secret: "tok"}); r.Values["value"] != 25 {
		t.Fatalf("scaled string: %+v", r)
	}
	p.Path = "results.memory"
	if r := run(store.CheckJSON, p, Target{Secret: "tok"}); r.Up || !strings.Contains(r.Text, `"memory" is not in the answer`) {
		t.Fatalf("missing path: %+v", r)
	}
	if r := run(store.CheckJSON, p, Target{Secret: "bad"}); r.Up || !strings.Contains(r.Text, "403") {
		t.Fatalf("forbidden: %+v", r)
	}
}

func TestROSDuration(t *testing.T) {
	for in, want := range map[string]float64{"5s": 5, "3h4m5s": 11045, "1w2d3h4m5s": 788645, "2d03:04:05": 183845, "00:10:00": 600} {
		if got, ok := rosDuration(in); !ok || got != want {
			t.Errorf("%s: %v %v, want %v", in, got, ok, want)
		}
	}
}
