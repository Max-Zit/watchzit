// Package checks runs the service checks: web pages, TLS certificates,
// DNS, PostgreSQL, MySQL/MariaDB, the MikroTik RouterOS REST API and numbers
// from any JSON API. Each returns a Result for the poller.
package checks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"golang.org/x/net/dns/dnsmessage"

	"github.com/max-zit/watchzit/internal/store"
)

// Result is the outcome of one run.
type Result struct {
	Up     bool
	Text   string
	Values map[string]float64
}

func fail(format string, a ...any) Result { return Result{Text: fmt.Sprintf(format, a...)} }

// Target is where a check goes: the device's name or address as entered,
// the address it resolves to, and the check's password or token.
type Target struct {
	Host   string
	Addr   netip.Addr
	Secret string
}

func msSince(t time.Time) float64 {
	return math.Round(float64(time.Since(t))/float64(time.Millisecond)*100) / 100
}

func hostPort(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) }

// Run runs a service check.
func Run(ctx context.Context, typ string, p store.CheckParams, t Target, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch typ {
	case store.CheckHTTP:
		return HTTP(ctx, p, t)
	case store.CheckTLS:
		return TLS(ctx, p, t)
	case store.CheckDNS:
		return DNS(ctx, p, t)
	case store.CheckPostgres:
		return Postgres(ctx, p, t)
	case store.CheckMySQL:
		return MySQL(ctx, p, t)
	case store.CheckRouterOS:
		return RouterOS(ctx, p, t)
	case store.CheckJSON:
		return JSONValue(ctx, p, t)
	}
	return fail("unknown check type")
}

// ---- HTTP(S)

func httpClient(insecure bool) *http.Client {
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: insecure, MinVersion: tls.VersionTLS12}, // #nosec G402 -- the user chose to accept untrusted certificates for this check
		DisableKeepAlives: true, ForceAttemptHTTP2: true, Proxy: nil}
	return &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("more than 5 redirects")
		}
		return nil
	}}
}

// pageURL is the check's address, or the device's web root.
func pageURL(p store.CheckParams, t Target) string {
	if p.URL != "" {
		return p.URL
	}
	return "https://" + hostPort(t.Host, 443) + "/"
}

func certDays(c *x509.Certificate) float64 {
	return math.Floor(time.Until(c.NotAfter).Hours()/24*10) / 10
}

// HTTP fetches a page: status, time, optional text, certificate validity.
func HTTP(ctx context.Context, p store.CheckParams, t Target) Result {
	u := pageURL(p, t)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fail("%v", err)
	}
	req.Header.Set("User-Agent", "WatchZit")
	start := time.Now()
	resp, err := httpClient(p.Insecure).Do(req) // #nosec G704 -- the address is set by an operator: checking it is the purpose
	if err != nil {
		return fail("%s", shortErr(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	r := Result{Values: map[string]float64{"response_ms": msSince(start), "status_code": float64(resp.StatusCode)}}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		r.Values["cert_days"] = certDays(resp.TLS.PeerCertificates[0])
	}
	ok := resp.StatusCode < 400
	if p.Expect != 0 {
		ok = resp.StatusCode == p.Expect
	}
	r.Text = fmt.Sprintf("%s, %.0f ms", resp.Status, r.Values["response_ms"])
	if !ok {
		if p.Expect != 0 {
			r.Text += fmt.Sprintf(" (expected %d)", p.Expect)
		}
		return r
	}
	if p.Contains != "" && !strings.Contains(string(body), p.Contains) {
		r.Text += fmt.Sprintf(", %q not found", p.Contains)
		return r
	}
	r.Up = true
	return r
}

// shortErr drops Go's request prefix from network errors.
func shortErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	s := err.Error()
	if errors.Is(err, context.DeadlineExceeded) {
		s = "no answer within the timeout"
	}
	return s
}

// ---- TLS certificates

// TLS connects to a TLS port and reports how long its certificate is valid.
func TLS(ctx context.Context, p store.CheckParams, t Target) Result {
	sni := p.Query
	if sni == "" {
		if _, err := netip.ParseAddr(t.Host); err != nil {
			sni = t.Host
		}
	}
	d := &tls.Dialer{NetDialer: &net.Dialer{}, Config: &tls.Config{ServerName: sni, InsecureSkipVerify: true}} // #nosec G402 -- verified below, so an untrusted certificate still reports its days
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", hostPort(t.Addr.String(), p.Port))
	if err != nil {
		return fail("%s", shortErr(err))
	}
	defer conn.Close()
	st := conn.(*tls.Conn).ConnectionState()
	if len(st.PeerCertificates) == 0 {
		return fail("no certificate")
	}
	leaf := st.PeerCertificates[0]
	days := certDays(leaf)
	r := Result{Values: map[string]float64{"cert_days": days, "handshake_ms": msSince(start)}}
	who := leaf.Subject.CommonName
	if who == "" && len(leaf.DNSNames) > 0 {
		who = leaf.DNSNames[0]
	}
	r.Text = fmt.Sprintf("%s, valid until %s (%.0f days)", who, leaf.NotAfter.UTC().Format("2006-01-02"), math.Floor(days))
	if days < 0 {
		r.Text = fmt.Sprintf("%s, expired on %s", who, leaf.NotAfter.UTC().Format("2006-01-02"))
		return r
	}
	if !p.Insecure {
		inter := x509.NewCertPool()
		for _, c := range st.PeerCertificates[1:] {
			inter.AddCert(c)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: sni, Intermediates: inter}); err != nil {
			r.Text += ", not trusted: " + err.Error()
			return r
		}
	}
	r.Up = true
	return r
}

// ---- DNS

var dnsTypes = map[string]dnsmessage.Type{"A": dnsmessage.TypeA, "AAAA": dnsmessage.TypeAAAA, "CNAME": dnsmessage.TypeCNAME, "MX": dnsmessage.TypeMX,
	"NS": dnsmessage.TypeNS, "TXT": dnsmessage.TypeTXT, "SOA": dnsmessage.TypeSOA, "PTR": dnsmessage.TypePTR}

// DNS asks the device (a DNS server) for a name.
func DNS(ctx context.Context, p store.CheckParams, t Target) Result {
	name := p.Query
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	qn, err := dnsmessage.NewName(name)
	if err != nil {
		return fail("%v", err)
	}
	id := uint16(time.Now().UnixNano()) // #nosec G115 -- a query id, any 16 bits
	msg := dnsmessage.Message{Header: dnsmessage.Header{ID: id, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: qn, Type: dnsTypes[p.RType], Class: dnsmessage.ClassINET}}}
	q, err := msg.Pack()
	if err != nil {
		return fail("%v", err)
	}
	var dl net.Dialer
	start := time.Now()
	conn, err := dl.DialContext(ctx, "udp", hostPort(t.Addr.String(), p.Port))
	if err != nil {
		return fail("%s", shortErr(err))
	}
	defer conn.Close()
	if dlt, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dlt)
	}
	if _, err := conn.Write(q); err != nil {
		return fail("%s", shortErr(err))
	}
	buf := make([]byte, 4096)
	var resp dnsmessage.Message
	for {
		n, err := conn.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return fail("no answer within the timeout")
			}
			return fail("%s", shortErr(err))
		}
		if err := resp.Unpack(buf[:n]); err == nil && resp.ID == id {
			break
		}
	}
	r := Result{Values: map[string]float64{"response_ms": msSince(start)}}
	if resp.RCode != dnsmessage.RCodeSuccess {
		r.Text = "answer: " + strings.TrimPrefix(resp.RCode.String(), "RCode")
		return r
	}
	var answers []string
	for _, a := range resp.Answers {
		if s := rrText(a); s != "" {
			answers = append(answers, s)
		}
	}
	if len(answers) == 0 {
		r.Text = fmt.Sprintf("no %s record, %.0f ms", p.RType, r.Values["response_ms"])
		return r
	}
	r.Text = fmt.Sprintf("%s, %.0f ms", strings.Join(limit(answers, 4), ", "), r.Values["response_ms"])
	if p.Contains != "" {
		found := false
		for _, a := range answers {
			found = found || strings.Contains(strings.ToLower(a), strings.ToLower(p.Contains))
		}
		if !found {
			r.Text += fmt.Sprintf(" (%q not in the answer)", p.Contains)
			return r
		}
	}
	r.Up = true
	return r
}

func limit(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], "…")
	}
	return s
}

func rrText(a dnsmessage.Resource) string {
	switch b := a.Body.(type) {
	case *dnsmessage.AResource:
		return netip.AddrFrom4(b.A).String()
	case *dnsmessage.AAAAResource:
		return netip.AddrFrom16(b.AAAA).String()
	case *dnsmessage.CNAMEResource:
		return b.CNAME.String()
	case *dnsmessage.MXResource:
		return strconv.Itoa(int(b.Pref)) + " " + b.MX.String()
	case *dnsmessage.NSResource:
		return b.NS.String()
	case *dnsmessage.PTRResource:
		return b.PTR.String()
	case *dnsmessage.TXTResource:
		return strings.Join(b.TXT, "")
	case *dnsmessage.SOAResource:
		return b.NS.String() + " " + strconv.FormatUint(uint64(b.Serial), 10)
	}
	return ""
}

// ---- databases

// Postgres signs in, asks for the version and counts connections.
func Postgres(ctx context.Context, p store.CheckParams, t Target) Result {
	db := p.Database
	if db == "" {
		db = "postgres"
	}
	// a keyword string, so the TLS attempt and its plain fallback both use
	// this host and port (user and database are checked when saved)
	q := func(s string) string { return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'" }
	cfg, err := pgx.ParseConfig(fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=prefer application_name=WatchZit",
		q(t.Addr.String()), p.Port, q(p.User), q(db)))
	if err != nil {
		return fail("%v", err)
	}
	cfg.Password = t.Secret
	start := time.Now()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return fail("%s", dbErr(err, p.User))
	}
	defer conn.Close(context.WithoutCancel(ctx))
	var version string
	var n int64
	if err := conn.QueryRow(ctx, `SELECT current_setting('server_version'), (SELECT count(*) FROM pg_stat_activity)`).Scan(&version, &n); err != nil {
		return fail("%s", dbErr(err, p.User))
	}
	ms := msSince(start)
	return Result{Up: true, Text: fmt.Sprintf("PostgreSQL %s, %d connections, %.0f ms", strings.Fields(version + " ")[0], n, ms),
		Values: map[string]float64{"response_ms": ms, "connections": float64(n)}}
}

// MySQL signs in to MySQL or MariaDB, asks for the version and connections.
func MySQL(ctx context.Context, p store.CheckParams, t Target) Result {
	cfg := mysql.NewConfig()
	cfg.Net, cfg.Addr, cfg.User, cfg.Passwd, cfg.DBName = "tcp", hostPort(t.Addr.String(), p.Port), p.User, t.Secret, p.Database
	cfg.TLSConfig = "preferred"
	if dl, ok := ctx.Deadline(); ok {
		cfg.Timeout, cfg.ReadTimeout = time.Until(dl), time.Until(dl)
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return fail("%v", err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)
	start := time.Now()
	var version, name string
	var n int64
	if err := db.QueryRowContext(ctx, `SELECT VERSION()`).Scan(&version); err != nil {
		return fail("%s", dbErr(err, p.User))
	}
	if err := db.QueryRowContext(ctx, `SHOW GLOBAL STATUS LIKE 'Threads_connected'`).Scan(&name, &n); err != nil {
		return fail("%s", dbErr(err, p.User))
	}
	ms := msSince(start)
	kind := "MySQL"
	if strings.Contains(strings.ToLower(version), "mariadb") {
		kind = "MariaDB"
	}
	return Result{Up: true, Text: fmt.Sprintf("%s %s, %d connections, %.0f ms", kind, strings.SplitN(version, "-", 2)[0], n, ms),
		Values: map[string]float64{"response_ms": ms, "connections": float64(n)}}
}

func dbErr(err error, user string) string {
	s := shortErr(err)
	if i := strings.Index(s, "failed to connect to"); i >= 0 {
		if j := strings.LastIndex(s, ": "); j > i {
			s = s[j+2:]
		}
	}
	return strings.ReplaceAll(s, "\n", " ")
}

// ---- MikroTik RouterOS REST API (RouterOS 7)

// RouterOS reads CPU, memory, uptime and temperature over the REST API.
func RouterOS(ctx context.Context, p store.CheckParams, t Target) Result {
	get := rosGetter(ctx, p, t)
	start := time.Now()
	var res map[string]any
	if err := get("system/resource", &res); err != nil {
		return fail("%v", err)
	}
	r := Result{Up: true, Values: map[string]float64{}}
	if v, ok := num(res["cpu-load"]); ok {
		r.Values["cpu_pct"] = v
	}
	free, ok1 := num(res["free-memory"])
	total, ok2 := num(res["total-memory"])
	if ok1 && ok2 && total > 0 {
		r.Values["mem_pct"] = math.Round((1-free/total)*10000) / 100
	}
	if up, ok := res["uptime"].(string); ok {
		if s, ok := rosDuration(up); ok {
			r.Values["uptime_s"] = s
		}
	}
	var health []map[string]any
	if get("system/health", &health) == nil {
		for _, h := range health {
			if n, _ := h["name"].(string); strings.Contains(n, "temperature") {
				if v, ok := num(h["value"]); ok {
					r.Values["temperature"] = v
					break
				}
			}
		}
	}
	ver, _ := res["version"].(string)
	board, _ := res["board-name"].(string)
	r.Text = fmt.Sprintf("RouterOS %s %s, CPU %.0f%%, memory %.0f%%, %.0f ms", strings.Fields(ver + " ")[0], board, r.Values["cpu_pct"], r.Values["mem_pct"], msSince(start))
	if t, ok := r.Values["temperature"]; ok {
		r.Text += fmt.Sprintf(", %.0f °C", t)
	}
	return r
}

// RouterVLAN is a VLAN on a port of a RouterOS bridge.
type RouterVLAN struct {
	ID     int
	Name   string
	Port   string // interface name; "" for a VLAN on no port
	Tagged bool
}

// RouterOSVLANs reads the VLANs of the bridges (bridge VLAN filtering) with
// the ports that currently carry them; names come from the VLAN interfaces
// or the comments. The bridge itself (the router's own port) is left out.
func RouterOSVLANs(ctx context.Context, p store.CheckParams, t Target) ([]RouterVLAN, error) {
	get := rosGetter(ctx, p, t)
	var rows []map[string]any
	if err := get("interface/bridge/vlan", &rows); err != nil {
		return nil, err
	}
	var ifs []map[string]any
	names := map[int]string{}
	if get("interface/vlan", &ifs) == nil {
		for _, v := range ifs {
			if id, ok := num(v["vlan-id"]); ok && names[int(id)] == "" {
				names[int(id)], _ = v["name"].(string)
			}
		}
	}
	var out []RouterVLAN
	for _, r := range rows {
		if d, _ := r["disabled"].(string); d == "true" {
			continue
		}
		bridge, _ := r["bridge"].(string)
		comment, _ := r["comment"].(string)
		if dyn, _ := r["dynamic"].(string); dyn == "true" {
			comment = "" // "added by pvid" and the like
		}
		for _, id := range rosIDs(fmt.Sprint(r["vlan-ids"])) {
			name := names[id]
			if name == "" {
				name = comment
			}
			n := len(out)
			for _, part := range []struct {
				key    string
				tagged bool
			}{{"current-tagged", true}, {"current-untagged", false}} {
				list, _ := r[part.key].(string)
				for _, port := range strings.Split(list, ",") {
					if port = strings.TrimSpace(port); port != "" && port != bridge {
						out = append(out, RouterVLAN{ID: id, Name: name, Port: port, Tagged: part.tagged})
					}
				}
			}
			if len(out) == n {
				out = append(out, RouterVLAN{ID: id, Name: name})
			}
		}
	}
	return out, nil
}

// RouterOSVXLANs reads the VXLAN interfaces (RouterOS 7) with their remote
// VTEPs. A router without VXLAN support answers 404 or an empty list: none.
func RouterOSVXLANs(ctx context.Context, p store.CheckParams, t Target) ([]store.VXLAN, error) {
	get := rosGetter(ctx, p, t)
	var ifs []map[string]any
	if err := get("interface/vxlan", &ifs); err != nil {
		return nil, err
	}
	var vteps []map[string]any
	remotes := map[string][]string{}
	if get("interface/vxlan/vteps", &vteps) == nil {
		for _, v := range vteps {
			name, _ := v["interface"].(string)
			if ip, _ := v["remote-ip"].(string); ip != "" {
				remotes[name] = append(remotes[name], ip)
			}
		}
	}
	var out []store.VXLAN
	for _, f := range ifs {
		if d, _ := f["disabled"].(string); d == "true" {
			continue
		}
		vni, ok := num(f["vni"])
		if !ok {
			continue
		}
		x := store.VXLAN{VNI: int(vni)}
		x.Name, _ = f["name"].(string)
		x.Local, _ = f["local-address"].(string)
		if port, ok := num(f["port"]); ok {
			x.Port = int(port)
		}
		x.Remotes = remotes[x.Name]
		out = append(out, x)
	}
	return out, nil
}

// rosIDs reads "10", "10,20" or "100-105" (at most 4094 numbers).
func rosIDs(s string) []int {
	var out []int
	for _, part := range strings.Split(s, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			continue
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil {
				continue
			}
		}
		for id := max(a, 1); id <= min(b, 4094) && len(out) < 4094; id++ {
			out = append(out, id)
		}
	}
	return out
}

// rosGetter returns a function that reads a path of the RouterOS REST API
// (e.g. "system/resource") into v.
func rosGetter(ctx context.Context, p store.CheckParams, t Target) func(path string, v any) error {
	scheme := "http"
	if p.TLS {
		scheme = "https"
	}
	base := scheme + "://" + hostPort(t.Host, p.Port) + "/rest/"
	cl := httpClient(p.Insecure)
	return func(path string, v any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return err
		}
		req.SetBasicAuth(p.User, t.Secret)
		resp, err := cl.Do(req) // #nosec G704 -- the router's address
		if err != nil {
			return errors.New(shortErr(err))
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			return errors.New("wrong user or password")
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("the router answered %s (RouterOS 7 with the www or www-ssl service is needed)", resp.Status)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
	}
}

// num reads a JSON number, a numeric string or a boolean.
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(x, "%")), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// rosDuration reads RouterOS uptimes like "1w2d03:04:05" or "3h4m5s".
func rosDuration(s string) (float64, bool) {
	units := map[byte]float64{'w': 7 * 86400, 'd': 86400, 'h': 3600, 'm': 60, 's': 1}
	var total, n float64
	have := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			n = n*10 + float64(c-'0')
			have = true
		case units[c] != 0:
			total += n * units[c]
			n, have = 0, false
		case c == ':':
			// hh:mm:ss after the days
			parts := strings.Split(s[i-countDigitsBack(s, i):], ":")
			if len(parts) == 3 {
				h, e1 := strconv.Atoi(parts[0])
				m, e2 := strconv.Atoi(parts[1])
				sec, e3 := strconv.Atoi(parts[2])
				if e1 == nil && e2 == nil && e3 == nil {
					return total + float64(h*3600+m*60+sec), true
				}
			}
			return 0, false
		default:
			return 0, false
		}
	}
	if have {
		total += n
	}
	return total, true
}

func countDigitsBack(s string, i int) int {
	n := 0
	for j := i - 1; j >= 0 && s[j] >= '0' && s[j] <= '9'; j-- {
		n++
	}
	return n
}

// ---- any JSON API

// JSONValue fetches a JSON answer and reads one number from it.
func JSONValue(ctx context.Context, p store.CheckParams, t Target) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return fail("%v", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "WatchZit")
	switch p.Auth {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+t.Secret)
	case "basic":
		req.SetBasicAuth(p.User, t.Secret)
	}
	start := time.Now()
	resp, err := httpClient(p.Insecure).Do(req) // #nosec G704 -- the address is set by an operator
	if err != nil {
		return fail("%s", shortErr(err))
	}
	defer resp.Body.Close()
	ms := msSince(start)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{Text: resp.Status, Values: map[string]float64{"response_ms": ms}}
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return Result{Text: "the answer is not JSON", Values: map[string]float64{"response_ms": ms}}
	}
	v, err := Lookup(doc, p.Path)
	if err != nil {
		return Result{Text: err.Error(), Values: map[string]float64{"response_ms": ms}}
	}
	if p.Scale != 0 {
		v *= p.Scale
	}
	v = math.Round(v*10000) / 10000
	return Result{Up: true, Text: strings.TrimSpace(strconv.FormatFloat(v, 'f', -1, 64) + " " + p.Unit),
		Values: map[string]float64{"value": v, "response_ms": ms}}
}

// Lookup follows a dot path (a.b.0.c) in decoded JSON to a number.
func Lookup(doc any, path string) (float64, error) {
	cur := doc
	for _, part := range strings.Split(path, ".") {
		switch x := cur.(type) {
		case map[string]any:
			v, ok := x[part]
			if !ok {
				return 0, fmt.Errorf("%q is not in the answer", part)
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return 0, fmt.Errorf("the list has no item %q", part)
			}
			cur = x[i]
		default:
			return 0, fmt.Errorf("%q is below a single value", part)
		}
	}
	f, ok := num(cur)
	if !ok {
		return 0, errors.New("the value at " + path + " is not a number")
	}
	return f, nil
}
