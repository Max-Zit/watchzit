// Package logrecv receives syslog messages (RFC 3164 and RFC 5424 over UDP)
// and SNMP v1/v2c traps and informs from devices, finds the device by the
// sender's address and stores the messages in batches.
package logrecv

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/snmp"
	"github.com/max-zit/watchzit/internal/store"
)

// Config is what Settings → Syslog and traps sets.
type Config struct {
	Syslog      bool     `json:"syslog"`
	SyslogPort  int      `json:"syslog_port"`
	Traps       bool     `json:"traps"`
	TrapPort    int      `json:"trap_port"`
	KeepDays    int      `json:"keep_days"`
	Communities []string `json:"communities,omitempty"` // accepted trap communities (empty: any)
	Unknown     bool     `json:"unknown"`               // also keep messages of addresses no device has
}

// Defaults: off, standard ports, 30 days.
func Defaults() Config {
	return Config{SyslogPort: 514, TrapPort: 162, KeepDays: 30, Unknown: true}
}

// Store is what the receiver needs of the database.
type Store interface {
	AddLogs(ctx context.Context, ms []store.LogMessage) error
	DeviceAddresses(ctx context.Context) (map[netip.Addr]int64, error)
	PruneLogs(ctx context.Context, before time.Time) (int64, error)
}

// Receiver runs the listeners.
type Receiver struct {
	st  Store
	log *slog.Logger
	now func() time.Time

	mu      sync.Mutex
	cfg     Config
	conns   []net.PacketConn
	addrs   map[netip.Addr]int64
	queue   []store.LogMessage
	dropped int
	lastErr string
	// Received counts the messages since the start (status page).
	Received int64
}

// New makes a receiver; Run starts it.
func New(st Store, log *slog.Logger) *Receiver {
	return &Receiver{st: st, log: log, now: time.Now, addrs: map[netip.Addr]int64{}}
}

// Status is the state for the settings page.
func (r *Receiver) Status() (cfg Config, received int64, dropped int, lastErr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg, r.Received, r.dropped, r.lastErr
}

// Apply (re)opens the listeners for cfg.
func (r *Receiver) Apply(cfg Config) error {
	r.mu.Lock()
	for _, c := range r.conns {
		_ = c.Close()
	}
	r.conns, r.cfg, r.lastErr = nil, cfg, ""
	r.mu.Unlock()
	var errs []string
	if cfg.Syslog {
		if c, err := net.ListenPacket("udp", ":"+strconv.Itoa(cfg.SyslogPort)); err != nil {
			errs = append(errs, "syslog port "+strconv.Itoa(cfg.SyslogPort)+": "+err.Error())
		} else {
			r.add(c)
			go r.serve(c, false)
		}
	}
	if cfg.Traps {
		if c, err := net.ListenPacket("udp", ":"+strconv.Itoa(cfg.TrapPort)); err != nil {
			errs = append(errs, "trap port "+strconv.Itoa(cfg.TrapPort)+": "+err.Error())
		} else {
			r.add(c)
			go r.serve(c, true)
		}
	}
	if len(errs) > 0 {
		r.mu.Lock()
		r.lastErr = strings.Join(errs, "; ")
		r.mu.Unlock()
		return &applyError{r.lastErr}
	}
	return nil
}

type applyError struct{ s string }

func (e *applyError) Error() string { return e.s }

func (r *Receiver) add(c net.PacketConn) {
	r.mu.Lock()
	r.conns = append(r.conns, c)
	r.mu.Unlock()
}

// Addr is the local address of the first listener (tests).
func (r *Receiver) Addr(trap bool) net.Addr {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		p := c.LocalAddr().(*net.UDPAddr).Port
		if (trap && p == r.cfgPort(true)) || (!trap && p == r.cfgPort(false)) {
			return c.LocalAddr()
		}
	}
	if len(r.conns) > 0 {
		return r.conns[0].LocalAddr()
	}
	return nil
}

func (r *Receiver) cfgPort(trap bool) int {
	if trap {
		return r.cfg.TrapPort
	}
	return r.cfg.SyslogPort
}

// maxQueue bounds the messages waiting to be stored (a flood is dropped).
const maxQueue = 20000

func (r *Receiver) serve(c net.PacketConn, trap bool) {
	buf := make([]byte, 65535)
	for {
		n, from, err := c.ReadFrom(buf)
		if err != nil {
			return // closed by Apply or Close
		}
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			continue
		}
		src, _ := netip.AddrFromSlice(ua.IP)
		src = src.Unmap()
		m := store.LogMessage{At: r.now(), Source: src}
		if trap {
			t, err := snmp.ParseTrap(buf[:n])
			if err != nil || !r.community(t.Community) {
				continue
			}
			if a := t.InformAnswer(); a != nil {
				_, _ = c.WriteTo(a, from)
			}
			m.Kind, m.Facility, m.Severity, m.Message = store.LogTrap, -1, 4, t.Text()
			m.Tag = "snmp-trap"
			if t.Version == 1 && t.Agent.IsValid() && !t.Agent.IsUnspecified() {
				m.Host = t.Agent.String()
			}
		} else {
			m.Kind = store.LogSyslog
			m.Facility, m.Severity, m.Host, m.Tag, m.Message = ParseSyslog(buf[:n])
			if m.Message == "" {
				continue
			}
		}
		r.enqueue(m)
	}
}

func (r *Receiver) community(c string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cfg.Communities) == 0 {
		return true
	}
	for _, x := range r.cfg.Communities {
		if x == c {
			return true
		}
	}
	return false
}

func (r *Receiver) enqueue(m store.LogMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m.DeviceID = r.addrs[m.Source]
	if m.DeviceID == 0 && !r.cfg.Unknown {
		return
	}
	if len(r.queue) >= maxQueue {
		r.dropped++
		return
	}
	r.queue = append(r.queue, m)
	r.Received++
}

// Flush stores the waiting messages.
func (r *Receiver) Flush(ctx context.Context) error {
	r.mu.Lock()
	q := r.queue
	r.queue = nil
	r.mu.Unlock()
	for len(q) > 0 {
		n := min(len(q), 1000)
		if err := r.st.AddLogs(ctx, q[:n]); err != nil {
			return err
		}
		q = q[n:]
	}
	return nil
}

// refresh reloads the device addresses.
func (r *Receiver) refresh(ctx context.Context) {
	a, err := r.st.DeviceAddresses(ctx)
	if err != nil {
		r.log.Error("syslog: device addresses", "err", err)
		return
	}
	r.mu.Lock()
	r.addrs = a
	r.mu.Unlock()
}

// Run stores messages every second, reloads device addresses every minute
// and deletes old messages every hour, until ctx ends.
func (r *Receiver) Run(ctx context.Context) {
	r.refresh(ctx)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	defer r.Close()
	var n int
	for {
		select {
		case <-ctx.Done():
			_ = r.Flush(context.Background())
			return
		case <-tick.C:
			if err := r.Flush(ctx); err != nil {
				r.log.Error("syslog: storing messages", "err", err)
			}
			n++
			if n%60 == 0 {
				r.refresh(ctx)
			}
			if n%3600 == 1 {
				r.mu.Lock()
				days := r.cfg.KeepDays
				r.mu.Unlock()
				if days > 0 {
					if _, err := r.st.PruneLogs(ctx, r.now().AddDate(0, 0, -days)); err != nil {
						r.log.Error("syslog: deleting old messages", "err", err)
					}
				}
			}
		}
	}
}

// Close stops the listeners.
func (r *Receiver) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		_ = c.Close()
	}
	r.conns = nil
}

// Refresh reloads the device addresses now (tests, after adding devices).
func (r *Receiver) Refresh(ctx context.Context) { r.refresh(ctx) }

// SetClock replaces the clock (tests).
func (r *Receiver) SetClock(now func() time.Time) { r.now = now }

var months = map[string]bool{"Jan": true, "Feb": true, "Mar": true, "Apr": true, "May": true, "Jun": true, "Jul": true, "Aug": true, "Sep": true, "Oct": true, "Nov": true, "Dec": true}

// ParseSyslog reads a syslog message: RFC 5424 ("<PRI>1 TIMESTAMP HOST APP
// PROCID MSGID SD MSG"), RFC 3164 ("<PRI>Mmm dd hh:mm:ss HOST TAG: MSG") or
// what many devices send in between (no time, no host). Without a priority
// it is user.notice.
func ParseSyslog(b []byte) (facility, severity int, host, tag, msg string) {
	s := strings.TrimRight(strings.ToValidUTF8(string(b), "?"), "\r\n\x00 ")
	facility, severity = 1, 5
	if strings.HasPrefix(s, "<") {
		if i := strings.IndexByte(s, '>'); i > 1 && i <= 4 {
			if p, err := strconv.Atoi(s[1:i]); err == nil && p >= 0 && p <= 191 {
				facility, severity = p/8, p%8
				s = s[i+1:]
			}
		}
	}
	// RFC 5424
	if strings.HasPrefix(s, "1 ") {
		f := strings.SplitN(s[2:], " ", 6)
		if len(f) == 6 {
			host, tag = nil5424(f[1]), nil5424(f[2])
			rest := f[5]
			if strings.HasPrefix(rest, "[") { // structured data
				depth := 0
				for i, c := range rest {
					if c == '[' {
						depth++
					} else if c == ']' {
						depth--
						if depth == 0 && (i+1 == len(rest) || rest[i+1] == ' ') {
							rest = strings.TrimPrefix(rest[i+1:], " ")
							break
						}
					}
				}
			} else {
				rest = strings.TrimPrefix(rest, "- ")
			}
			return facility, severity, host, tag, strings.TrimPrefix(rest, string(rune(0xfeff)))
		}
	}
	// RFC 3164 time stamp
	if len(s) >= 16 && months[s[:3]] && s[3] == ' ' && s[6] == ' ' && s[9] == ':' {
		s = strings.TrimLeft(s[15:], " ")
		// a host name before the tag (no ":" in it)
		if sp := strings.IndexByte(s, ' '); sp > 0 && !strings.ContainsAny(s[:sp], ":[") {
			if next := s[sp+1:]; strings.Contains(strings.SplitN(next, " ", 2)[0], ":") || strings.Contains(strings.SplitN(next, " ", 2)[0], "[") {
				host, s = s[:sp], next
			}
		}
	}
	// TAG[pid]: message
	if i := strings.Index(s, ": "); i > 0 && i < 64 && !strings.ContainsAny(s[:i], " ") {
		tag, s = s[:i], s[i+2:]
		if j := strings.IndexByte(tag, '['); j > 0 {
			tag = tag[:j]
		}
	}
	return facility, severity, host, tag, strings.TrimSpace(s)
}

func nil5424(s string) string {
	if s == "-" {
		return ""
	}
	return s
}
