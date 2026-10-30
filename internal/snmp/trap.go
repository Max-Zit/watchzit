package snmp

import (
	"errors"
	"net/netip"
	"strings"
)

// Traps and informs that devices send to WatchZit (UDP 162): SNMPv1 traps,
// SNMPv2c traps and informs (an inform is answered).

const (
	pduTrapV1  = 0xa4
	pduInform  = 0xa6
	pduTrapV2  = 0xa7
	tagIPAddrV = tagIPAddress
)

// Trap is a received trap or inform.
type Trap struct {
	Version   int // 1 or 2 (v2c)
	Community string
	TrapOID   OID        // snmpTrapOID.0 (v1: built from enterprise and trap numbers)
	Agent     netip.Addr // v1 agent address (the sender's address otherwise)
	Uptime    int64      // in hundredths of a second
	Vars      []Variable // without sysUpTime.0 and snmpTrapOID.0
	Inform    bool
	reqID     int32
	vars      []Variable // all of them, for the answer to an inform
}

var (
	oidSysUpTime   = MustOID("1.3.6.1.2.1.1.3.0")
	oidSnmpTrapOID = MustOID("1.3.6.1.6.3.1.1.4.1.0")
	oidSnmpTraps   = MustOID("1.3.6.1.6.3.1.1.5")
)

// TrapNames are the standard traps (RFC 3418).
var TrapNames = map[string]string{
	"1.3.6.1.6.3.1.1.5.1": "coldStart", "1.3.6.1.6.3.1.1.5.2": "warmStart", "1.3.6.1.6.3.1.1.5.3": "linkDown",
	"1.3.6.1.6.3.1.1.5.4": "linkUp", "1.3.6.1.6.3.1.1.5.5": "authenticationFailure", "1.3.6.1.6.3.1.1.5.6": "egpNeighborLoss",
}

var errNotTrap = errors.New("snmp: not a trap")

// ParseTrap reads a v1 or v2c trap or inform.
func ParseTrap(b []byte) (*Trap, error) {
	r, err := newReader(b).sub(tagSequence)
	if err != nil {
		return nil, err
	}
	ver, err := r.int()
	if err != nil {
		return nil, err
	}
	comm, _, err := r.expect(tagOctetString)
	if err != nil {
		return nil, err
	}
	if !r.more() {
		return nil, errBER
	}
	t := &Trap{Community: string(comm)}
	switch typ := r.b[r.pos]; {
	case ver == 0 && typ == pduTrapV1:
		t.Version = 1
		pr, err := r.sub(pduTrapV1)
		if err != nil {
			return nil, err
		}
		ec, _, err := pr.expect(tagOID)
		if err != nil {
			return nil, err
		}
		ent, err := decOID(ec)
		if err != nil {
			return nil, err
		}
		ac, _, err := pr.expect(tagIPAddrV)
		if err != nil {
			return nil, err
		}
		if a, ok := netip.AddrFromSlice(ac); ok {
			t.Agent = a.Unmap()
		}
		generic, err := pr.int()
		if err != nil {
			return nil, err
		}
		specific, err := pr.int()
		if err != nil {
			return nil, err
		}
		tc, _, err := pr.expect(tagTimeTicks)
		if err != nil {
			return nil, err
		}
		up, _ := decUint(tc)
		t.Uptime = int64(up) // #nosec G115 -- 32-bit time ticks
		// RFC 3584: a generic trap is snmpTraps.(generic+1), an enterprise
		// one is enterprise.0.specific
		if generic >= 0 && generic < 6 {
			t.TrapOID = oidSnmpTraps.Append(uint32(generic + 1)) // #nosec G115 -- 0–5
		} else if specific >= 0 && specific <= 1<<31 {
			t.TrapOID = ent.Append(0, uint32(specific)) // #nosec G115 -- checked
		}
		vl, err := pr.sub(tagSequence)
		if err != nil {
			return nil, err
		}
		for vl.more() && len(t.Vars) < 500 {
			v, err := decVariable(vl)
			if err != nil {
				return nil, err
			}
			t.Vars = append(t.Vars, v)
		}
		return t, nil
	case ver == 1 && (typ == pduTrapV2 || typ == pduInform):
		t.Version, t.Inform = 2, typ == pduInform
		p, err := decPDU(r)
		if err != nil {
			return nil, err
		}
		t.reqID, t.vars = p.reqID, p.vars
		for _, v := range p.vars {
			switch {
			case v.OID.Compare(oidSysUpTime) == 0:
				t.Uptime = int64(v.Uint) // #nosec G115 -- 32-bit time ticks
			case v.OID.Compare(oidSnmpTrapOID) == 0:
				t.TrapOID = v.Obj
			default:
				if len(t.Vars) < 500 {
					t.Vars = append(t.Vars, v)
				}
			}
		}
		return t, nil
	}
	return nil, errNotTrap
}

// InformAnswer is the response an inform asks for (nil for traps).
func (t *Trap) InformAnswer() []byte {
	if !t.Inform {
		return nil
	}
	p := &pdu{typ: pduResponse, reqID: t.reqID}
	var vbs [][]byte
	for _, v := range t.vars {
		vbs = append(vbs, tlv(tagSequence, encOID(v.OID), []byte{tagNull, 0}))
	}
	return tlv(tagSequence, encInt(tagInteger, 1), encOctets([]byte(t.Community)),
		tlv(p.typ, encInt(tagInteger, int64(p.reqID)), encInt(tagInteger, 0), encInt(tagInteger, 0), tlv(tagSequence, vbs...)))
}

// Text is the trap as one line: its name or OID and its values.
func (t *Trap) Text() string {
	var b strings.Builder
	name := t.TrapOID.String()
	if n := TrapNames[name]; n != "" {
		name = n
	}
	b.WriteString(name)
	for _, v := range t.Vars {
		b.WriteString(" " + v.OID.String() + "=" + v.String())
		if b.Len() > 2000 {
			break
		}
	}
	s := b.String()
	if len(s) > 2000 {
		s = s[:2000]
	}
	return s
}

// EncodeTrapV2 builds a v2c trap (tests and tools).
func EncodeTrapV2(community string, trapOID OID, uptime uint32, vars ...Variable) []byte {
	vbs := [][]byte{
		tlv(tagSequence, encOID(oidSysUpTime), encUint(tagTimeTicks, uint64(uptime))),
		tlv(tagSequence, encOID(oidSnmpTrapOID), encOID(trapOID)),
	}
	for _, v := range vars {
		val := []byte{tagNull, 0}
		switch v.Kind {
		case KindInteger:
			val = encInt(tagInteger, v.Int)
		case KindOctetString:
			val = encOctets(v.Bytes)
		}
		vbs = append(vbs, tlv(tagSequence, encOID(v.OID), val))
	}
	return tlv(tagSequence, encInt(tagInteger, 1), encOctets([]byte(community)),
		tlv(pduTrapV2, encInt(tagInteger, 1), encInt(tagInteger, 0), encInt(tagInteger, 0), tlv(tagSequence, vbs...)))
}

// EncodeTrapV1 builds a v1 trap (tests and tools).
func EncodeTrapV1(community string, enterprise OID, agent netip.Addr, generic, specific int64, uptime uint32) []byte {
	a := agent.As4()
	return tlv(tagSequence, encInt(tagInteger, 0), encOctets([]byte(community)),
		tlv(pduTrapV1, encOID(enterprise), tlv(tagIPAddrV, a[:]), encInt(tagInteger, generic), encInt(tagInteger, specific),
			encUint(tagTimeTicks, uint64(uptime)), tlv(tagSequence)))
}
