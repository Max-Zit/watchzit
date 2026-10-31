package flow

import (
	"context"
	"log/slog"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/store"
)

// Config is what Settings → Traffic flows sets.
type Config struct {
	Enabled     bool `json:"enabled"`
	NetFlowPort int  `json:"netflow_port"` // NetFlow v5/v9 and IPFIX
	SFlowPort   int  `json:"sflow_port"`
	KeepDays    int  `json:"keep_days"`
	Unknown     bool `json:"unknown"` // also keep flows of exporters no device has
}

// Defaults: off, the usual ports, 7 days.
func Defaults() Config { return Config{NetFlowPort: 2055, SFlowPort: 6343, KeepDays: 7} }

// Store is what the collector needs of the database.
type Store interface {
	AddFlows(ctx context.Context, rows []store.FlowRow) error
	DeviceAddresses(ctx context.Context) (map[netip.Addr]int64, error)
	PruneFlows(ctx context.Context, before time.Time) (int64, error)
}

// keepTop is how many rows of each exporter a minute keeps; the rest is
// summed into one row without addresses.
const keepTop = 1000

type aggKey struct {
	exporter netip.Addr
	src, dst netip.Addr
	proto    uint8
	port     uint16
}

type agg struct{ bytes, packets uint64 }

// Collector runs the listeners and sums the flows per minute.
type Collector struct {
	st  Store
	log *slog.Logger
	now func() time.Time
	dec *Decoder

	mu      sync.Mutex
	cfg     Config
	conns   []net.PacketConn
	addrs   map[netip.Addr]int64
	minute  time.Time
	sums    map[aggKey]*agg
	lastErr string
	// Received counts the datagrams and records since the start.
	Datagrams, Records int64
	// errors counts datagrams that could not be read
	errors int64
}

// New makes a collector; Run starts it.
func New(st Store, log *slog.Logger) *Collector {
	return &Collector{st: st, log: log, now: time.Now, dec: NewDecoder(), addrs: map[netip.Addr]int64{}, sums: map[aggKey]*agg{}}
}

// Status is the state for the settings page.
func (c *Collector) Status() (cfg Config, datagrams, records, errs int64, lastErr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg, c.Datagrams, c.Records, c.errors, c.lastErr
}

// Apply (re)opens the listeners for cfg.
func (c *Collector) Apply(cfg Config) error {
	c.mu.Lock()
	for _, x := range c.conns {
		_ = x.Close()
	}
	c.conns, c.cfg, c.lastErr = nil, cfg, ""
	c.mu.Unlock()
	if !cfg.Enabled {
		return nil
	}
	var errs []string
	for _, p := range []int{cfg.NetFlowPort, cfg.SFlowPort} {
		if p == 0 {
			continue
		}
		x, err := net.ListenPacket("udp", ":"+strconv.Itoa(p))
		if err != nil {
			errs = append(errs, "port "+strconv.Itoa(p)+": "+err.Error())
			continue
		}
		c.mu.Lock()
		c.conns = append(c.conns, x)
		c.mu.Unlock()
		go c.serve(x)
	}
	if len(errs) > 0 {
		c.mu.Lock()
		c.lastErr = strings.Join(errs, "; ")
		c.mu.Unlock()
		return &applyError{c.lastErr}
	}
	return nil
}

type applyError struct{ s string }

func (e *applyError) Error() string { return e.s }

// Addr is the local address of listener i (tests).
func (c *Collector) Addr(i int) net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i < len(c.conns) {
		return c.conns[i].LocalAddr()
	}
	return nil
}

func (c *Collector) serve(x net.PacketConn) {
	buf := make([]byte, 65535)
	for {
		n, from, err := x.ReadFrom(buf)
		if err != nil {
			return // closed by Apply
		}
		ua, ok := from.(*net.UDPAddr)
		if !ok {
			continue
		}
		src, _ := netip.AddrFromSlice(ua.IP)
		c.Take(src.Unmap(), buf[:n])
	}
}

// Take decodes one datagram of an exporter and adds its flows.
func (c *Collector) Take(exporter netip.Addr, b []byte) {
	// senders that are no device are dropped before anything is decoded or
	// remembered (templates), unless flows of unknown exporters are wanted
	c.mu.Lock()
	c.Datagrams++
	known := c.addrs[exporter] != 0 || c.cfg.Unknown
	c.mu.Unlock()
	if !known {
		return
	}
	recs, err := c.dec.Decode(exporter, b)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil && len(recs) == 0 {
		c.errors++
		return
	}
	c.roll()
	for _, r := range recs {
		k := aggKey{exporter: exporter, src: r.Src, dst: r.Dst, proto: r.Proto, port: servicePort(r)}
		a := c.sums[k]
		if a == nil {
			if len(c.sums) >= 500000 {
				// a flood: what does not fit is summed without addresses
				k.src, k.dst, k.port = netip.Addr{}, netip.Addr{}, 0
				if a = c.sums[k]; a == nil {
					a = &agg{}
					c.sums[k] = a
				}
			} else {
				a = &agg{}
				c.sums[k] = a
			}
		}
		a.bytes += r.Bytes
		a.packets += r.Packets
		c.Records++
	}
}

// servicePort is the port of the service side of a conversation: the
// lower of the two (clients use high ports).
func servicePort(r Record) uint16 {
	if r.Proto != 6 && r.Proto != 17 {
		return 0
	}
	if r.SrcPort != 0 && (r.DstPort == 0 || r.SrcPort < r.DstPort) {
		return r.SrcPort
	}
	return r.DstPort
}

// roll notes the minute the sums belong to (mu held).
func (c *Collector) roll() {
	if c.minute.IsZero() {
		c.minute = c.now().Truncate(time.Minute)
	}
}

// Flush stores the minutes that are over (all of them when final).
func (c *Collector) Flush(ctx context.Context, final bool) {
	c.mu.Lock()
	m := c.now().Truncate(time.Minute)
	if len(c.sums) == 0 || (!final && !m.After(c.minute)) {
		c.mu.Unlock()
		return
	}
	at, sums := c.minute, c.sums
	c.sums, c.minute = map[aggKey]*agg{}, time.Time{}
	addrs := c.addrs
	c.mu.Unlock()
	rows := Rows(at, sums, addrs)
	if err := c.st.AddFlows(ctx, rows); err != nil {
		c.log.Warn("flows: cannot store", "err", err)
	}
}

// Rows turns a minute's sums into rows: the top ones of each exporter and
// one row with the rest.
func Rows(at time.Time, sums map[aggKey]*agg, addrs map[netip.Addr]int64) []store.FlowRow {
	byExp := map[netip.Addr][]store.FlowRow{}
	for k, a := range sums {
		byExp[k.exporter] = append(byExp[k.exporter], store.FlowRow{At: at, Exporter: k.exporter, DeviceID: addrs[k.exporter],
			Src: k.src, Dst: k.dst, Proto: int(k.proto), Port: int(k.port), Bytes: int64(min(a.bytes, 1<<62)), Packets: int64(min(a.packets, 1<<62))}) // #nosec G115 -- capped
	}
	var out []store.FlowRow
	for _, rs := range byExp {
		sort.Slice(rs, func(i, j int) bool { return rs[i].Bytes > rs[j].Bytes })
		if len(rs) > keepTop {
			rest := store.FlowRow{At: at, Exporter: rs[0].Exporter, DeviceID: rs[0].DeviceID}
			for _, r := range rs[keepTop:] {
				rest.Bytes += r.Bytes
				rest.Packets += r.Packets
			}
			rs = append(rs[:keepTop], rest)
		}
		out = append(out, rs...)
	}
	return out
}

// RefreshDevices reads which addresses belong to which devices.
func (c *Collector) RefreshDevices(ctx context.Context) {
	if m, err := c.st.DeviceAddresses(ctx); err == nil {
		c.mu.Lock()
		c.addrs = m
		c.mu.Unlock()
	}
}

// Run keeps the device addresses fresh, stores each minute and removes
// old flows, until ctx ends.
func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	refresh := func() { c.RefreshDevices(ctx) }
	refresh()
	var lastPrune, lastAddrs time.Time
	for {
		select {
		case <-ctx.Done():
			c.mu.Lock()
			for _, x := range c.conns {
				_ = x.Close()
			}
			c.mu.Unlock()
			c.Flush(context.WithoutCancel(ctx), true)
			return
		case <-t.C:
			c.Flush(ctx, false)
			now := c.now()
			if now.Sub(lastAddrs) >= time.Minute {
				refresh()
				lastAddrs = now
			}
			if now.Sub(lastPrune) >= time.Hour {
				c.mu.Lock()
				days := c.cfg.KeepDays
				c.mu.Unlock()
				if days > 0 {
					_, _ = c.st.PruneFlows(ctx, now.AddDate(0, 0, -days))
				}
				lastPrune = now
			}
		}
	}
}
