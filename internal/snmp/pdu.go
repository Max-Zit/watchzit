package snmp

import (
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"unicode/utf8"
)

// Kind is the type of a variable's value.
type Kind uint8

// Value kinds.
const (
	KindNull Kind = iota
	KindInteger
	KindOctetString
	KindOID
	KindIPAddress
	KindCounter32
	KindGauge32
	KindTimeTicks
	KindOpaque
	KindCounter64
	KindNoSuchObject
	KindNoSuchInstance
	KindEndOfMibView
)

// Variable is one OID with its value.
type Variable struct {
	OID   OID
	Kind  Kind
	Int   int64  // Integer
	Uint  uint64 // Counter32, Gauge32, TimeTicks, Counter64
	Bytes []byte // OctetString, Opaque, IPAddress
	Obj   OID    // OID values
}

// Exists is false for noSuchObject / noSuchInstance / endOfMibView.
func (v Variable) Exists() bool {
	return v.Kind != KindNoSuchObject && v.Kind != KindNoSuchInstance && v.Kind != KindEndOfMibView && v.Kind != KindNull
}

// IsCounter tells whether the value only grows (rates are computed).
func (v Variable) IsCounter() bool { return v.Kind == KindCounter32 || v.Kind == KindCounter64 }

// Float returns a numeric value; strings holding a number count too
// (some agents report temperatures or levels as text).
func (v Variable) Float() (float64, bool) {
	switch v.Kind {
	case KindInteger:
		return float64(v.Int), true
	case KindCounter32, KindGauge32, KindTimeTicks, KindCounter64:
		return float64(v.Uint), true
	case KindOctetString, KindOpaque:
		f, err := strconv.ParseFloat(string(v.Bytes), 64)
		return f, err == nil
	}
	return 0, false
}

// String formats the value for people.
func (v Variable) String() string {
	switch v.Kind {
	case KindInteger:
		return strconv.FormatInt(v.Int, 10)
	case KindCounter32, KindGauge32, KindCounter64:
		return strconv.FormatUint(v.Uint, 10)
	case KindTimeTicks:
		s := v.Uint / 100
		return fmt.Sprintf("%dd %02d:%02d:%02d", s/86400, s/3600%24, s/60%60, s%60)
	case KindOctetString, KindOpaque:
		if printable(v.Bytes) {
			return string(v.Bytes)
		}
		return hex.EncodeToString(v.Bytes)
	case KindIPAddress:
		if a, ok := netip.AddrFromSlice(v.Bytes); ok {
			return a.String()
		}
		return hex.EncodeToString(v.Bytes)
	case KindOID:
		return v.Obj.String()
	case KindNoSuchObject:
		return "noSuchObject"
	case KindNoSuchInstance:
		return "noSuchInstance"
	case KindEndOfMibView:
		return "endOfMibView"
	}
	return ""
}

func printable(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for i, c := range string(b) {
		if c == 0 && i == len(b)-1 {
			continue // C strings
		}
		if c < 0x20 && c != '\t' && c != '\r' && c != '\n' {
			return false
		}
	}
	return true
}

func decVariable(r *reader) (Variable, error) {
	vb, err := r.sub(tagSequence)
	if err != nil {
		return Variable{}, err
	}
	oc, _, err := vb.expect(tagOID)
	if err != nil {
		return Variable{}, err
	}
	v := Variable{}
	if v.OID, err = decOID(oc); err != nil {
		return v, err
	}
	tag, c, _, err := vb.next()
	if err != nil {
		return v, err
	}
	switch tag {
	case tagNull:
		v.Kind = KindNull
	case tagInteger:
		v.Kind = KindInteger
		v.Int, err = decInt(c)
	case tagOctetString:
		v.Kind, v.Bytes = KindOctetString, c
	case tagOpaque:
		v.Kind, v.Bytes = KindOpaque, c
	case tagOID:
		v.Kind = KindOID
		v.Obj, err = decOID(c)
	case tagIPAddress:
		v.Kind, v.Bytes = KindIPAddress, c
	case tagCounter32, tagGauge32, tagTimeTicks, tagCounter64:
		v.Kind = map[byte]Kind{tagCounter32: KindCounter32, tagGauge32: KindGauge32, tagTimeTicks: KindTimeTicks, tagCounter64: KindCounter64}[tag]
		v.Uint, err = decUint(c)
		if err == nil && tag != tagCounter64 {
			v.Uint &= 0xffffffff
		}
	case tagNoSuchObject:
		v.Kind = KindNoSuchObject
	case tagNoSuchInstance:
		v.Kind = KindNoSuchInstance
	case tagEndOfMibView:
		v.Kind = KindEndOfMibView
	default:
		return v, fmt.Errorf("snmp: unknown value type 0x%02x", tag)
	}
	return v, err
}

// pdu is a request or response.
type pdu struct {
	typ      byte
	reqID    int32
	errStat  int64 // or non-repeaters
	errIndex int64 // or max-repetitions
	vars     []Variable
}

func (p *pdu) encode() []byte {
	var vbs [][]byte
	for _, v := range p.vars {
		vbs = append(vbs, tlv(tagSequence, encOID(v.OID), []byte{tagNull, 0}))
	}
	return tlv(p.typ, encInt(tagInteger, int64(p.reqID)), encInt(tagInteger, p.errStat), encInt(tagInteger, p.errIndex), tlv(tagSequence, vbs...))
}

func decPDU(r *reader) (*pdu, error) {
	if !r.more() {
		return nil, errBER
	}
	typ := r.b[r.pos]
	if typ < 0xa0 || typ > 0xa8 {
		return nil, errBER
	}
	pr, err := r.sub(typ)
	if err != nil {
		return nil, err
	}
	p := &pdu{typ: typ}
	id, err := pr.int()
	if err != nil {
		return nil, err
	}
	p.reqID = int32(id) // #nosec G115 -- request ids are 32-bit
	if p.errStat, err = pr.int(); err != nil {
		return nil, err
	}
	if p.errIndex, err = pr.int(); err != nil {
		return nil, err
	}
	vl, err := pr.sub(tagSequence)
	if err != nil {
		return nil, err
	}
	for vl.more() {
		if len(p.vars) >= 10000 {
			return nil, errBER
		}
		v, err := decVariable(vl)
		if err != nil {
			return nil, err
		}
		p.vars = append(p.vars, v)
	}
	return p, nil
}

// Error statuses of a response PDU (RFC 3416).
var errStatusNames = map[int64]string{
	1: "tooBig", 2: "noSuchName", 3: "badValue", 4: "readOnly", 5: "genErr", 6: "noAccess",
	7: "wrongType", 8: "wrongLength", 9: "wrongEncoding", 10: "wrongValue", 11: "noCreation",
	12: "inconsistentValue", 13: "resourceUnavailable", 14: "commitFailed", 15: "undoFailed",
	16: "authorizationError", 17: "notWritable", 18: "inconsistentName",
}

// ErrStatus is an error returned by the agent in a response.
type ErrStatus struct {
	Status int64
	Index  int64
}

func (e *ErrStatus) Error() string {
	n := errStatusNames[e.Status]
	if n == "" {
		n = "error " + strconv.FormatInt(e.Status, 10)
	}
	return "snmp: agent answered " + n
}

// ---- community messages (v1, v2c)

func encCommunity(version int, community string, p *pdu) []byte {
	return tlv(tagSequence, encInt(tagInteger, int64(version)), encOctets([]byte(community)), p.encode())
}

func decCommunity(b []byte) (version int64, community []byte, p *pdu, err error) {
	r, err := newReader(b).sub(tagSequence)
	if err != nil {
		return 0, nil, nil, err
	}
	if version, err = r.int(); err != nil {
		return 0, nil, nil, err
	}
	if community, _, err = r.expect(tagOctetString); err != nil {
		return 0, nil, nil, err
	}
	p, err = decPDU(r)
	return version, community, p, err
}
