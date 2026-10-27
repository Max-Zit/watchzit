package snmp

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"sync"
	"time"
)

// Config says how to talk to one agent.
type Config struct {
	Version        int    // 1, 2 (v2c) or 3
	Community      string // v1 and v2c
	V3             V3
	Timeout        time.Duration // per attempt (default 2 s)
	Retries        int           // extra attempts (default 1)
	MaxRepetitions int           // GETBULK size of walks (default 20)
}

// ErrTimeout means the agent did not answer.
var ErrTimeout = errors.New("no answer (check address, port, community or user, and the device's SNMP access list)")

// Client talks to one agent over UDP. It is not safe for concurrent use.
type Client struct {
	cfg   Config
	addr  netip.AddrPort
	conn  *net.UDPConn
	reqID int32
	buf   []byte
}

// Engines learned from agents (v3); the engine ID, boots and time stay
// valid between polls, so discovery runs once per agent.
var engines = struct {
	sync.Mutex
	m map[netip.AddrPort]engine
}{m: map[netip.AddrPort]engine{}}

var epoch = time.Now()

func monoSeconds() int64 { return int64(time.Since(epoch) / time.Second) }

// Dial prepares a client (UDP has no connection; nothing is sent yet).
func Dial(addr netip.AddrPort, cfg Config) (*Client, error) {
	switch cfg.Version {
	case 1, 2:
		if cfg.Community == "" {
			return nil, errors.New("the community is empty")
		}
	case 3:
		if err := cfg.V3.Check(); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown SNMP version %d", cfg.Version)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.Retries < 0 {
		cfg.Retries = 0
	} else if cfg.Retries == 0 {
		cfg.Retries = 1
	}
	if cfg.MaxRepetitions <= 0 {
		cfg.MaxRepetitions = 20
	}
	conn, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(addr))
	if err != nil {
		return nil, err
	}
	return &Client{cfg: cfg, addr: addr, conn: conn, reqID: rand.Int32N(1 << 30), buf: make([]byte, 65536)}, nil // #nosec G404 -- request ids only match answers
}

// Close releases the socket.
func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) nextID() int32 {
	c.reqID++
	if c.reqID >= 1<<31-1 || c.reqID <= 0 {
		c.reqID = 1
	}
	return c.reqID
}

// exchange sends a message and waits for an answer accepted by match.
func (c *Client) exchange(ctx context.Context, msg []byte, match func([]byte) bool) ([]byte, error) {
	for attempt := 0; attempt <= c.cfg.Retries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := c.conn.Write(msg); err != nil {
			return nil, err
		}
		deadline := time.Now().Add(c.cfg.Timeout)
		if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
			deadline = d
		}
		_ = c.conn.SetReadDeadline(deadline)
		for {
			n, err := c.conn.Read(c.buf)
			if err != nil {
				if errors.Is(err, os.ErrDeadlineExceeded) {
					break
				}
				if isRefused(err) {
					return nil, errors.New("the device refused SNMP (port closed)")
				}
				return nil, err
			}
			// decoded values point into the message: never into the reused buffer
			b := append([]byte(nil), c.buf[:n]...)
			if match(b) {
				return b, nil
			}
		}
	}
	return nil, ErrTimeout
}

func isRefused(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && isPlatformRefused(err)
}

// request sends a PDU and returns the response PDU.
func (c *Client) request(ctx context.Context, p *pdu) (*pdu, error) {
	if c.cfg.Version == 3 {
		return c.requestV3(ctx, p)
	}
	p.reqID = c.nextID()
	msg := encCommunity(c.cfg.Version-1, c.cfg.Community, p)
	var resp *pdu
	_, err := c.exchange(ctx, msg, func(b []byte) bool {
		_, _, r, err := decCommunity(b)
		if err != nil || r.reqID != p.reqID || r.typ != pduResponse {
			return false
		}
		resp = r
		return true
	})
	if errors.Is(err, ErrTimeout) {
		return nil, errors.New("no answer (check address, port, community and the device's SNMP access list)")
	}
	return resp, err
}

func (c *Client) discover(ctx context.Context) (engine, error) {
	engines.Lock()
	e, ok := engines.m[c.addr]
	engines.Unlock()
	if ok {
		return e, nil
	}
	p := &pdu{typ: pduGet}
	m, err := c.sendV3(ctx, &engine{}, p, true)
	if err != nil {
		return e, err
	}
	if len(m.engineID) == 0 {
		return e, errors.New("snmp: the device did not report its engine ID")
	}
	e = engine{id: append([]byte(nil), m.engineID...), boots: m.boots, time: m.time, since: monoSeconds()}
	c.remember(e)
	return e, nil
}

func (c *Client) remember(e engine) {
	engines.Lock()
	if len(engines.m) > 100000 {
		clear(engines.m)
	}
	engines.m[c.addr] = e
	engines.Unlock()
}

// sendV3 sends one v3 request and returns the parsed (not yet opened) answer.
func (c *Client) sendV3(ctx context.Context, e *engine, p *pdu, discovery bool) (*v3msg, error) {
	p.reqID = c.nextID()
	msgID := c.nextID()
	etime := e.time
	if e.since != 0 || e.time != 0 {
		etime = e.time + monoSeconds() - e.since
	}
	msg, err := c.cfg.V3.encV3(msgID, e, e.boots, etime, p.encode(), discovery)
	if err != nil {
		return nil, err
	}
	var got *v3msg
	_, err = c.exchange(ctx, msg, func(b []byte) bool {
		m, err := decV3(b)
		if err != nil || m.msgID != int64(msgID) {
			return false
		}
		got = m
		return true
	})
	return got, err
}

func (c *Client) requestV3(ctx context.Context, p *pdu) (*pdu, error) {
	v := &c.cfg.V3
	for try := 0; try < 3; try++ {
		e, err := c.discover(ctx)
		if err != nil {
			return nil, err
		}
		q := &pdu{typ: p.typ, errStat: p.errStat, errIndex: p.errIndex, vars: p.vars}
		m, err := c.sendV3(ctx, &e, q, false)
		if errors.Is(err, ErrTimeout) && v.PrivProto != PrivNone {
			// agents drop requests they cannot decrypt without a report
			return nil, errors.New("no answer to the encrypted request (check the privacy password and protocol)")
		}
		if err != nil {
			return nil, err
		}
		authed := m.flags&0x01 != 0
		if authed {
			if v.AuthProto == AuthNone {
				return nil, errors.New("snmp: unexpected authenticated answer")
			}
			if err := v.verify(m, &e); err != nil {
				return nil, err
			}
		}
		if m.flags&0x02 != 0 {
			if err := v.open(m, &e); err != nil {
				return nil, err
			}
		}
		if m.pdu == nil {
			return nil, errBER
		}
		if m.pdu.typ == pduReport {
			reason := reportReason(m.pdu)
			switch reason {
			case usmStats["1.3.6.1.6.3.15.1.1.2.0"], usmStats["1.3.6.1.6.3.15.1.1.4.0"]:
				// the agent tells its current boots/time or a new engine ID
				if len(m.engineID) > 0 {
					c.remember(engine{id: append([]byte(nil), m.engineID...), boots: m.boots, time: m.time, since: monoSeconds()})
					continue
				}
			}
			return nil, errors.New(reason)
		}
		if m.pdu.typ != pduResponse || m.pdu.reqID != q.reqID {
			return nil, errBER
		}
		if v.AuthProto != AuthNone && !authed {
			return nil, errors.New("snmp: answer not authenticated")
		}
		if authed && (m.boots != e.boots || m.time > e.time+monoSeconds()-e.since+1) {
			c.remember(engine{id: e.id, boots: m.boots, time: m.time, since: monoSeconds()})
		}
		return m.pdu, nil
	}
	return nil, errors.New("snmp: the device keeps rejecting the time window")
}

// Forget drops what is known about an agent's engine (after a change of
// credentials or a reboot that went unnoticed).
func Forget(addr netip.AddrPort) {
	engines.Lock()
	delete(engines.m, addr)
	engines.Unlock()
}

func vars(oids []OID) []Variable {
	out := make([]Variable, len(oids))
	for i, o := range oids {
		out[i] = Variable{OID: o}
	}
	return out
}

// Get reads the given OIDs; missing ones come back with Exists() false.
func (c *Client) Get(ctx context.Context, oids ...OID) ([]Variable, error) {
	if len(oids) == 0 {
		return nil, nil
	}
	out := vars(oids)
	todo := make([]int, len(oids))
	for i := range todo {
		todo[i] = i
	}
	for len(todo) > 0 {
		req := make([]Variable, len(todo))
		for i, k := range todo {
			req[i] = Variable{OID: oids[k]}
		}
		r, err := c.request(ctx, &pdu{typ: pduGet, vars: req})
		if err != nil {
			return nil, err
		}
		// SNMPv1 fails the whole request for one missing OID: mark it
		// and ask again for the rest.
		if r.errStat == 2 && c.cfg.Version == 1 && r.errIndex >= 1 && int(r.errIndex) <= len(todo) {
			k := todo[r.errIndex-1]
			out[k].Kind = KindNoSuchObject
			todo = append(todo[:r.errIndex-1], todo[r.errIndex:]...)
			continue
		}
		if r.errStat != 0 {
			return nil, &ErrStatus{r.errStat, r.errIndex}
		}
		if len(r.vars) != len(todo) {
			return nil, errBER
		}
		for i, k := range todo {
			out[k] = r.vars[i]
		}
		break
	}
	return out, nil
}

// next returns the variables following cur (GETBULK, or GETNEXT on v1).
func (c *Client) next(ctx context.Context, cur OID, maxRep int) ([]Variable, error) {
	if c.cfg.Version == 1 {
		r, err := c.request(ctx, &pdu{typ: pduGetNext, vars: vars([]OID{cur})})
		if err != nil {
			return nil, err
		}
		if r.errStat == 2 {
			return []Variable{{OID: cur, Kind: KindEndOfMibView}}, nil
		}
		if r.errStat != 0 {
			return nil, &ErrStatus{r.errStat, r.errIndex}
		}
		return r.vars, nil
	}
	r, err := c.request(ctx, &pdu{typ: pduGetBulk, errStat: 0, errIndex: int64(maxRep), vars: vars([]OID{cur})})
	if err != nil {
		return nil, err
	}
	if r.errStat != 0 {
		return nil, &ErrStatus{r.errStat, r.errIndex}
	}
	return r.vars, nil
}

// Walk calls fn for every variable under root, in order.
func (c *Client) Walk(ctx context.Context, root OID, fn func(Variable) error) error {
	cur := root
	maxRep := c.cfg.MaxRepetitions
	total := 0
	for {
		vs, err := c.next(ctx, cur, maxRep)
		var es *ErrStatus
		if errors.As(err, &es) && es.Status == 1 && maxRep > 1 { // tooBig
			maxRep /= 2
			continue
		}
		if err != nil {
			return err
		}
		if len(vs) == 0 {
			return nil
		}
		for _, v := range vs {
			if v.Kind == KindEndOfMibView || !v.OID.HasPrefix(root) {
				return nil
			}
			if v.OID.Compare(cur) <= 0 {
				return errors.New("snmp: the device returned OIDs out of order")
			}
			if total++; total > 200000 {
				return errors.New("snmp: walk too long")
			}
			if err := fn(v); err != nil {
				return err
			}
			cur = v.OID
		}
	}
}

// WalkAll collects a walk.
func (c *Client) WalkAll(ctx context.Context, root OID) ([]Variable, error) {
	var out []Variable
	err := c.Walk(ctx, root, func(v Variable) error {
		out = append(out, v)
		return nil
	})
	return out, err
}
