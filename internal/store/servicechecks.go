package store

import (
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Checks of services: web pages, certificates, DNS, databases, RouterOS,
// JSON APIs, machines without an agent (SSH, WinRM) (run by internal/checks). Their password or token is sealed
// with the server key when the check is saved and never shown again.

// DNSTypes are the record types a DNS check can ask for.
var DNSTypes = []string{"A", "AAAA", "CNAME", "MX", "NS", "TXT", "SOA", "PTR"}

var (
	dnsNameRE  = regexp.MustCompile(`^([A-Za-z0-9_]([A-Za-z0-9_-]{0,62})\.)*[A-Za-z0-9_]([A-Za-z0-9_-]{0,62})\.?$`)
	jsonPathRE = regexp.MustCompile(`^[A-Za-z0-9_$@-]+(\.[A-Za-z0-9_$@-]+)*$`)
	dbNameRE   = regexp.MustCompile(`^[A-Za-z0-9_$.-]{1,63}$`)
	apiUserRE  = regexp.MustCompile(`^[^\s:]{1,64}$`)
	hostKeyRE  = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
)

func checkURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(raw) > 2000 {
		return errors.New("the address must start with http:// or https:// (no user name in it)")
	}
	return nil
}

func (p *CheckParams) cleanService(typ string) error {
	if typ != CheckSSH {
		p.HostKey = ""
	}
	p.Count, p.IfIndex, p.IfName, p.Index, p.Descr, p.OID, p.Rate = 0, 0, "", 0, "", "", false
	p.URL, p.Contains, p.Query, p.User, p.Database, p.Path = strings.TrimSpace(p.URL), strings.TrimSpace(p.Contains),
		strings.TrimSpace(p.Query), strings.TrimSpace(p.User), strings.TrimSpace(p.Database), strings.TrimSpace(p.Path)
	if len(p.Contains) > 200 {
		return errors.New("the text to look for is too long (200 characters at most)")
	}
	if len(p.Secret) > 1000 && (typ != CheckSSH || len(p.Secret) > 16000) { // an SSH private key is longer
		return errors.New("the password or token is too long")
	}
	keep := func(port int) error {
		if p.Port == 0 {
			p.Port = port
		}
		if p.Port < 1 || p.Port > 65535 {
			return errors.New("the port must be 1–65535")
		}
		return nil
	}
	switch typ {
	case CheckHTTP:
		p.Port, p.Query, p.RType, p.User, p.Secret, p.Database, p.Path, p.Auth, p.TLS, p.Unit, p.Scale = 0, "", "", "", "", "", "", "", false, "", 0
		if p.URL != "" {
			if err := checkURL(p.URL); err != nil {
				return err
			}
		}
		if p.Expect != 0 && (p.Expect < 100 || p.Expect > 599) {
			return errors.New("the expected status must be 100–599 (0 = any below 400)")
		}
	case CheckTLS:
		p.URL, p.Expect, p.Contains, p.RType, p.User, p.Secret, p.Database, p.Path, p.Auth, p.TLS, p.Unit, p.Scale = "", 0, "", "", "", "", "", "", "", false, "", 0
		if p.Query != "" && !dnsNameRE.MatchString(p.Query) {
			return errors.New("the server name must be a DNS name")
		}
		return keep(443)
	case CheckDNS:
		p.URL, p.Expect, p.Insecure, p.User, p.Secret, p.Database, p.Path, p.Auth, p.TLS, p.Unit, p.Scale = "", 0, false, "", "", "", "", "", false, "", 0
		if !dnsNameRE.MatchString(p.Query) || len(p.Query) > 253 {
			return errors.New("enter the name to look up, for example example.com")
		}
		p.RType = strings.ToUpper(strings.TrimSpace(p.RType))
		if p.RType == "" {
			p.RType = "A"
		}
		if !slices.Contains(DNSTypes, p.RType) {
			return errors.New("unknown DNS record type")
		}
		return keep(53)
	case CheckPostgres, CheckMySQL:
		p.URL, p.Expect, p.Contains, p.Query, p.RType, p.Path, p.Auth, p.TLS, p.Unit, p.Scale = "", 0, "", "", "", "", "", false, "", 0
		if p.User == "" || !apiUserRE.MatchString(p.User) {
			return errors.New("enter the database user (a monitoring user with read rights is enough)")
		}
		if p.Database != "" && !dbNameRE.MatchString(p.Database) {
			return errors.New("the database name may contain letters, digits and _ $ . - only")
		}
		if typ == CheckPostgres {
			return keep(5432)
		}
		return keep(3306)
	case CheckRouterOS:
		p.URL, p.Expect, p.Contains, p.Query, p.RType, p.Database, p.Path, p.Auth, p.Unit, p.Scale = "", 0, "", "", "", "", "", "", "", 0
		if p.User == "" || !apiUserRE.MatchString(p.User) {
			return errors.New("enter the RouterOS user (a read-only group is enough)")
		}
		if p.TLS {
			return keep(443)
		}
		return keep(80)
	case CheckSSH:
		p.URL, p.Expect, p.Contains, p.Query, p.RType, p.Database, p.Path, p.Auth, p.TLS, p.Unit, p.Scale, p.Insecure, p.HV = "", 0, "", "", "", "", "", "", false, "", 0, false, ""
		if p.User == "" || !apiUserRE.MatchString(p.User) {
			return errors.New("enter the user (any user that may sign in; no administrator rights are needed)")
		}
		p.HostKey = strings.TrimSpace(p.HostKey)
		if p.HostKey != "" && !hostKeyRE.MatchString(p.HostKey) {
			return errors.New("the host key must be a SHA256 fingerprint like SHA256:abc… (empty: accept the first one)")
		}
		return keep(22)
	case CheckWinRM:
		p.URL, p.Expect, p.Contains, p.Query, p.RType, p.Database, p.Path, p.Auth, p.Unit, p.Scale, p.HV, p.HostKey = "", 0, "", "", "", "", "", "", "", 0, "", ""
		if p.User == "" || len(p.User) > 128 || strings.ContainsAny(p.User, " \t\r\n") || strings.Count(p.User, `\`) > 1 {
			return errors.New("enter the user: a local name, a domain user (DOMAIN and the name) or name@domain")
		}
		if !p.TLS {
			p.Insecure = false
			return keep(5985)
		}
		return keep(5986)
	case CheckHypervisor:
		p.URL, p.Expect, p.Contains, p.Query, p.RType, p.Database, p.Path, p.Auth, p.TLS, p.Unit, p.Scale = "", 0, "", "", "", "", "", "", false, "", 0
		if p.HV != "vsphere" && p.HV != "proxmox" {
			return errors.New("choose the hypervisor platform")
		}
		if p.User == "" || len(p.User) > 128 || strings.ContainsAny(p.User, " \t\r\n") {
			return errors.New("enter the user (a read-only role is enough)")
		}
		if p.HV == "proxmox" {
			return keep(8006)
		}
		return keep(443)
	case CheckScenario:
		p.Port, p.Query, p.RType, p.User, p.Database, p.Path, p.Auth, p.TLS, p.Unit, p.Scale, p.Expect, p.Contains = 0, "", "", "", "", "", "", false, "", 0, 0, ""
		if p.URL != "" {
			if err := checkURL(p.URL); err != nil {
				return err
			}
		}
		return cleanSteps(p.Steps, p.URL)
	case CheckJSON:
		p.Expect, p.Contains, p.Query, p.RType, p.Database, p.TLS = 0, "", "", "", "", false
		if err := checkURL(p.URL); err != nil {
			return err
		}
		p.Port = 0
		if !jsonPathRE.MatchString(p.Path) || len(p.Path) > 200 {
			return errors.New("enter where the number is in the answer, for example results.cpu or data.0.value")
		}
		switch p.Auth {
		case "":
			p.User, p.Secret = "", ""
		case "bearer":
			p.User = ""
		case "basic":
			if !apiUserRE.MatchString(p.User) {
				return errors.New("enter the user for basic sign-in")
			}
		default:
			return errors.New("unknown sign-in method")
		}
		p.Unit = strings.TrimSpace(p.Unit)
		if !unitRE.MatchString(p.Unit) {
			//lint:ignore ST1005 the sentence names the dot as an allowed sign
			return errors.New("the unit may have up to 16 letters, digits or % / ° .")
		}
	}
	return nil
}

func (p *CheckParams) defaultName(typ string) string {
	switch typ {
	case CheckHTTP:
		if p.URL != "" {
			if u, err := url.Parse(p.URL); err == nil {
				return "HTTP " + u.Host + u.Path
			}
		}
		return "HTTP"
	case CheckTLS:
		return "Certificate :" + strconv.Itoa(p.Port)
	case CheckDNS:
		return "DNS " + p.Query + " " + p.RType
	case CheckPostgres:
		return "PostgreSQL"
	case CheckMySQL:
		return "MySQL"
	case CheckRouterOS:
		return "RouterOS"
	case CheckJSON:
		return "API " + p.Path
	case CheckScenario:
		return "Web scenario"
	case CheckSSH:
		return "Linux (SSH)"
	case CheckWinRM:
		return "Windows (WinRM)"
	case CheckHypervisor:
		if p.HV == "proxmox" {
			return "Proxmox VE"
		}
		return "VMware vSphere"
	}
	return typ
}

// HasSecret reports whether a check type takes a password or token.
func HasSecret(typ string) bool {
	return typ == CheckPostgres || typ == CheckMySQL || typ == CheckRouterOS || typ == CheckJSON || typ == CheckHypervisor || typ == CheckScenario || typ == CheckSSH || typ == CheckWinRM
}

const checkSecretPurpose = "check.secret"

// sealCheckSecret seals a new password; an empty one keeps the stored one.
func (s *Store) sealCheckSecret(p *CheckParams, old string) {
	if p.Secret == "" {
		p.Secret = old
		return
	}
	p.Secret = s.SealSecret(checkSecretPurpose, p.Secret)
}

// CheckSecret opens the sealed password of a check ("" when it has none).
func (s *Store) CheckSecret(c *Check) (string, error) {
	if c.Params.Secret == "" {
		return "", nil
	}
	return s.OpenSecret(checkSecretPurpose, c.Params.Secret)
}

// IsService tells whether a check type is a service check (internal/checks).
func IsService(typ string) bool {
	switch typ {
	case CheckHTTP, CheckTLS, CheckDNS, CheckPostgres, CheckMySQL, CheckRouterOS, CheckJSON, CheckHypervisor, CheckScenario, CheckSSH, CheckWinRM:
		return true
	}
	return false
}

// WebStep is one request of a web scenario.
type WebStep struct {
	Method   string `json:"method"`             // GET or POST
	URL      string `json:"url"`                // absolute, or a path from the scenario's address
	Body     string `json:"body,omitempty"`     // POST form, e.g. user=admin&password={secret}
	Expect   int    `json:"expect,omitempty"`   // status (0: any below 400)
	Contains string `json:"contains,omitempty"` // text the answer must contain
}

// cleanSteps checks the steps of a web scenario.
func cleanSteps(steps []WebStep, base string) error {
	if len(steps) == 0 || len(steps) > 10 {
		return errors.New("a web scenario has 1–10 steps")
	}
	for i := range steps {
		s := &steps[i]
		s.Method, s.URL, s.Contains = strings.ToUpper(strings.TrimSpace(s.Method)), strings.TrimSpace(s.URL), strings.TrimSpace(s.Contains)
		n := strconv.Itoa(i + 1)
		if s.Method != "GET" && s.Method != "POST" {
			return errors.New("step " + n + ": the method must be GET or POST")
		}
		if strings.HasPrefix(s.URL, "/") {
			if strings.HasPrefix(s.URL, "//") || len(s.URL) > 2000 || strings.ContainsAny(s.URL, " \t\r\n") {
				return errors.New("step " + n + ": the address is not a path")
			}
		} else if err := checkURL(s.URL); err != nil {
			return errors.New("step " + n + ": " + err.Error())
		}
		if len(s.Body) > 4000 || strings.ContainsAny(s.Body, "\r\n") {
			return errors.New("step " + n + ": the form data is too long")
		}
		if strings.Contains(s.Body, "{secret}") && !SecretStep(base, s.URL) {
			return errors.New("step " + n + ": the password goes only to the scenario's own address")
		}
		if s.Method == "GET" {
			s.Body = ""
		}
		if s.Expect != 0 && (s.Expect < 100 || s.Expect > 599) {
			return errors.New("step " + n + ": the expected status must be 100–599")
		}
		if len(s.Contains) > 200 {
			return errors.New("step " + n + ": the text to look for is too long")
		}
		if strings.Contains(s.Contains, "|") || strings.Contains(s.URL, "|") {
			return errors.New("step " + n + ": the address and the text to look for cannot contain |")
		}
	}
	return nil
}

// ParseSteps reads the steps of a web scenario, one per line:
//
//	METHOD URL | expected status | text on the page | form data
//
// for example "POST /login | 302 | | user=admin&password={secret}".
func ParseSteps(text string) ([]WebStep, error) {
	var out []WebStep
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		head := strings.Fields(parts[0])
		if len(head) != 2 {
			return nil, errors.New("each step starts with GET or POST and the address: " + line)
		}
		s := WebStep{Method: head[0], URL: head[1]}
		if len(parts) > 1 {
			if v := strings.TrimSpace(parts[1]); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil {
					return nil, errors.New("the expected status must be a number: " + line)
				}
				s.Expect = n
			}
		}
		if len(parts) > 2 {
			s.Contains = strings.TrimSpace(parts[2])
		}
		if len(parts) > 3 {
			s.Body = strings.TrimSpace(parts[3])
		}
		out = append(out, s)
	}
	return out, nil
}

// StepsText writes steps back in the form ParseSteps reads.
func StepsText(steps []WebStep) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(s.Method + " " + s.URL)
		if s.Expect != 0 || s.Contains != "" || s.Body != "" {
			b.WriteString(" | ")
			if s.Expect != 0 {
				b.WriteString(strconv.Itoa(s.Expect))
			}
			if s.Contains != "" || s.Body != "" {
				b.WriteString(" | " + s.Contains)
			}
			if s.Body != "" {
				b.WriteString(" | " + s.Body)
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// SecretStep tells whether a scenario step may carry the check's password:
// a path on the scenario's address, or an absolute address on the same
// host (never another site).
func SecretStep(base, step string) bool {
	if strings.HasPrefix(step, "/") {
		return true
	}
	b, err1 := url.Parse(base)
	s, err2 := url.Parse(step)
	return base != "" && err1 == nil && err2 == nil && b.Host != "" && strings.EqualFold(b.Host, s.Host) && b.Scheme == s.Scheme
}

// secretHosts are the addresses a check sends its password to (beyond the
// device): when they change, the stored password is not kept.
func secretHosts(typ string, p CheckParams) string {
	hosts := []string{}
	if u, err := url.Parse(p.URL); err == nil {
		hosts = append(hosts, strings.ToLower(u.Scheme+"://"+u.Host))
	}
	if typ == CheckScenario {
		for _, s := range p.Steps {
			if u, err := url.Parse(s.URL); err == nil && u.Host != "" {
				hosts = append(hosts, strings.ToLower(u.Scheme+"://"+u.Host))
			}
		}
	}
	return strings.Join(hosts, " ")
}
