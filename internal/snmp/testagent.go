package snmp

import (
	"net"
	"net/netip"
	"slices"
	"sync"
)

// TestAgent is a small SNMP v1/v2c agent on 127.0.0.1 for tests of the
// packages that use this client. Values can change while it runs.
type TestAgent struct {
	conn      *net.UDPConn
	community string
	mu        sync.Mutex
	mib       []Variable
}

// NewTestAgent starts an agent answering to community; Close stops it.
func NewTestAgent(community string) (*TestAgent, error) {
	c, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if err != nil {
		return nil, err
	}
	a := &TestAgent{conn: c, community: community}
	go a.serve()
	return a, nil
}

// Addr is where the agent listens.
func (a *TestAgent) Addr() netip.AddrPort { return a.conn.LocalAddr().(*net.UDPAddr).AddrPort() }

// Close stops the agent.
func (a *TestAgent) Close() error { return a.conn.Close() }

// Set adds or replaces a value.
func (a *TestAgent) Set(v Variable) {
	a.mu.Lock()
	defer a.mu.Unlock()
	i, found := slices.BinarySearchFunc(a.mib, v.OID, func(x Variable, o OID) int { return x.OID.Compare(o) })
	if found {
		a.mib[i] = v
		return
	}
	a.mib = slices.Insert(a.mib, i, v)
}

// Delete removes a value.
func (a *TestAgent) Delete(o OID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i, found := slices.BinarySearchFunc(a.mib, o, func(x Variable, o OID) int { return x.OID.Compare(o) }); found {
		a.mib = slices.Delete(a.mib, i, i+1)
	}
}

func encValueOf(v Variable) []byte {
	switch v.Kind {
	case KindInteger:
		return encInt(tagInteger, v.Int)
	case KindOctetString:
		return encOctets(v.Bytes)
	case KindOID:
		return encOID(v.Obj)
	case KindIPAddress:
		return tlv(tagIPAddress, v.Bytes)
	case KindTimeTicks:
		return encUint(tagTimeTicks, v.Uint)
	case KindCounter32:
		return encUint(tagCounter32, v.Uint)
	case KindGauge32:
		return encUint(tagGauge32, v.Uint)
	case KindCounter64:
		return encUint(tagCounter64, v.Uint)
	case KindNoSuchObject:
		return []byte{tagNoSuchObject, 0}
	case KindNoSuchInstance:
		return []byte{tagNoSuchInstance, 0}
	case KindEndOfMibView:
		return []byte{tagEndOfMibView, 0}
	}
	return []byte{tagNull, 0}
}

func (a *TestAgent) answer(p *pdu) []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	after := func(o OID) Variable {
		for _, v := range a.mib {
			if v.OID.Compare(o) > 0 {
				return v
			}
		}
		return Variable{OID: o, Kind: KindEndOfMibView}
	}
	var out []Variable
	switch p.typ {
	case pduGet:
		for _, q := range p.vars {
			v := Variable{OID: q.OID, Kind: KindNoSuchObject}
			for _, m := range a.mib {
				if m.OID.Compare(q.OID) == 0 {
					v = m
				}
			}
			out = append(out, v)
		}
	case pduGetNext:
		for _, q := range p.vars {
			out = append(out, after(q.OID))
		}
	case pduGetBulk:
		cur := p.vars[0].OID
		for i := int64(0); i < p.errIndex && i < 100; i++ {
			v := after(cur)
			out = append(out, v)
			if v.Kind == KindEndOfMibView {
				break
			}
			cur = v.OID
		}
	}
	var vbs [][]byte
	for _, v := range out {
		vbs = append(vbs, tlv(tagSequence, encOID(v.OID), encValueOf(v)))
	}
	return tlv(pduResponse, encInt(tagInteger, int64(p.reqID)), encInt(tagInteger, 0), encInt(tagInteger, 0), tlv(tagSequence, vbs...))
}

func (a *TestAgent) serve() {
	buf := make([]byte, 65536)
	for {
		n, from, err := a.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		ver, comm, p, err := decCommunity(append([]byte(nil), buf[:n]...))
		if err != nil || string(comm) != a.community {
			continue
		}
		reply := tlv(tagSequence, encInt(tagInteger, ver), encOctets(comm), a.answer(p))
		_, _ = a.conn.WriteToUDPAddrPort(reply, from)
	}
}
