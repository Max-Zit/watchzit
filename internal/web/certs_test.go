package web_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/max-zit/watchzit/internal/tlsutil"
)

func testCertPEM(t *testing.T, name string, notAfter time.Time) (certPEM, keyPEM string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		Issuer: pkix.Name{CommonName: "Test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
}

// TestConsoleCertificate: an uploaded certificate is checked, served for
// its name (the console's own one for other names), and the console can go
// back to its own certificate.
func TestConsoleCertificate(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	certPEM, keyPEM, err := tlsutil.SelfSigned("WatchZit console", 5)
	if err != nil {
		t.Fatal(err)
	}
	self, _ := tls.X509KeyPair(certPEM, keyPEM)
	cfg := e.srv.StartConsoleTLS(ctx, t.TempDir(), self, ":8443")
	c, _ := e.setupAdmin("192.168.1.10")
	if r := c.get("/settings/certificate"); r.code != http.StatusOK || !strings.Contains(r.body, "the console&#39;s own certificate") {
		t.Fatalf("page: %d", r.code)
	}

	good, key := testCertPEM(t, "watch.example.com", time.Now().Add(90*24*time.Hour))
	other, _ := testCertPEM(t, "other.example.com", time.Now().Add(90*24*time.Hour))
	old, oldKey := testCertPEM(t, "watch.example.com", time.Now().Add(-time.Hour))
	for _, bad := range [][3]string{{"watch.example.com", other, key}, {"watch.example.com", old, oldKey}, {"watch.example.com", "junk", key}, {"not a name!", good, key}} {
		c.post("/settings/certificate", "mode", "upload", "hostname", bad[0], "cert_pem", bad[1], "key_pem", bad[2])
		if got, _ := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "watch.example.com"}); got.Leaf != nil && got.Leaf.Subject.CommonName == "watch.example.com" {
			t.Fatalf("a bad certificate was installed: %s", bad[0])
		}
	}
	if r := c.post("/settings/certificate", "mode", "upload", "hostname", "watch.example.com", "cert_pem", good, "key_pem", key); r.code != http.StatusSeeOther {
		t.Fatalf("upload: %d", r.code)
	}
	got, _ := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "watch.example.com"})
	if got.Leaf == nil || got.Leaf.Subject.CommonName != "watch.example.com" {
		t.Fatal("the uploaded certificate is not served")
	}
	if got, _ := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "10.0.0.5.nip.io"}); got.Leaf != nil && got.Leaf.Subject.CommonName == "watch.example.com" {
		t.Fatal("the uploaded certificate is served for another name")
	}
	if r := c.get("/settings/certificate"); !strings.Contains(r.body, "watch.example.com") || !strings.Contains(r.body, "Valid until") {
		t.Fatal("page does not show the uploaded certificate")
	}
	// keep it while changing nothing else; back to the console's own one
	c.post("/settings/certificate", "mode", "upload", "hostname", "watch.example.com")
	if got, _ := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "watch.example.com"}); got.Leaf == nil {
		t.Fatal("the certificate was dropped")
	}
	c.post("/settings/certificate", "mode", "self")
	if got, _ := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "watch.example.com"}); got.Leaf != nil && got.Leaf.Subject.CommonName == "watch.example.com" {
		t.Fatal("still the uploaded certificate after going back")
	}
	// Let's Encrypt needs an email and a way to check the name
	c.post("/settings/certificate", "mode", "acme", "hostname", "watch.example.com", "challenge", "http")
	if r := c.get("/settings/certificate"); !strings.Contains(r.body, "email") {
		t.Fatal("no message for Let's Encrypt without an email")
	}
}
