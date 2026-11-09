package checks

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// ---- Windows over WinRM (no agent)

// WinRM reads processor, memory, page file, the fullest drive and the
// uptime of a Windows machine from WMI over WS-Management, signed in with
// NTLMv2. Over HTTP (port 5985, the Windows default) the messages are
// sealed with the NTLM session key; over HTTPS (5986) TLS protects them
// and the sign-in is bound to the server's certificate.
func WinRM(ctx context.Context, p store.CheckParams, t Target) Result {
	start := time.Now()
	w, err := newWinRM(p, t)
	if err != nil {
		return fail("%v", err)
	}
	defer w.http.CloseIdleConnections()
	osRows, err := w.query(ctx, "SELECT Caption,FreePhysicalMemory,TotalVisibleMemorySize,FreeSpaceInPagingFiles,SizeStoredInPagingFiles,LastBootUpTime FROM Win32_OperatingSystem")
	if err != nil {
		return fail("%s", shortErr(err))
	}
	cpuRows, err := w.query(ctx, "SELECT LoadPercentage FROM Win32_Processor")
	if err != nil {
		return fail("%s", shortErr(err))
	}
	diskRows, err := w.query(ctx, "SELECT DeviceID,Size,FreeSpace FROM Win32_LogicalDisk WHERE DriveType=3")
	if err != nil {
		return fail("%s", shortErr(err))
	}
	r := windowsResult(osRows, cpuRows, diskRows, time.Now())
	r.Text += fmt.Sprintf(", %.0f ms", msSince(start))
	return r
}

func windowsResult(osRows, cpuRows, diskRows []map[string]string, now time.Time) Result {
	r := Result{Up: true, Values: map[string]float64{}}
	f := func(m map[string]string, k string) float64 {
		v, err := strconv.ParseFloat(strings.TrimSpace(m[k]), 64)
		if err != nil {
			return math.NaN()
		}
		return v
	}
	name := "Windows"
	if len(osRows) > 0 {
		o := osRows[0]
		if c := strings.TrimSpace(o["Caption"]); c != "" {
			name = truncate(strings.TrimPrefix(c, "Microsoft "), 60)
		}
		if total, free := f(o, "TotalVisibleMemorySize"), f(o, "FreePhysicalMemory"); total > 0 && !math.IsNaN(free) {
			r.Values["mem_pct"] = pct(total-free, total)
			r.Values["mem_used_b"] = (total - free) * 1024
		}
		if size, free := f(o, "SizeStoredInPagingFiles"), f(o, "FreeSpaceInPagingFiles"); size > 0 && !math.IsNaN(free) {
			r.Values["swap_pct"] = pct(size-free, size)
		}
		if b, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(o["LastBootUpTime"])); err == nil && now.After(b) {
			r.Values["uptime_s"] = math.Round(now.Sub(b).Seconds())
		}
	}
	var sum float64
	var n int
	for _, c := range cpuRows {
		if v := f(c, "LoadPercentage"); !math.IsNaN(v) {
			sum += v
			n++
		}
	}
	if n > 0 {
		r.Values["cpu_pct"] = math.Round(sum/float64(n)*100) / 100
	}
	best, bestPct := "", -1.0
	for _, d := range diskRows {
		size, free := f(d, "Size"), f(d, "FreeSpace")
		if size > 0 && !math.IsNaN(free) {
			if p := pct(size-free, size); p > bestPct {
				best, bestPct = truncate(d["DeviceID"], 20), p
			}
		}
	}
	if best != "" {
		r.Values["used_pct"] = bestPct
	}
	r.Text = fmt.Sprintf("%s, CPU %.0f%%, memory %.0f%%", name, r.Values["cpu_pct"], r.Values["mem_pct"])
	if best != "" {
		r.Text += fmt.Sprintf(", fullest %s %.0f%%", best, bestPct)
	}
	return r
}

type winrm struct {
	url    string
	https  bool
	http   *http.Client
	ntlm   *ntlmClient
	authed bool
}

func newWinRM(p store.CheckParams, t Target) (*winrm, error) {
	if t.Secret == "" {
		return nil, errors.New("the check has no password")
	}
	scheme := "http"
	if p.TLS {
		scheme = "https"
	}
	host := t.Host
	tr := &http.Transport{Proxy: nil, MaxConnsPerHost: 1, MaxIdleConnsPerHost: 1, // NTLM signs in the connection, not the request
		TLSClientConfig: &tls.Config{InsecureSkipVerify: p.Insecure, MinVersion: tls.VersionTLS12, ServerName: t.Host}} // #nosec G402 -- the user chose to accept untrusted certificates for this check
	if t.Addr.IsValid() {
		host = t.Addr.String()
	}
	w := &winrm{url: scheme + "://" + hostPort(host, p.Port) + "/wsman", https: p.TLS,
		http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		ntlm: newNTLM(p.User, t.Secret, t.Host)}
	return w, nil
}

const encBoundary = "Encrypted Boundary"

func (w *winrm) post(ctx context.Context, body []byte, auth string) (*http.Response, []byte, error) {
	var payload []byte
	ctype := "application/soap+xml;charset=UTF-8"
	if body != nil && !w.https {
		sig, sealed := w.ntlm.seal(body)
		var b bytes.Buffer
		fmt.Fprintf(&b, "--%s\r\n\tContent-Type: application/HTTP-SPNEGO-session-encrypted\r\n\tOriginalContent: type=application/soap+xml;charset=UTF-8;Length=%d\r\n--%s\r\n\tContent-Type: application/octet-stream\r\n", encBoundary, len(body), encBoundary)
		var n [4]byte
		le.PutUint32(n[:], uint32(len(sig))) // #nosec G115 -- 16
		b.Write(n[:])
		b.Write(sig)
		b.Write(sealed)
		fmt.Fprintf(&b, "--%s--\r\n", encBoundary)
		payload = b.Bytes()
		ctype = `multipart/encrypted;protocol="application/HTTP-SPNEGO-session-encrypted";boundary="` + encBoundary + `"`
	} else {
		payload = body
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("User-Agent", "WatchZit")
	if auth != "" {
		req.Header.Set("Authorization", "Negotiate "+auth)
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, err
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "multipart/encrypted") {
		if raw, err = w.unwrap(raw); err != nil {
			return nil, nil, err
		}
	}
	return resp, raw, nil
}

// unwrap opens a multipart/encrypted answer.
func (w *winrm) unwrap(raw []byte) ([]byte, error) {
	marker := []byte("Content-Type: application/octet-stream\r\n")
	i := bytes.Index(raw, marker)
	if i < 0 {
		return nil, errors.New("the encrypted answer has no data")
	}
	data := raw[i+len(marker):]
	if j := bytes.LastIndex(data, []byte("--"+encBoundary)); j >= 0 {
		data = data[:j]
	}
	if len(data) < 4 {
		return nil, errors.New("the encrypted answer is short")
	}
	n := int(le.Uint32(data))
	if n != 16 || len(data) < 4+n {
		return nil, errors.New("the encrypted answer has a bad signature")
	}
	return w.ntlm.unseal(data[4:4+n], data[4+n:])
}

func negotiateToken(resp *http.Response) ([]byte, bool) {
	for _, h := range resp.Header.Values("WWW-Authenticate") {
		if v, ok := strings.CutPrefix(h, "Negotiate "); ok {
			b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
			return b, err == nil
		}
	}
	return nil, false
}

// call sends one SOAP message, signing in first on a new connection.
func (w *winrm) call(ctx context.Context, body []byte) ([]byte, error) {
	if !w.authed {
		resp, _, err := w.post(ctx, nil, base64.StdEncoding.EncodeToString(w.ntlm.negotiateMessage()))
		if err != nil {
			return nil, err
		}
		challenge, ok := negotiateToken(resp)
		if resp.StatusCode != http.StatusUnauthorized || !ok {
			if resp.StatusCode == http.StatusUnauthorized {
				return nil, errors.New("WinRM does not offer Negotiate (NTLM) sign-in")
			}
			return nil, fmt.Errorf("WinRM answered %s to the sign-in", resp.Status)
		}
		var cbt []byte
		if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
			cbt = certHash(resp.TLS.PeerCertificates[0])
		}
		client, session := make([]byte, 8), make([]byte, 16)
		_, _ = rand.Read(client)
		_, _ = rand.Read(session)
		auth, err := w.ntlm.authenticateMessage(challenge, cbt, time.Now(), client, session)
		if err != nil {
			return nil, err
		}
		resp, raw, err := w.post(ctx, body, base64.StdEncoding.EncodeToString(auth))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, errors.New("sign-in refused: check the user and the password (a local user needs to be an administrator or in Remote Management Users)")
		}
		w.authed = true
		return soapResult(resp, raw)
	}
	resp, raw, err := w.post(ctx, body, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("WinRM ended the sign-in")
	}
	return soapResult(resp, raw)
}

// certHash is the tls-server-end-point hash of a certificate (RFC 5929).
func certHash(c *x509.Certificate) []byte {
	switch c.SignatureAlgorithm {
	case x509.SHA384WithRSA, x509.ECDSAWithSHA384, x509.SHA384WithRSAPSS:
		h := sha512.Sum384(c.Raw)
		return h[:]
	case x509.SHA512WithRSA, x509.ECDSAWithSHA512, x509.SHA512WithRSAPSS:
		h := sha512.Sum512(c.Raw)
		return h[:]
	}
	h := sha256.Sum256(c.Raw)
	return h[:]
}

func soapResult(resp *http.Response, raw []byte) ([]byte, error) {
	if resp.StatusCode == http.StatusOK {
		return raw, nil
	}
	if msg := soapFault(raw); msg != "" {
		return nil, errors.New("WinRM: " + msg)
	}
	return nil, fmt.Errorf("WinRM answered %s", resp.Status)
}

// soapFault is the text of a SOAP fault ("" when there is none).
func soapFault(raw []byte) string {
	d := xml.NewDecoder(bytes.NewReader(raw))
	var inText bool
	var msg string
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			inText = t.Name.Local == "Text" || t.Name.Local == "Message"
		case xml.CharData:
			if s := strings.TrimSpace(string(t)); inText && s != "" {
				msg = s
			}
		case xml.EndElement:
			inText = false
		}
	}
	return truncate(strings.Join(strings.Fields(msg), " "), 200)
}

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// query runs a WQL query on root/cimv2 (WS-Enumeration with the items in
// the first answer).
func (w *winrm) query(ctx context.Context, wql string) ([]map[string]string, error) {
	env := `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" ` +
		`xmlns:n="http://schemas.xmlsoap.org/ws/2004/09/enumeration" xmlns:w="http://schemas.dmtf.org/wbem/wsman/1/wsman.xsd"><s:Header>` +
		`<a:To>` + xmlEscape(w.url) + `</a:To>` +
		`<w:ResourceURI s:mustUnderstand="true">http://schemas.microsoft.com/wbem/wsman/1/wmi/root/cimv2/*</w:ResourceURI>` +
		`<a:ReplyTo><a:Address s:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</a:Address></a:ReplyTo>` +
		`<a:Action s:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2004/09/enumeration/Enumerate</a:Action>` +
		`<w:MaxEnvelopeSize s:mustUnderstand="true">512000</w:MaxEnvelopeSize><a:MessageID>uuid:` + uuid() + `</a:MessageID>` +
		`<w:OperationTimeout>PT20S</w:OperationTimeout></s:Header><s:Body><n:Enumerate><w:OptimizeEnumeration/><w:MaxElements>64</w:MaxElements>` +
		`<w:Filter Dialect="http://schemas.microsoft.com/wbem/wsman/1/WQL">` + xmlEscape(wql) + `</w:Filter></n:Enumerate></s:Body></s:Envelope>`
	raw, err := w.call(ctx, []byte(env))
	if err != nil {
		return nil, err
	}
	return wsItems(raw)
}

// wsItems reads the items of an EnumerateResponse: each child of Items is
// one object, the text of each of its children one property.
func wsItems(raw []byte) ([]map[string]string, error) {
	d := xml.NewDecoder(bytes.NewReader(raw))
	var out []map[string]string
	depth, itemsAt := 0, -1
	var cur map[string]string
	var prop string
	var text strings.Builder
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("the WinRM answer is not valid XML")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch {
			case itemsAt == -1 && t.Name.Local == "Items":
				itemsAt = depth
			case itemsAt > 0 && depth == itemsAt+1:
				cur = map[string]string{}
			case itemsAt > 0 && depth == itemsAt+2:
				prop = t.Name.Local
				text.Reset()
			}
		case xml.CharData:
			if prop != "" && text.Len() < 1000 {
				text.Write(t)
			}
		case xml.EndElement:
			switch {
			case itemsAt > 0 && depth == itemsAt+2:
				cur[prop] = strings.TrimSpace(text.String())
				prop = ""
			case itemsAt > 0 && depth == itemsAt+1:
				if len(out) < 1000 {
					out = append(out, cur)
				}
			case depth == itemsAt:
				itemsAt = -2 // one Items element
			}
			depth--
		}
	}
	return out, nil
}
