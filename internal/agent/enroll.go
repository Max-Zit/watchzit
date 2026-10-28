package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

// EnrollData is what an enrollment code carries.
type EnrollData = proto.EnrollData

// ParseEnrollCode decodes and validates a code from the console (WZ1-…).
func ParseEnrollCode(code string) (EnrollData, error) {
	var d EnrollData
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, proto.EnrollCodePrefix) {
		return d, errors.New("an enrollment code starts with " + proto.EnrollCodePrefix)
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(code, proto.EnrollCodePrefix))
	if err != nil || json.Unmarshal(b, &d) != nil {
		return d, errors.New("the enrollment code is damaged; copy it again from the console")
	}
	return checkEnroll(d)
}

func checkEnroll(d EnrollData) (EnrollData, error) {
	var err error
	if d.Server, err = CheckServerURL(d.Server); err != nil {
		return d, err
	}
	if d.Fingerprint, err = CheckFingerprint(d.Fingerprint); err != nil {
		return d, err
	}
	if d.Token = strings.TrimSpace(d.Token); d.Token == "" {
		return d, errors.New("an enrollment token is required")
	}
	return d, nil
}

// pendingFile holds an enrollment that could not reach the server yet
// (for example an MSI installed at boot before the network was up).
const pendingFile = "pending-enrollment.json"

// SavePending stores an enrollment for the service to finish later.
func SavePending(dir string, d EnrollData) error {
	d, err := checkEnroll(d)
	if err != nil {
		return err
	}
	if err := EnsureDir(dir); err != nil {
		return err
	}
	b, _ := json.Marshal(d)
	return os.WriteFile(filepath.Join(dir, pendingFile), b, 0o600)
}

// LoadPending returns a stored enrollment, or ok=false.
func LoadPending(dir string) (EnrollData, bool) {
	var d EnrollData
	b, err := os.ReadFile(filepath.Join(dir, pendingFile)) // #nosec G304 -- fixed name in the data directory
	if err != nil || json.Unmarshal(b, &d) != nil {
		return d, false
	}
	d, err = checkEnroll(d)
	return d, err == nil
}

// Forget removes the enrollment (uninstall): configuration, key and a
// pending enrollment. The log stays.
func Forget(dir string) error {
	var errs []error
	for _, f := range []string{configFile, keyFile, pendingFile} {
		if err := os.Remove(filepath.Join(dir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	markEnrolled(false)
	return errors.Join(errs...)
}

// pinnedTLS trusts exactly the server certificate with the fingerprint
// (no certificate authorities) and presents the agent's key.
func pinnedTLS(fingerprint string, cert tls.Certificate) *tls.Config {
	want, _ := hex.DecodeString(fingerprint)
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		// The chain is checked by VerifyConnection against the pin.
		InsecureSkipVerify: true, // #nosec G402 -- certificate pinning below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("server sent no certificate")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(got[:], want) != 1 {
				return fmt.Errorf("%w (got %s)", ErrPinMismatch, hex.EncodeToString(got[:]))
			}
			return nil
		},
	}
}

func httpClient(tc *tls.Config) *http.Client {
	tr := &http.Transport{TLSClientConfig: tc, ForceAttemptHTTP2: false, TLSHandshakeTimeout: 15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 90 * time.Second, Proxy: http.ProxyFromEnvironment}
	return &http.Client{Transport: tr, Timeout: 60 * time.Second}
}

// ErrPinMismatch: the server is not the one the agent was told to trust.
var ErrPinMismatch = errors.New("server certificate does not match the pinned fingerprint")

// RefusedError is a definitive refusal (wrong token, wrong server); trying
// again with the same data does not help.
type RefusedError struct{ Msg string }

func (e *RefusedError) Error() string { return "enrollment refused: " + e.Msg }

// TestAddresses replaces the machine's addresses in tests.
var TestAddresses []string

// localAddresses lists this machine's unicast addresses (the server
// matches them to a device that is already monitored).
func localAddresses() []string {
	if TestAddresses != nil {
		return TestAddresses
	}
	var out []string
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagLoopback != 0 || i.Flags&net.FlagUp == 0 {
			continue
		}
		as, _ := i.Addrs()
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok && n.IP.IsGlobalUnicast() {
				out = append(out, n.IP.String())
			}
		}
	}
	return out
}

// Enroll registers this machine with the server.
func Enroll(ctx context.Context, dir string, d EnrollData) (*Config, error) {
	d, err := checkEnroll(d)
	if err != nil {
		return nil, err
	}
	if err := EnsureDir(dir); err != nil {
		return nil, err
	}
	priv, err := LoadOrCreateKey(dir)
	if err != nil {
		return nil, err
	}
	cert, err := clientCert(priv)
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	body, _ := json.Marshal(proto.EnrollRequest{Protocol: proto.Version, Token: d.Token, Hostname: host, OS: osName(),
		AgentVersion: Version, Addresses: localAddresses()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Server+proto.PathEnroll, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "watchzit-agent/"+Version)
	resp, err := httpClient(pinnedTLS(d.Fingerprint, cert)).Do(req)
	if errors.Is(err, ErrPinMismatch) {
		return nil, &RefusedError{Msg: err.Error()}
	}
	if err != nil {
		return nil, fmt.Errorf("cannot reach the server: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		var e proto.Error
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("server error: %s", msg)
		}
		return nil, &RefusedError{Msg: msg}
	}
	var er proto.EnrollResponse
	if err := json.Unmarshal(raw, &er); err != nil || er.AgentID == "" {
		return nil, errors.New("unexpected answer from the server")
	}
	c := &Config{ServerURL: d.Server, Fingerprint: d.Fingerprint, AgentID: er.AgentID, DeviceID: er.DeviceID, Device: er.Device, EnrolledAt: time.Now().UTC()}
	if err := c.Save(dir); err != nil {
		return nil, err
	}
	_ = os.Remove(filepath.Join(dir, pendingFile))
	markEnrolled(true)
	return c, nil
}

func osName() string {
	if inv := basicOS(); inv != "" {
		return inv
	}
	return runtime.GOOS + "/" + runtime.GOARCH
}
