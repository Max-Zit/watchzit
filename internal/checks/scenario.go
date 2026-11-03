package checks

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// Scenario steps through a web application like a user: each step is a
// GET or a POST of a form, cookies are kept from step to step (a sign-in
// stays signed in), and every step must answer with the expected status
// and text. {secret} in form data is the check's password (stored sealed),
// URL-encoded. It stops at the first step that fails.
func Scenario(ctx context.Context, p store.CheckParams, t Target) Result {
	base := strings.TrimRight(p.URL, "/")
	if base == "" {
		base = "https://" + hostPort(t.Host, 443)
	}
	jar, _ := cookiejar.New(nil)
	follow := true
	c := &http.Client{Jar: jar,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: p.Insecure, MinVersion: tls.VersionTLS12}, // #nosec G402 -- the user chose to accept untrusted certificates for this check
			DisableKeepAlives: true, ForceAttemptHTTP2: true, Proxy: nil},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !follow {
				return http.ErrUseLastResponse // the step expects the redirect itself
			}
			if len(via) >= 5 {
				return errors.New("more than 5 redirects")
			}
			return nil
		}}
	start := time.Now()
	r := Result{Values: map[string]float64{"steps_ok": 0}}
	for i, s := range p.Steps {
		u := s.URL
		if strings.HasPrefix(u, "/") {
			u = base + u
		}
		var body io.Reader
		if s.Method == http.MethodPost {
			body = strings.NewReader(strings.ReplaceAll(s.Body, "{secret}", url.QueryEscape(t.Secret)))
		}
		req, err := http.NewRequestWithContext(ctx, s.Method, u, body)
		if err != nil {
			return stepFail(r, start, i, s, err.Error())
		}
		req.Header.Set("User-Agent", "WatchZit")
		if body != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		follow = s.Expect < 300 || s.Expect > 399
		resp, err := c.Do(req) // #nosec G704 -- the addresses are set by an operator: checking them is the purpose
		if err != nil {
			return stepFail(r, start, i, s, shortErr(err))
		}
		page, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		ok := resp.StatusCode < 400
		if s.Expect != 0 {
			ok = resp.StatusCode == s.Expect
		}
		if !ok {
			msg := resp.Status
			if s.Expect != 0 {
				msg += fmt.Sprintf(" (expected %d)", s.Expect)
			}
			return stepFail(r, start, i, s, msg)
		}
		if s.Contains != "" && !strings.Contains(string(page), s.Contains) {
			return stepFail(r, start, i, s, fmt.Sprintf("%q not found", s.Contains))
		}
		r.Values["steps_ok"] = float64(i + 1)
	}
	r.Values["response_ms"] = msSince(start)
	r.Up = true
	r.Text = fmt.Sprintf("%d steps, %.0f ms", len(p.Steps), r.Values["response_ms"])
	return r
}

func stepFail(r Result, start time.Time, i int, s store.WebStep, msg string) Result {
	r.Values["response_ms"] = msSince(start)
	r.Text = fmt.Sprintf("step %d (%s %s): %s", i+1, s.Method, s.URL, msg)
	return r
}
