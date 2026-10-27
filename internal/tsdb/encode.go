// Package tsdb stores metric samples in PostgreSQL in a compact form:
// samples of one series and one period are packed into a block with
// delta-of-delta timestamps and XOR-compressed floats (the scheme
// described in Facebook's "Gorilla" paper, implemented here from the
// description), so 60 one-minute samples take about 100 bytes instead of
// 60 table rows.
package tsdb

import (
	"encoding/binary"
	"errors"
	"math"
	"math/bits"
)

// blockVersion is the first byte of every block.
const blockVersion = 1

// maxPoints bounds a block (a month of 5-minute points is 8928).
const maxPoints = 1 << 20

// errCorrupt is returned for a block that cannot be decoded.
var errCorrupt = errors.New("tsdb: corrupt block")

// ---- bit stream

type bitWriter struct {
	buf  []byte
	used uint8 // bits used in the last byte (0 = start a new byte)
}

func (w *bitWriter) bit(b bool) {
	if w.used == 0 {
		w.buf = append(w.buf, 0)
	}
	if b {
		w.buf[len(w.buf)-1] |= 1 << (7 - w.used)
	}
	w.used = (w.used + 1) % 8
}

// bitsOf writes the n low bits of v, most significant first.
func (w *bitWriter) bitsOf(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v>>uint(i)&1 == 1) // #nosec G115 -- 0 <= i < 64
	}
}

type bitReader struct {
	buf []byte
	pos int // bit position
}

func (r *bitReader) bit() (bool, error) {
	if r.pos >= len(r.buf)*8 {
		return false, errCorrupt
	}
	b := r.buf[r.pos/8]>>(7-uint(r.pos%8))&1 == 1 // #nosec G115 -- 0..7
	r.pos++
	return b, nil
}

func (r *bitReader) bitsOf(n int) (uint64, error) {
	var v uint64
	for i := 0; i < n; i++ {
		b, err := r.bit()
		if err != nil {
			return 0, err
		}
		v <<= 1
		if b {
			v |= 1
		}
	}
	return v, nil
}

// ---- timestamps: delta of delta in buckets

func writeDoD(w *bitWriter, d int64) {
	switch {
	case d == 0:
		w.bit(false)
	case d >= -63 && d <= 64:
		w.bitsOf(0b10, 2)
		w.bitsOf(uint64(d+63), 7) // #nosec G115 -- 0..127
	case d >= -255 && d <= 256:
		w.bitsOf(0b110, 3)
		w.bitsOf(uint64(d+255), 9) // #nosec G115 -- 0..511
	case d >= -2047 && d <= 2048:
		w.bitsOf(0b1110, 4)
		w.bitsOf(uint64(d+2047), 12) // #nosec G115 -- 0..4095
	default:
		w.bitsOf(0b1111, 4)
		w.bitsOf(uint64(d), 64) // #nosec G115 -- two's complement round trip
	}
}

func readDoD(r *bitReader) (int64, error) {
	n := 0
	for n < 4 {
		b, err := r.bit()
		if err != nil {
			return 0, err
		}
		if !b {
			break
		}
		n++
	}
	var width int
	var off int64
	switch n {
	case 0:
		return 0, nil
	case 1:
		width, off = 7, 63
	case 2:
		width, off = 9, 255
	case 3:
		width, off = 12, 2047
	default:
		v, err := r.bitsOf(64)
		return int64(v), err // #nosec G115 -- two's complement round trip
	}
	v, err := r.bitsOf(width)
	return int64(v) - off, err // #nosec G115 -- at most 12 bits
}

// ---- values: XOR with the previous value of the same column

type xorState struct {
	prev     uint64
	lead     int
	trail    int
	hasWidth bool
}

func (x *xorState) write(w *bitWriter, v float64) {
	cur := math.Float64bits(v)
	d := cur ^ x.prev
	x.prev = cur
	if d == 0 {
		w.bit(false)
		return
	}
	w.bit(true)
	lead := min(bits.LeadingZeros64(d), 31)
	trail := bits.TrailingZeros64(d)
	if x.hasWidth && lead >= x.lead && trail >= x.trail {
		w.bit(false)
		w.bitsOf(d>>uint(x.trail), 64-x.lead-x.trail) // #nosec G115 -- 0..63
		return
	}
	w.bit(true)
	sig := 64 - lead - trail
	w.bitsOf(uint64(lead), 5)     // #nosec G115 -- 0..31
	w.bitsOf(uint64(sig-1), 6)    // #nosec G115 -- 0..63
	w.bitsOf(d>>uint(trail), sig) // #nosec G115 -- 0..63
	x.lead, x.trail, x.hasWidth = lead, trail, true
}

func (x *xorState) read(r *bitReader) (float64, error) {
	b, err := r.bit()
	if err != nil {
		return 0, err
	}
	if b {
		newWidth, err := r.bit()
		if err != nil {
			return 0, err
		}
		if newWidth {
			lead, err := r.bitsOf(5)
			if err != nil {
				return 0, err
			}
			sig, err := r.bitsOf(6)
			if err != nil {
				return 0, err
			}
			x.lead, x.trail, x.hasWidth = int(lead), 64-int(lead)-int(sig+1), true // #nosec G115 -- small
			if x.trail < 0 {
				return 0, errCorrupt
			}
		} else if !x.hasWidth {
			return 0, errCorrupt
		}
		d, err := r.bitsOf(64 - x.lead - x.trail)
		if err != nil {
			return 0, err
		}
		x.prev ^= d << uint(x.trail) // #nosec G115 -- 0..63
	}
	return math.Float64frombits(x.prev), nil
}

// Column modes: values are XOR-compressed floats, or, when every value of
// the column is a decimal number with at most four places (a ping time
// rounded to 0.01 ms, a counter, a percentage), integers scaled by 10^s
// whose differences are stored like the timestamps — far smaller.
const (
	modeXOR    = 0
	modeScaled = 1 // + s (0..4)
)

var pow10 = [...]float64{1, 10, 100, 1000, 10000}

// columnMode picks the mode of a column.
func columnMode(v []float64) int {
	for s, p := range pow10 {
		ok := true
		for _, x := range v {
			r := math.Round(x * p)
			if math.Abs(r) >= 1<<53 || r/p != x || math.Signbit(x) && x == 0 {
				ok = false
				break
			}
		}
		if ok {
			return modeScaled + s
		}
	}
	return modeXOR
}

// Encode packs points: ts are Unix seconds in ascending order, cols holds
// one or more value columns of the same length as ts.
func Encode(ts []int64, cols ...[]float64) []byte {
	for _, c := range cols {
		if len(c) != len(ts) {
			panic("tsdb: column length differs from the timestamps")
		}
	}
	hdr := []byte{blockVersion, byte(len(cols))} // #nosec G115 -- few columns
	modes := make([]int, len(cols))
	for c := range cols {
		modes[c] = columnMode(cols[c])
		hdr = append(hdr, byte(modes[c])) // #nosec G115 -- 0..5
	}
	hdr = binary.AppendUvarint(hdr, uint64(len(ts)))
	if len(ts) == 0 {
		return hdr
	}
	hdr = binary.AppendVarint(hdr, ts[0])
	w := &bitWriter{buf: hdr}
	xs := make([]xorState, len(cols))
	prevI := make([]int64, len(cols))
	var prevT, prevD int64
	for i, t := range ts {
		if i > 0 {
			d := t - prevT
			writeDoD(w, d-prevD)
			prevD = d
		}
		prevT = t
		for c := range cols {
			v := cols[c][i]
			if modes[c] != modeXOR {
				iv := int64(math.Round(v * pow10[modes[c]-modeScaled]))
				if i == 0 {
					w.bitsOf(uint64(iv), 64) // #nosec G115 -- two's complement round trip
				} else {
					writeDoD(w, iv-prevI[c])
				}
				prevI[c] = iv
				continue
			}
			if i == 0 {
				xs[c].prev = math.Float64bits(v)
				w.bitsOf(xs[c].prev, 64)
				continue
			}
			xs[c].write(w, v)
		}
	}
	return w.buf
}

// Decode unpacks a block made by Encode.
func Decode(b []byte) (ts []int64, cols [][]float64, err error) {
	if len(b) < 3 || b[0] != blockVersion {
		return nil, nil, errCorrupt
	}
	k := int(b[1])
	if k == 0 || k > 16 || len(b) < 2+k+1 {
		return nil, nil, errCorrupt
	}
	modes := make([]int, k)
	for c := range modes {
		modes[c] = int(b[2+c])
		if modes[c] > modeScaled+len(pow10)-1 {
			return nil, nil, errCorrupt
		}
	}
	p := 2 + k
	n, used := binary.Uvarint(b[p:])
	if used <= 0 || n > maxPoints {
		return nil, nil, errCorrupt
	}
	p += used
	cols = make([][]float64, k)
	if n == 0 {
		return nil, cols, nil
	}
	t0, used := binary.Varint(b[p:])
	if used <= 0 {
		return nil, nil, errCorrupt
	}
	p += used
	r := &bitReader{buf: b[p:]}
	// Every point takes at least one bit per timestamp and column.
	if n > uint64(len(r.buf))*8+1 {
		return nil, nil, errCorrupt
	}
	ts = make([]int64, n)
	for c := range cols {
		cols[c] = make([]float64, n)
	}
	xs := make([]xorState, k)
	prevI := make([]int64, k)
	var prevD int64
	for i := range ts {
		if i == 0 {
			ts[0] = t0
		} else {
			dod, err := readDoD(r)
			if err != nil {
				return nil, nil, err
			}
			prevD += dod
			ts[i] = ts[i-1] + prevD
		}
		for c := range cols {
			if modes[c] != modeXOR {
				if i == 0 {
					v, err := r.bitsOf(64)
					if err != nil {
						return nil, nil, err
					}
					prevI[c] = int64(v) // #nosec G115 -- two's complement round trip
				} else {
					d, err := readDoD(r)
					if err != nil {
						return nil, nil, err
					}
					prevI[c] += d
				}
				cols[c][i] = float64(prevI[c]) / pow10[modes[c]-modeScaled]
				continue
			}
			if i == 0 {
				v, err := r.bitsOf(64)
				if err != nil {
					return nil, nil, err
				}
				xs[c].prev = v
				cols[c][0] = math.Float64frombits(v)
				continue
			}
			v, err := xs[c].read(r)
			if err != nil {
				return nil, nil, err
			}
			cols[c][i] = v
		}
	}
	return ts, cols, nil
}
