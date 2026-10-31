package web_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"github.com/max-zit/watchzit/internal/store"
)

// fakeOIDC is an identity provider: discovery, keys and a token endpoint
// that answers with an ID token for the nonce it is told.
type fakeOIDC struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	nonce  string
	groups []string
	user   string
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	f := &fakeOIDC{}
	f.key, _ = rsa.GenerateKey(rand.Reader, 2048)
	mux := http.NewServeMux()
	f.srv = httptest.NewTLSServer(mux)
	iss := f.srv.URL
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": iss, "authorization_endpoint": iss + "/authorize", "token_endpoint": iss + "/token",
			"jwks_uri": iss + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "good-code" || r.Form.Get("code_verifier") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
		if err != nil {
			t.Error(err)
			return
		}
		now := time.Now()
		claims, _ := json.Marshal(map[string]any{"iss": iss, "aud": "watchzit", "sub": "abc", "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
			"nonce": f.nonce, "preferred_username": f.user, "name": "Ana Admin", "email": "ana@example.com", "groups": f.groups})
		obj, _ := sig.Sign(claims)
		tok, _ := obj.CompactSerialize()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 300, "id_token": tok})
	})
	return f
}

func (f *fakeOIDC) caPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw}))
}

// TestSingleSignOn: OpenID Connect creates the user at the first sign-in
// with the role of its groups; a local account cannot be taken over;
// directory accounts have no WatchZit password.
func TestSingleSignOn(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	f := newFakeOIDC(t)
	defer f.srv.Close()

	if r := c.post("/settings/sso", "protocol", "oidc", "issuer", "http://idp.example", "client_id", "watchzit", "client_secret", "s3cret"); !strings.Contains(c.get(r.loc).body, "https://") {
		t.Fatal("an http issuer was accepted")
	}
	if r := c.post("/settings/sso", "protocol", "oidc", "issuer", f.srv.URL, "client_id", "watchzit", "client_secret", "s3cret", "ca_cert", f.caPEM(),
		"label", "Lab IdP", "admin_group", "wz-admins", "operator_group", "wz-ops"); r.code != http.StatusSeeOther || strings.Contains(c.get(r.loc).body, "flash err") {
		t.Fatalf("save: %d", r.code)
	}
	var raw map[string]any
	_ = e.st.GetSetting(ctx, "sso", &raw)
	if s, _ := raw["client_secret"].(string); !strings.HasPrefix(s, "v1:") {
		t.Fatal("the client secret is not sealed")
	}

	signIn := func(user string, groups []string) resp {
		g := e.client("192.168.1.20")
		if !strings.Contains(g.get("/login").body, "Sign in with Lab IdP") {
			t.Fatal("no single sign-on button")
		}
		r := g.get("/login/sso?next=/maps")
		u, err := url.Parse(r.loc)
		if r.code != http.StatusSeeOther || err != nil || !strings.HasPrefix(r.loc, f.srv.URL+"/authorize") || u.Query().Get("code_challenge") == "" {
			t.Fatalf("start: %d %s", r.code, r.loc)
		}
		f.nonce, f.user, f.groups = u.Query().Get("nonce"), user, groups
		return g.get("/login/sso/callback?state=" + url.QueryEscape(u.Query().Get("state")) + "&code=good-code")
	}
	if r := signIn("ana", []string{"other", "wz-ops"}); r.code != http.StatusSeeOther || r.loc != "/maps" {
		t.Fatalf("callback: %d %s %s", r.code, r.loc, r.body)
	}
	us, _ := e.st.Users(ctx)
	var ana *store.User
	for _, u := range us {
		if u.Username == "ana" {
			ana = u
		}
	}
	if ana == nil || ana.Source != store.SourceSSO || ana.Role != store.RoleOperator || ana.Email != "ana@example.com" || ana.DisplayName != "Ana Admin" {
		t.Fatalf("user: %+v", ana)
	}
	// the groups decide again at the next sign-in
	signIn("ana", []string{"wz-admins"})
	if u, _ := e.st.UserByID(ctx, ana.ID); u.Role != store.RoleAdmin {
		t.Fatalf("role after the group change: %s", u.Role)
	}
	// no group, no default role: refused
	if r := signIn("bob", []string{"sales"}); r.code != http.StatusUnauthorized || !strings.Contains(r.body, "no group that may use WatchZit") {
		t.Fatalf("no group: %d", r.code)
	}
	// a local account is not taken over
	if r := signIn("admin", []string{"wz-admins"}); r.code != http.StatusUnauthorized || !strings.Contains(r.body, "another sign-in method") {
		t.Fatalf("take-over: %d", r.code)
	}
	// a state that does not match is refused
	g := e.client("192.168.1.21")
	g.get("/login/sso")
	if r := g.get("/login/sso/callback?state=forged&code=good-code"); r.code != http.StatusUnauthorized {
		t.Fatalf("forged state: %d", r.code)
	}
	// directory accounts have no password here
	if _, err := e.st.CheckPassword(ctx, "ana", "!"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("password of an SSO account: %v", err)
	}
	if r := c.post("/users/"+itoa64(ana.ID)+"/password", "password", "a long new password 1"); !strings.Contains(r.body, "its password is set there") {
		t.Fatalf("password reset of an SSO account: %d", r.code)
	}
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// TestDirectorySettings: LDAP settings are checked and the bind password
// sealed; an unreachable directory says so and local accounts keep
// working; SAML gets its own key and metadata.
func TestDirectorySettings(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c, _ := e.setupAdmin("192.168.1.10")
	if r := c.post("/settings/ldap", "enabled", "1", "url", "ldap://127.0.0.1:1", "bind_dn", "svc", "bind_password", "pw", "base_dn", "DC=lab", "user_filter", "(uid={username})", "default_role", "viewer"); !strings.Contains(c.get(r.loc).body, "StartTLS") {
		t.Fatal("ldap:// without StartTLS was accepted")
	}
	if r := c.post("/settings/ldap", "enabled", "1", "url", "ldaps://127.0.0.1:1", "bind_dn", "svc", "bind_password", "pw", "base_dn", "DC=lab", "user_filter", "(uid={username})"); !strings.Contains(c.get(r.loc).body, "at least one group") {
		t.Fatal("no role mapping was accepted")
	}
	if r := c.post("/settings/ldap", "enabled", "1", "url", "ldaps://127.0.0.1:1", "bind_dn", "svc", "bind_password", "pw", "base_dn", "DC=lab", "user_filter", "(uid={username})", "default_role", "viewer"); strings.Contains(c.get(r.loc).body, "flash err") {
		t.Fatal("valid LDAP settings refused")
	}
	var raw map[string]any
	_ = e.st.GetSetting(ctx, "ldap", &raw)
	if s, _ := raw["bind_password"].(string); !strings.HasPrefix(s, "v1:") {
		t.Fatal("the bind password is not sealed")
	}
	g := e.client("192.168.1.30")
	if r := g.post("/login", "username", "someone", "password", "x"); r.code != http.StatusServiceUnavailable || !strings.Contains(r.body, "cannot be reached") {
		t.Fatalf("unreachable directory: %d", r.code)
	}
	if r := g.post("/login", "username", "admin", "password", "admin password 123"); r.code != http.StatusSeeOther || !strings.HasPrefix(r.loc, "/login/2fa") {
		t.Fatalf("local account with LDAP on: %d %s", r.code, r.loc)
	}

	md := `<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example/saml"><IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol"><SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example/sso"/></IDPSSODescriptor></EntityDescriptor>`
	if r := c.post("/settings/sso", "protocol", "saml", "metadata_xml", md, "default_role", "viewer"); strings.Contains(c.get(r.loc).body, "flash err") {
		t.Fatal("SAML settings refused")
	}
	if r := c.get("/login/sso/metadata"); r.code != http.StatusOK || !strings.Contains(r.body, "/login/sso/saml") || !strings.Contains(r.body, "X509Certificate") {
		t.Fatalf("SP metadata: %d %s", r.code, r.body)
	}
	if r := g.get("/login/sso"); r.code != http.StatusSeeOther || !strings.HasPrefix(r.loc, "https://idp.example/sso?SAMLRequest=") {
		t.Fatalf("SAML start: %d %s", r.code, r.loc)
	}
	// the IdP posts the response cross-site: not refused as CSRF, but an
	// answer that is no SAML response is
	if r := g.do(http.MethodPost, "/login/sso/saml", url.Values{"SAMLResponse": {"bm9uZQ=="}, "RelayState": {"x"}}, map[string]string{"Origin": "https://idp.example"}); r.code != http.StatusUnauthorized {
		t.Fatalf("bad SAML response: %d", r.code)
	}
}
