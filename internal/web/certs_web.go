package web

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/tlsutil"
)

// StartConsoleTLS returns the TLS configuration of the console listener:
// the trusted certificate of Settings → Certificate, else self (the
// console's own). It starts port 80 when set and renews Let's Encrypt
// certificates in the background.
func (s *Server) StartConsoleTLS(ctx context.Context, dataDir string, self tls.Certificate, mgmtListen string) *tls.Config {
	w := &consoleTLS{dir: filepath.Join(dataDir, "web"), log: s.log, self: self}
	if _, port, err := net.SplitHostPort(mgmtListen); err == nil {
		w.mgmtPort = port
	}
	s.tlsc = w
	if err := w.apply(s.certSettings(ctx)); err != nil {
		s.log.Error("console certificate", "err", err)
		w.setErr(err.Error())
	}
	go func() {
		for {
			if err := w.renewIfNeeded(ctx); err != nil {
				s.log.Error("renewing the Let's Encrypt certificate failed; the current one stays until it expires", "err", err)
			}
			if info, _ := w.status(); info != nil && !info.SelfSigned && info.DaysLeft() < 21 {
				s.log.Warn("the console certificate expires soon", "days", info.DaysLeft(), "name", info.Subject)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(12 * time.Hour):
			}
		}
	}()
	cfg := tlsutil.ServerConfig(self)
	cfg.Certificates = nil
	cfg.GetCertificate = w.getCertificate
	return cfg
}

// certSettings reads the settings with the Cloudflare token opened.
func (s *Server) certSettings(ctx context.Context) CertSettings {
	c := CertSettings{Mode: "self", RedirectHTTP: true, Challenge: "http"}
	if err := s.store.GetSetting(ctx, settingCert, &c); err != nil {
		return c
	}
	if c.CloudflareToken != "" {
		t, err := s.store.OpenSecret(cfTokenPurpose, c.CloudflareToken)
		if err != nil {
			s.log.Error("cannot decrypt the Cloudflare token", "err", err)
		}
		c.CloudflareToken = t
	}
	return c
}

func (s *Server) saveCertSettings(ctx context.Context, c CertSettings) error {
	c.CloudflareToken = s.store.SealSecret(cfTokenPurpose, c.CloudflareToken)
	return s.store.SetSetting(ctx, settingCert, c)
}

type certData struct {
	S        CertSettings
	HasToken bool
	Staging  bool
	Info     *CertInfo
	Self     *CertInfo
	LastErr  string
	Address  string
	Off      bool // no certificate handling (development server)
}

func (s *Server) handleCertificate(w http.ResponseWriter, r *http.Request) {
	c := s.certSettings(r.Context())
	d := &certData{S: c, HasToken: c.CloudflareToken != "", Staging: c.ACMEDirectory == letsEncryptStaging, Off: s.tlsc == nil}
	d.S.CloudflareToken = ""
	if s.tlsc != nil {
		d.Info, d.LastErr = s.tlsc.status()
		d.Self = certInfo(&s.tlsc.self)
		if c.Mode != "self" {
			d.Address = s.tlsc.Address()
		}
	}
	if d.S.Mode == "" {
		d.S.Mode = "self"
	}
	s.render(w, r, http.StatusOK, "certificate", &Page{Title: "Certificate", Nav: "settings", Data: d})
}

// readPEM reads a PEM upload (file) or pasted text.
func readPEM(r *http.Request, name string) []byte {
	if f, _, err := r.FormFile(name + "_file"); err == nil {
		defer f.Close()
		if b, err := io.ReadAll(io.LimitReader(f, 1<<20)); err == nil && len(b) > 0 {
			return b
		}
	}
	return []byte(strings.TrimSpace(r.FormValue(name)))
}

func (s *Server) handleCertificateSave(w http.ResponseWriter, r *http.Request) {
	const back = "/settings/certificate"
	lang := s.lang(r, userFrom(r))
	if s.tlsc == nil {
		s.redirectErr(w, r, back, "The certificate cannot be changed on this server.")
		return
	}
	_ = r.ParseMultipartForm(4 << 20)
	old := s.certSettings(r.Context())
	c := CertSettings{Mode: r.FormValue("mode"), Hostname: strings.ToLower(strings.TrimSpace(r.FormValue("hostname"))),
		RedirectHTTP: r.FormValue("redirect_http") == "1", ACMEEmail: strings.TrimSpace(r.FormValue("acme_email")), Challenge: r.FormValue("challenge"),
		CloudflareToken: strings.TrimSpace(r.FormValue("cloudflare_token"))}
	if c.CloudflareToken == "" {
		c.CloudflareToken = old.CloudflareToken
	}
	if r.FormValue("staging") == "1" {
		c.ACMEDirectory = letsEncryptStaging
	}
	if err := c.Validate(); err != nil {
		s.redirectErr(w, r, back, sentence(translate(lang, err.Error())))
		return
	}
	switch c.Mode {
	case "upload":
		certPEM, keyPEM := readPEM(r, "cert_pem"), readPEM(r, "key_pem")
		if len(certPEM) == 0 && len(keyPEM) == 0 && old.Mode == "upload" {
			// keep the uploaded certificate; other settings change
			if cur := s.tlsc.loadCert(); cur == nil || cur.Leaf == nil || cur.Leaf.VerifyHostname(c.Hostname) != nil {
				s.redirectErr(w, r, back, translate(lang, "The uploaded certificate is not valid for %s; upload one that is.", c.Hostname))
				return
			}
			break
		}
		cert, err := parseKeyPair(certPEM, keyPEM, c.Hostname, time.Now())
		if err != nil {
			s.redirectErr(w, r, back, sentence(translate(lang, err.Error())))
			return
		}
		if err := s.tlsc.saveCert(certPEM, keyPEM); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.audit(r, "settings.certificate", "settings", "uploaded certificate for %s (%s, valid until %s)", c.Hostname, cert.Leaf.Issuer.CommonName, cert.Leaf.NotAfter.Format("2006-01-02"))
	case "acme":
		// port 80 must answer before the HTTP challenge
		if c.Challenge == "http" {
			if err := s.tlsc.apply(CertSettings{Mode: "acme", Hostname: c.Hostname, RedirectHTTP: true}); err != nil {
				_ = s.tlsc.apply(old)
				s.redirectErr(w, r, back, sentence(translate(lang, err.Error())))
				return
			}
		}
		if err := s.tlsc.obtainACME(r.Context(), c, nil); err != nil {
			_ = s.tlsc.apply(old)
			s.tlsc.setErr(err.Error())
			s.redirectErr(w, r, back, sentence(translate(lang, err.Error())))
			return
		}
		s.audit(r, "settings.certificate", "settings", "Let's Encrypt certificate for %s (%s challenge)", c.Hostname, c.Challenge)
	default:
		c = CertSettings{Mode: "self", RedirectHTTP: true, Challenge: "http"}
		s.audit(r, "settings.certificate", "settings", "the console's own certificate only")
	}
	if err := s.saveCertSettings(r.Context(), c); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.tlsc.setErr("")
	if err := s.tlsc.apply(c); err != nil {
		s.tlsc.setErr(err.Error())
		s.redirectErr(w, r, back, translate(lang, "Saved, but: %s", err.Error()))
		return
	}
	if c.Mode == "self" {
		s.redirect(w, r, back, "The console uses its own certificate.")
		return
	}
	s.redirect(w, r, back, "Certificate installed. Open the console under its name to use it.")
}

func (s *Server) handleCertificateRenew(w http.ResponseWriter, r *http.Request) {
	const back = "/settings/certificate"
	lang := s.lang(r, userFrom(r))
	c := s.certSettings(r.Context())
	if c.Mode != "acme" || s.tlsc == nil {
		s.redirectErr(w, r, back, "No Let's Encrypt certificate is set up.")
		return
	}
	if err := s.tlsc.obtainACME(r.Context(), c, nil); err != nil {
		s.tlsc.setErr(err.Error())
		s.redirectErr(w, r, back, sentence(translate(lang, err.Error())))
		return
	}
	s.tlsc.setErr("")
	if err := s.tlsc.apply(c); err != nil {
		s.redirectErr(w, r, back, fmt.Sprint(err))
		return
	}
	s.audit(r, "settings.certificate", "settings", "Let's Encrypt certificate for %s renewed", c.Hostname)
	s.redirect(w, r, back, "Certificate renewed.")
}
