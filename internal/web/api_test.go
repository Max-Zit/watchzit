package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

var apiTokenRE = regexp.MustCompile(`wzat_[A-Za-z0-9_-]{30,}`)

// api calls the REST API with a token (no cookies, no Origin).
func (e *env) api(token, method, path, body string) resp {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, "http://wz.test"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, "http://wz.test"+path, nil)
	}
	r.RemoteAddr = "192.168.1.77:40000"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return resp{code: w.Code, body: w.Body.String(), hdr: w.Header()}
}

func TestAPI(t *testing.T) {
	e := newEnv(t)
	c, _ := e.setupAdmin("192.168.1.10")
	c.post("/devices/new", "name", "core", "address", "10.0.0.1", "ping", "1")

	// a token is shown once on the account page
	r := c.post("/account/tokens", "name", "Script", "days", "30")
	tok := apiTokenRE.FindString(r.body)
	if r.code != http.StatusOK || tok == "" {
		t.Fatalf("token: %d", r.code)
	}
	if strings.Contains(c.get("/account").body, tok) {
		t.Fatal("the token is shown again")
	}

	// without a token, with a bad one, or with only the session cookie: refused
	if r := e.api("", "GET", "/api/v1/status", ""); r.code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", r.code)
	}
	if r := e.api("wzat_nope", "GET", "/api/v1/status", ""); r.code != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", r.code)
	}
	if r := c.get("/api/v1/status"); r.code != http.StatusUnauthorized {
		t.Fatalf("session cookie accepted by the API: %d", r.code)
	}

	r = e.api(tok, "GET", "/api/v1/status", "")
	var st struct {
		Devices map[string]int `json:"devices"`
	}
	if r.code != http.StatusOK || json.Unmarshal([]byte(r.body), &st) != nil || st.Devices["unknown"] != 1 {
		t.Fatalf("status: %d %s", r.code, r.body)
	}
	r = e.api(tok, "GET", "/api/v1/devices", "")
	if r.code != http.StatusOK || !strings.Contains(r.body, `"name":"core"`) {
		t.Fatalf("devices: %s", r.body)
	}
	r = e.api(tok, "GET", "/api/v1/devices/1", "")
	var dev struct {
		CheckList []struct {
			Type   string `json:"type"`
			Series []struct {
				ID     int    `json:"id"`
				Metric string `json:"metric"`
			} `json:"series"`
		} `json:"check_list"`
	}
	if json.Unmarshal([]byte(r.body), &dev) != nil || len(dev.CheckList) != 1 || dev.CheckList[0].Type != "icmp" || len(dev.CheckList[0].Series) != 2 {
		t.Fatalf("device: %s", r.body)
	}
	if r := e.api(tok, "GET", "/api/v1/series/"+itoa(int32(dev.CheckList[0].Series[0].ID))+"?range=1h", ""); r.code != http.StatusOK || !strings.Contains(r.body, `"t":[`) {
		t.Fatalf("series: %d %s", r.code, r.body)
	}
	if r := e.api(tok, "GET", "/api/v1/devices/99", ""); r.code != http.StatusNotFound || !strings.Contains(r.body, `"error"`) {
		t.Fatalf("missing device: %d %s", r.code, r.body)
	}
	if r := e.api(tok, "GET", "/api/v1/nothing", ""); r.code != http.StatusNotFound {
		t.Fatalf("unknown path: %d", r.code)
	}

	// maintenance and acknowledging over the API, without Origin or cookies
	end := e.now.Add(2 * time.Hour).Format(time.RFC3339)
	r = e.api(tok, "POST", "/api/v1/maintenance", `{"name":"Patch night","ends_at":"`+end+`","device_ids":[1]}`)
	if r.code != http.StatusCreated {
		t.Fatalf("maintenance: %d %s", r.code, r.body)
	}
	if r := e.api(tok, "POST", "/api/v1/maintenance", `{"name":"x","ends_at":"`+end+`","bogus":1}`); r.code != http.StatusBadRequest {
		t.Fatalf("unknown field accepted: %d", r.code)
	}
	if r := e.api(tok, "GET", "/api/v1/maintenance", ""); !strings.Contains(r.body, "Patch night") {
		t.Fatalf("maintenance list: %s", r.body)
	}
	if r := c.get("/audit"); !strings.Contains(r.body, "API token Script") {
		t.Fatal("API change not audited with the token name")
	}
	if r := e.api(tok, "POST", "/api/v1/problems/5/ack", `{"note":"x"}`); r.code != http.StatusNotFound {
		t.Fatalf("ack missing: %d", r.code)
	}

	// a viewer's token reads but cannot change
	c.post("/users", "username", "watcher", "password", "watcher password 1", "role", "viewer")
	v := e.client("192.168.1.30")
	v.post("/login", "username", "watcher", "password", "watcher password 1")
	e.enroll(v)
	v.post("/account/password", "current", "watcher password 1", "password", "new watcher pass 1", "password2", "new watcher pass 1")
	vt := apiTokenRE.FindString(v.post("/account/tokens", "name", "Read", "days", "0").body)
	if r := e.api(vt, "GET", "/api/v1/problems", ""); r.code != http.StatusOK {
		t.Fatalf("viewer read: %d", r.code)
	}
	if r := e.api(vt, "POST", "/api/v1/maintenance", `{"name":"x","ends_at":"`+end+`","device_ids":[1]}`); r.code != http.StatusForbidden {
		t.Fatalf("viewer wrote: %d", r.code)
	}

	// revoked and expired tokens stop working
	c.post("/account/tokens/1/delete")
	if r := e.api(tok, "GET", "/api/v1/status", ""); r.code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", r.code)
	}
	e.now = e.now.AddDate(0, 0, 400)
	if r := e.api(vt, "GET", "/api/v1/status", ""); r.code != http.StatusOK {
		t.Fatalf("token without limit: %d", r.code)
	}
}
