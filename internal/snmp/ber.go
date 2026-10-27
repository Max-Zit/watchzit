// Package snmp is WatchZit's own SNMP client: BER encoding, SNMP v1, v2c
// and v3 (USM with MD5/SHA/SHA-2 authentication and DES/AES privacy),
// GET, GETNEXT, GETBULK and walks.
package snmp

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// BER tags used by SNMP.
const (
	tagInteger     = 0x02
	tagOctetString = 0x04
	tagNull        = 0x05
	tagOID         = 0x06
	tagSequence    = 0x30

	tagIPAddress = 0x40
	tagCounter32 = 0x41
	tagGauge32   = 0x42
	tagTimeTicks = 0x43
	tagOpaque    = 0x44
	tagCounter64 = 0x46

	tagNoSuchObject   = 0x80
	tagNoSuchInstance = 0x81
	tagEndOfMibView   = 0x82

	pduGet      = 0xa0
	pduGetNext  = 0xa1
	pduResponse = 0xa2
	pduGetBulk  = 0xa5
	pduReport   = 0xa8
)

var errBER = errors.New("snmp: malformed message")

// ---- OIDs

// OID is an object identifier.
type OID []uint32

// ParseOID reads "1.3.6.1.2.1.1.3.0" (a leading dot is allowed).
func ParseOID(s string) (OID, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), ".")
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 128 {
		return nil, fmt.Errorf("invalid OID %q", s)
	}
	o := make(OID, len(parts))
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid OID %q", s)
		}
		o[i] = uint32(n)
	}
	if o[0] > 2 || (o[0] < 2 && o[1] >= 40) {
		return nil, fmt.Errorf("invalid OID %q", s)
	}
	return o, nil
}

// MustOID is ParseOID for constants.
func MustOID(s string) OID {
	o, err := ParseOID(s)
	if err != nil {
		panic(err)
	}
	return o
}

func (o OID) String() string {
	var b strings.Builder
	for i, n := range o {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.FormatUint(uint64(n), 10))
	}
	return b.String()
}

// Compare orders OIDs lexicographically (-1, 0, 1).
func (o OID) Compare(p OID) int {
	for i := 0; i < len(o) && i < len(p); i++ {
		if o[i] != p[i] {
			if o[i] < p[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(o) < len(p):
		return -1
	case len(o) > len(p):
		return 1
	}
	return 0
}

// HasPrefix tells whether o lies under root.
func (o OID) HasPrefix(root OID) bool {
	if len(o) < len(root) {
		return false
	}
	for i := range root {
		if o[i] != root[i] {
			return false
		}
	}
	return true
}

// Append returns o with more arcs (a new slice).
func (o OID) Append(arcs ...uint32) OID {
	out := make(OID, 0, len(o)+len(arcs))
	return append(append(out, o...), arcs...)
}

// ---- encoding

func appendLen(b []byte, n int) []byte {
	switch {
	case n < 0x80:
		return append(b, byte(n)) // #nosec G115 -- BER encodes the bytes of the number
	case n <= 0xff:
		return append(b, 0x81, byte(n))
	case n <= 0xffff:
		return append(b, 0x82, byte(n>>8), byte(n)) // #nosec G115 -- BER encodes the bytes of the number
	default:
		return append(b, 0x83, byte(n>>16), byte(n>>8), byte(n)) // #nosec G115 -- BER encodes the bytes of the number
	}
}

func tlv(tag byte, content ...[]byte) []byte {
	n := 0
	for _, c := range content {
		n += len(c)
	}
	b := make([]byte, 0, n+5)
	b = appendLen(append(b, tag), n)
	for _, c := range content {
		b = append(b, c...)
	}
	return b
}

func encInt(tag byte, v int64) []byte {
	var c []byte
	for i := 7; i > 0; i-- {
		// drop leading bytes that only repeat the sign
		hi, next := byte(v>>(8*i)), byte(v>>(8*(i-1))) // #nosec G115 -- BER encodes the bytes of the number
		if (hi == 0 && next&0x80 == 0) || (hi == 0xff && next&0x80 != 0) {
			continue
		}
		for j := i; j >= 0; j-- {
			c = append(c, byte(v>>(8*j))) // #nosec G115 -- BER encodes the bytes of the number
		}
		return tlv(tag, c)
	}
	return tlv(tag, []byte{byte(v)}) // #nosec G115 -- BER encodes the bytes of the number
}

func encUint(tag byte, v uint64) []byte {
	c := []byte{0}
	for i := 7; i >= 0; i-- {
		c = append(c, byte(v>>(8*i))) // #nosec G115 -- BER encodes the bytes of the number
	}
	for len(c) > 1 && c[0] == 0 && c[1]&0x80 == 0 {
		c = c[1:]
	}
	return tlv(tag, c)
}

func encOctets(b []byte) []byte { return tlv(tagOctetString, b) }

func encOID(o OID) []byte {
	if len(o) < 2 {
		return tlv(tagOID, []byte{0})
	}
	c := appendBase128(nil, o[0]*40+o[1])
	for _, n := range o[2:] {
		c = appendBase128(c, n)
	}
	return tlv(tagOID, c)
}

func appendBase128(b []byte, n uint32) []byte {
	var tmp [5]byte
	i := len(tmp) - 1
	tmp[i] = byte(n & 0x7f)
	for n >>= 7; n > 0; n >>= 7 {
		i--
		tmp[i] = byte(n&0x7f) | 0x80
	}
	return append(b, tmp[i:]...)
}

// ---- decoding

// reader walks BER elements of one buffer; offsets are kept so a USM
// digest can be checked in place.
type reader struct {
	b   []byte
	pos int
	end int
}

func newReader(b []byte) *reader { return &reader{b: b, end: len(b)} }

func (r *reader) more() bool { return r.pos < r.end }

// next reads one element: its tag, content and the content's offset.
func (r *reader) next() (tag byte, content []byte, off int, err error) {
	if r.end-r.pos < 2 {
		return 0, nil, 0, errBER
	}
	tag = r.b[r.pos]
	if tag&0x1f == 0x1f {
		return 0, nil, 0, errBER // multi-byte tags are not used by SNMP
	}
	p := r.pos + 1
	n := int(r.b[p])
	p++
	if n&0x80 != 0 {
		k := n & 0x7f
		if k == 0 || k > 3 || r.end-p < k {
			return 0, nil, 0, errBER // indefinite or huge lengths
		}
		n = 0
		for i := 0; i < k; i++ {
			n = n<<8 | int(r.b[p+i])
		}
		p += k
	}
	if n > r.end-p {
		return 0, nil, 0, errBER
	}
	r.pos = p + n
	return tag, r.b[p : p+n : p+n], p, nil
}

// expect reads one element with the given tag.
func (r *reader) expect(tag byte) ([]byte, int, error) {
	t, c, off, err := r.next()
	if err != nil {
		return nil, 0, err
	}
	if t != tag {
		return nil, 0, fmt.Errorf("snmp: unexpected element 0x%02x (want 0x%02x)", t, tag)
	}
	return c, off, nil
}

// sub returns a reader over a constructed element's content.
func (r *reader) sub(tag byte) (*reader, error) {
	_, off, err := r.expect(tag)
	if err != nil {
		return nil, err
	}
	return &reader{b: r.b, pos: off, end: r.pos}, nil
}

func (r *reader) int() (int64, error) {
	c, _, err := r.expect(tagInteger)
	if err != nil {
		return 0, err
	}
	return decInt(c)
}

func decInt(c []byte) (int64, error) {
	if len(c) == 0 || len(c) > 8 {
		return 0, errBER
	}
	v := int64(int8(c[0])) // #nosec G115 -- BER encodes the bytes of the number
	for _, x := range c[1:] {
		v = v<<8 | int64(x)
	}
	return v, nil
}

func decUint(c []byte) (uint64, error) {
	if len(c) == 0 || len(c) > 9 || (len(c) == 9 && c[0] != 0) {
		return 0, errBER
	}
	var v uint64
	for _, x := range c {
		v = v<<8 | uint64(x)
	}
	// Some agents send unsigned values with the high bit set and no
	// leading zero; they are read as unsigned anyway.
	return v, nil
}

func decOID(c []byte) (OID, error) {
	if len(c) == 0 || len(c) > 512 {
		return nil, errBER
	}
	var o OID
	var n uint64
	for i, x := range c {
		if n == 0 && x == 0x80 {
			return nil, errBER // non-minimal encoding
		}
		n = n<<7 | uint64(x&0x7f)
		if n > math.MaxUint32 {
			return nil, errBER
		}
		if x&0x80 != 0 {
			if i == len(c)-1 {
				return nil, errBER
			}
			continue
		}
		if o == nil {
			switch {
			case n < 40:
				o = OID{0, uint32(n)}
			case n < 80:
				o = OID{1, uint32(n - 40)}
			default:
				o = OID{2, uint32(n - 80)}
			}
		} else {
			o = append(o, uint32(n))
		}
		n = 0
	}
	return o, nil
}
