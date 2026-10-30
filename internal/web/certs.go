package web

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"crypto/sha256"

	"golang.org/x/crypto/acme"
)

// The console serves browsers with its own self-signed certificate unless
// the administrator uploads a trusted one or lets WatchZit get one from
// Let's Encrypt (Settings → Certificate). The agent endpoint keeps its own
// certificate, which agents pin. Port 80 can redirect to the console and
// answers Let's Encrypt's HTTP challenge.

const settingCert = "certificate"

// CertSettings configure the console certificate.
type CertSettings struct {
	// Mode is "self", "upload" or "acme".
	Mode string `json:"mode"`
	// Hostname is the name browsers use, e.g. watch.example.com.
	Hostname string `json:"hostname,omitempty"`
	// RedirectHTTP serves port 80: redirects to the console and answers
	// the ACME HTTP-01 challenge.
	RedirectHTTP bool   `json:"redirect_http,omitempty"`
	ACMEEmail    string `json:"acme_email,omitempty"`
	// Challenge is "http" (port 80 reachable from the internet) or
	// "cloudflare" (a DNS TXT record through the Cloudflare API).
	Challenge       string `json:"challenge,omitempty"`
	CloudflareToken string `json:"cloudflare_token,omitempty"` // sealed in the database
	ACMEDirectory   string `json:"acme_directory,omitempty"`   // staging, tests
}

const (
	letsEncrypt        = "https://acme-v02.api.letsencrypt.org/directory"
	letsEncryptStaging = "https://acme-staging-v02.api.letsencrypt.org/directory"
	cfTokenPurpose     = "settings.certificate.cloudflare"
	renewBefore        = 30 * 24 * time.Hour
)

var dnsNameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// Validate checks c.
func (c CertSettings) Validate() error {
	switch c.Mode {
	case "self", "":
		return nil
	case "upload", "acme":
	default:
		return errors.New("unknown certificate mode")
	}
	// Uploaded certificates may also be for an IP address; Let's Encrypt
	// needs a public DNS name.
	if isIP := net.ParseIP(c.Hostname) != nil; !(isIP && c.Mode == "upload") && !dnsNameRe.MatchString(c.Hostname) {
		if c.Mode == "upload" {
			return errors.New("enter the DNS name or IP address browsers use for the console, e.g. watch.example.com")
		}
		return errors.New("enter the DNS name browsers use for the console, e.g. watch.example.com")
	}
	if c.Mode == "acme" {
		if !strings.Contains(c.ACMEEmail, "@") || len(c.ACMEEmail) > 200 {
			//lint:ignore ST1005 Let's Encrypt and Cloudflare are names
			return errors.New("Let's Encrypt needs an email address for expiry notices")
		}
		switch c.Challenge {
		case "http":
			if !c.RedirectHTTP {
				return errors.New("the HTTP challenge needs port 80: switch on \"Serve port 80\"")
			}
		case "cloudflare":
			if c.CloudflareToken == "" {
				return errors.New("enter a Cloudflare API token with the Zone.DNS edit permission")
			}
		default:
			return errors.New("choose how Let's Encrypt checks the name")
		}
	}
	return nil
}

func (c CertSettings) directory() string {
	if c.ACMEDirectory != "" {
		return c.ACMEDirectory
	}
	return letsEncrypt
}

// CertInfo describes a certificate for the settings page.
type CertInfo struct {
	Subject, Issuer string
	Names           []string
	NotAfter        time.Time
	Fingerprint     string
	SelfSigned      bool
}

// DaysLeft until the certificate expires.
func (c CertInfo) DaysLeft() int { return int(time.Until(c.NotAfter).Hours() / 24) }

func certInfo(c *tls.Certificate) *CertInfo {
	if c == nil || len(c.Certificate) == 0 {
		return nil
	}
	x, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return nil
	}
	names := append([]string(nil), x.DNSNames...)
	for _, ip := range x.IPAddresses {
		names = append(names, ip.String())
	}
	sum := sha256.Sum256(c.Certificate[0])
	return &CertInfo{Subject: x.Subject.CommonName, Issuer: x.Issuer.CommonName, Names: names, NotAfter: x.NotAfter,
		Fingerprint: hex.EncodeToString(sum[:]), SelfSigned: x.Issuer.String() == x.Subject.String()}
}

// parseKeyPair checks an uploaded certificate chain and key.
func parseKeyPair(certPEM, keyPEM []byte, hostname string, now time.Time) (*tls.Certificate, error) {
	c, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("the certificate and the key do not fit together or are not PEM: %v", err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return nil, err
	}
	if now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("the certificate expired on %s", leaf.NotAfter.Format("2006-01-02"))
	}
	if now.Before(leaf.NotBefore) {
		return nil, fmt.Errorf("the certificate is valid only from %s", leaf.NotBefore.Format("2006-01-02"))
	}
	if hostname != "" {
		if err := leaf.VerifyHostname(hostname); err != nil {
			covers := append([]string(nil), leaf.DNSNames...)
			for _, ip := range leaf.IPAddresses {
				covers = append(covers, ip.String())
			}
			return nil, fmt.Errorf("the certificate is not valid for %s (it covers %s)", hostname, strings.Join(covers, ", "))
		}
	}
	c.Leaf = leaf
	return &c, nil
}

// consoleTLS holds the console certificates and the port 80 listener.
type consoleTLS struct {
	dir  string // <data-dir>/web
	log  *slog.Logger
	self tls.Certificate

	mu       sync.Mutex
	cert     *tls.Certificate // trusted, or nil
	cfg      CertSettings
	http     *http.Server
	lastErr  string
	acmeKeys map[string]string // HTTP-01 token → key authorization
	mgmtPort string
	listen   func(network, addr string) (net.Listener, error)
	cfBase   string        // Cloudflare API address (tests)
	dnsDelay time.Duration // wait for DNS propagation (tests set it)
}

func (w *consoleTLS) certFile() string { return filepath.Join(w.dir, "cert.pem") }
func (w *consoleTLS) keyFile() string  { return filepath.Join(w.dir, "key.pem") }

func (w *consoleTLS) loadCert() *tls.Certificate {
	c, err := tls.LoadX509KeyPair(w.certFile(), w.keyFile())
	if err != nil {
		return nil
	}
	c.Leaf, _ = x509.ParseCertificate(c.Certificate[0])
	return &c
}

// saveCert writes a certificate chain and its key (readable only by the service).
func (w *consoleTLS) saveCert(certPEM, keyPEM []byte) error {
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		return err
	}
	for _, f := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{{w.keyFile(), keyPEM, 0o600}, {w.certFile(), certPEM, 0o644}} {
		tmp := f.path + ".new"
		if err := os.WriteFile(tmp, f.data, f.mode); err != nil {
			return err
		}
		if err := os.Rename(tmp, f.path); err != nil {
			return err
		}
	}
	return nil
}

// getCertificate serves the trusted certificate for its name (and for
// connections without a name), the console's own one otherwise.
func (w *consoleTLS) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	w.mu.Lock()
	c := w.cert
	w.mu.Unlock()
	if c != nil && c.Leaf != nil && (hello.ServerName == "" || c.Leaf.VerifyHostname(hello.ServerName) == nil) {
		return c, nil
	}
	return &w.self, nil
}

// apply uses cfg: loads the trusted certificate and starts or stops port 80.
func (w *consoleTLS) apply(cfg CertSettings) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cfg = cfg
	w.cert = nil
	if cfg.Mode == "upload" || cfg.Mode == "acme" {
		w.cert = w.loadCert()
	}
	want := cfg.RedirectHTTP && (cfg.Mode == "upload" || cfg.Mode == "acme")
	if w.http != nil && !want {
		closeServer(w.http)
		w.http = nil
	}
	if want && w.http == nil {
		listen := w.listen
		if listen == nil {
			listen = net.Listen
		}
		ln, err := listen("tcp", ":80")
		if err != nil {
			return fmt.Errorf("port 80: %v", err)
		}
		hs := &http.Server{Handler: http.HandlerFunc(w.serveHTTP), ReadHeaderTimeout: 15 * time.Second, ReadTimeout: time.Minute}
		w.http = hs
		go func() { _ = hs.Serve(ln) }()
	}
	return nil
}

func closeServer(hs *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(ctx)
}

// serveHTTP answers ACME HTTP-01 challenges and sends everything else to
// the console over HTTPS.
func (w *consoleTLS) serveHTTP(rw http.ResponseWriter, r *http.Request) {
	if tok, ok := strings.CutPrefix(r.URL.Path, "/.well-known/acme-challenge/"); ok {
		w.mu.Lock()
		ka, found := w.acmeKeys[tok]
		w.mu.Unlock()
		if !found {
			http.NotFound(rw, r)
			return
		}
		rw.Header().Set("Content-Type", "text/plain")
		_, _ = rw.Write([]byte(ka))
		return
	}
	target := w.Address()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(rw, "use HTTPS: "+target, http.StatusBadRequest)
		return
	}
	http.Redirect(rw, r, target+r.URL.RequestURI(), http.StatusMovedPermanently) // #nosec G710 -- to the configured console name
}

// Address is the console's HTTPS address under the certificate's name.
func (w *consoleTLS) Address() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	u := "https://" + w.cfg.Hostname
	if w.mgmtPort != "" && w.mgmtPort != "443" {
		u += ":" + w.mgmtPort
	}
	return u
}

func (w *consoleTLS) status() (*CertInfo, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return certInfo(w.cert), w.lastErr
}

func (w *consoleTLS) setErr(msg string) {
	w.mu.Lock()
	w.lastErr = msg
	w.mu.Unlock()
}

func pemEncode(der [][]byte) []byte {
	var b []byte
	for _, d := range der {
		b = append(b, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d})...)
	}
	return b
}

// ---- Let's Encrypt

func (w *consoleTLS) accountKey() (crypto.Signer, error) {
	p := filepath.Join(w.dir, "acme-account.key")
	if b, err := os.ReadFile(p); err == nil { // #nosec G304 -- our data directory
		blk, _ := pem.Decode(b)
		if blk == nil {
			return nil, errors.New("invalid ACME account key")
		}
		return x509.ParseECPrivateKey(blk.Bytes)
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(w.dir, 0o700); err != nil {
		return nil, err
	}
	return k, os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600)
}

// obtainACME gets a certificate for cfg.Hostname and stores it.
func (w *consoleTLS) obtainACME(ctx context.Context, cfg CertSettings, hc *http.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	key, err := w.accountKey()
	if err != nil {
		return err
	}
	cl := &acme.Client{Key: key, DirectoryURL: cfg.directory(), UserAgent: "watchzit"}
	if hc != nil {
		cl.HTTPClient = hc
	}
	if _, err := cl.Register(ctx, &acme.Account{Contact: []string{"mailto:" + cfg.ACMEEmail}}, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		//lint:ignore ST1005 Let's Encrypt and Cloudflare are names
		return fmt.Errorf("Let's Encrypt account: %w", err)
	}
	order, err := cl.AuthorizeOrder(ctx, acme.DomainIDs(cfg.Hostname))
	if err != nil {
		return fmt.Errorf("order: %w", err)
	}
	for _, u := range order.AuthzURLs {
		z, err := cl.GetAuthorization(ctx, u)
		if err != nil {
			return err
		}
		if z.Status == acme.StatusValid {
			continue
		}
		if err := w.solve(ctx, cl, cfg, z, hc); err != nil {
			return err
		}
	}
	finalize, orderURI := order.FinalizeURL, order.URI
	if order, err = cl.WaitOrder(ctx, order.URI); err != nil {
		return fmt.Errorf("order: %w", err)
	}
	if order.FinalizeURL != "" {
		finalize = order.FinalizeURL
	}
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cfg.Hostname}, DNSNames: []string{cfg.Hostname}}, certKey)
	if err != nil {
		return err
	}
	der, _, err := cl.CreateOrderCert(ctx, finalize, csr, true)
	if err != nil && orderURI != "" {
		// some ACME servers answer the finalize request without the order's
		// address: follow the order by its first address
		if o, werr := cl.WaitOrder(ctx, orderURI); werr == nil && o.CertURL != "" {
			der, err = cl.FetchCert(ctx, o.CertURL, true)
		}
	}
	if err != nil {
		return fmt.Errorf("issue certificate: %w", err)
	}
	kb, err := x509.MarshalECPrivateKey(certKey)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	certPEM := pemEncode(der)
	if _, err := parseKeyPair(certPEM, keyPEM, cfg.Hostname, time.Now()); err != nil {
		return err
	}
	return w.saveCert(certPEM, keyPEM)
}

func (w *consoleTLS) solve(ctx context.Context, cl *acme.Client, cfg CertSettings, z *acme.Authorization, hc *http.Client) error {
	want := "http-01"
	if cfg.Challenge == "cloudflare" {
		want = "dns-01"
	}
	var ch *acme.Challenge
	for _, c := range z.Challenges {
		if c.Type == want {
			ch = c
		}
	}
	if ch == nil {
		//lint:ignore ST1005 Let's Encrypt and Cloudflare are names
		return fmt.Errorf("Let's Encrypt offers no %s challenge for %s", want, z.Identifier.Value)
	}
	switch want {
	case "http-01":
		ka, err := cl.HTTP01ChallengeResponse(ch.Token)
		if err != nil {
			return err
		}
		w.mu.Lock()
		if w.acmeKeys == nil {
			w.acmeKeys = map[string]string{}
		}
		w.acmeKeys[ch.Token] = ka
		w.mu.Unlock()
		defer func() {
			w.mu.Lock()
			delete(w.acmeKeys, ch.Token)
			w.mu.Unlock()
		}()
	case "dns-01":
		val, err := cl.DNS01ChallengeRecord(ch.Token)
		if err != nil {
			return err
		}
		cf := cloudflare{token: cfg.CloudflareToken, http: hc, base: w.cfBase}
		zone, rec, err := cf.addTXT(ctx, "_acme-challenge."+z.Identifier.Value, val)
		if err != nil {
			//lint:ignore ST1005 Let's Encrypt and Cloudflare are names
			return fmt.Errorf("Cloudflare: %w", err)
		}
		defer func() { _ = cf.delete(context.Background(), zone, rec) }()
		wait := w.dnsDelay
		if wait == 0 {
			wait = 20 * time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	if _, err := cl.Accept(ctx, ch); err != nil {
		return fmt.Errorf("challenge: %w", err)
	}
	if _, err := cl.WaitAuthorization(ctx, z.URI); err != nil {
		hint := "is port 80 of this server reachable from the internet under " + z.Identifier.Value + "?"
		if want == "dns-01" {
			hint = "does the Cloudflare token manage the DNS zone of " + z.Identifier.Value + "?"
		}
		//lint:ignore ST1005 Let's Encrypt and Cloudflare are names
		return fmt.Errorf("Let's Encrypt could not check %s (%s): %w", z.Identifier.Value, hint, err)
	}
	return nil
}

// renewIfNeeded renews a Let's Encrypt certificate that expires soon.
func (w *consoleTLS) renewIfNeeded(ctx context.Context) error {
	w.mu.Lock()
	cfg, cur := w.cfg, w.cert
	w.mu.Unlock()
	if cfg.Mode != "acme" || (cur != nil && cur.Leaf != nil && time.Until(cur.Leaf.NotAfter) > renewBefore) {
		return nil
	}
	if err := w.obtainACME(ctx, cfg, nil); err != nil {
		w.setErr(err.Error())
		return err
	}
	w.setErr("")
	return w.apply(cfg)
}

// cloudflare adds and removes DNS TXT records through the Cloudflare API.
type cloudflare struct {
	token string
	http  *http.Client
	base  string
}

func (c cloudflare) do(ctx context.Context, method, path string, body, out any) error {
	base := c.base
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	hc := c.http
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var env struct {
		Success bool                       `json:"success"`
		Errors  []struct{ Message string } `json:"errors"`
		Result  json.RawMessage            `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env); err != nil {
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if !env.Success {
		var msgs []string
		for _, e := range env.Errors {
			msgs = append(msgs, e.Message)
		}
		return errors.New(strings.Join(msgs, "; "))
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

func (c cloudflare) zoneFor(ctx context.Context, name string) (string, error) {
	labels := strings.Split(strings.TrimSuffix(name, "."), ".")
	for i := 0; i < len(labels)-1; i++ {
		var zs []struct{ ID string }
		if err := c.do(ctx, "GET", "/zones?name="+url.QueryEscape(strings.Join(labels[i:], ".")), nil, &zs); err != nil {
			return "", err
		}
		if len(zs) > 0 {
			return zs[0].ID, nil
		}
	}
	return "", fmt.Errorf("no Cloudflare zone for %s is reachable with this token", name)
}

func (c cloudflare) addTXT(ctx context.Context, name, value string) (zone, id string, err error) {
	if zone, err = c.zoneFor(ctx, name); err != nil {
		return "", "", err
	}
	var rec struct{ ID string }
	err = c.do(ctx, "POST", "/zones/"+zone+"/dns_records", map[string]any{"type": "TXT", "name": name, "content": value, "ttl": 60}, &rec)
	return zone, rec.ID, err
}

func (c cloudflare) delete(ctx context.Context, zone, id string) error {
	if zone == "" || id == "" {
		return nil
	}
	return c.do(ctx, "DELETE", "/zones/"+zone+"/dns_records/"+id, nil, nil)
}
