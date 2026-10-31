// Package flow receives traffic flows from routers and switches — NetFlow
// v5 and v9, IPFIX and sFlow v5 over UDP — and sums them per minute into
// the top talkers, destinations, conversations and services.
package flow

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"sync"
)

// Record is one flow (or, for sFlow, one sampled packet scaled by the
// sampling rate).
type Record struct {
	Src, Dst         netip.Addr
	Proto            uint8
	SrcPort, DstPort uint16
	Bytes, Packets   uint64
}

var errShort = errors.New("short packet")

// Decoder turns datagrams into records. NetFlow v9 and IPFIX send their
// record layouts (templates) now and then; the decoder keeps them per
// exporter and observation domain, and data that arrives before its
// template is skipped.
type Decoder struct {
	mu        sync.Mutex
	templates map[tmplKey]*template
	// sampling rates an exporter announced (v9 option data), by exporter
	sampling map[netip.Addr]uint32
}

type tmplKey struct {
	exporter netip.Addr
	domain   uint32
	id       uint16
	ipfix    bool
}

type field struct {
	typ uint16
	len uint16 // 65535: variable (IPFIX)
	pen bool   // enterprise field (skipped)
}

type template struct {
	fields  []field
	options bool
	scope   int // options templates: the number of scope fields
}

// NewDecoder makes a decoder.
func NewDecoder() *Decoder {
	return &Decoder{templates: map[tmplKey]*template{}, sampling: map[netip.Addr]uint32{}}
}

// Decode reads one datagram from an exporter.
func (d *Decoder) Decode(exporter netip.Addr, b []byte) ([]Record, error) {
	if len(b) < 4 {
		return nil, errShort
	}
	switch binary.BigEndian.Uint16(b) {
	case 5:
		return decodeV5(b)
	case 9:
		return d.decodeV9(exporter, b)
	case 10:
		return d.decodeIPFIX(exporter, b)
	case 0:
		if binary.BigEndian.Uint32(b) == 5 {
			return decodeSFlow(b)
		}
	}
	return nil, errors.New("not NetFlow v5/v9, IPFIX or sFlow v5")
}

// ---- NetFlow v5: fixed 48-byte records

func decodeV5(b []byte) ([]Record, error) {
	if len(b) < 24 {
		return nil, errShort
	}
	n := int(binary.BigEndian.Uint16(b[2:]))
	rate := uint64(binary.BigEndian.Uint16(b[22:]) & 0x3fff)
	if rate == 0 {
		rate = 1
	}
	if len(b) < 24+n*48 {
		return nil, errShort
	}
	out := make([]Record, 0, n)
	for i := 0; i < n; i++ {
		r := b[24+i*48:]
		out = append(out, Record{
			Src: addr4(r[0:4]), Dst: addr4(r[4:8]),
			Packets: uint64(binary.BigEndian.Uint32(r[16:])) * rate, Bytes: uint64(binary.BigEndian.Uint32(r[20:])) * rate,
			SrcPort: binary.BigEndian.Uint16(r[32:]), DstPort: binary.BigEndian.Uint16(r[34:]), Proto: r[38],
		})
	}
	return out, nil
}

func addr4(b []byte) netip.Addr { return netip.AddrFrom4([4]byte(b[:4])) }

// ---- NetFlow v9 and IPFIX: templates and data sets

// field types used (the same numbers in v9 and IPFIX)
const (
	fBytes       = 1
	fPackets     = 2
	fProto       = 4
	fSrcPort     = 7
	fSrc4        = 8
	fDstPort     = 11
	fDst4        = 12
	fOutBytes    = 23
	fOutPackets  = 24
	fSrc6        = 27
	fDst6        = 28
	fSamplingInt = 34  // v9 SAMPLING_INTERVAL
	fTotalBytes  = 85  // IPFIX octetTotalCount
	fTotalPkts   = 86  // IPFIX packetTotalCount
	fSampPktInt  = 305 // IPFIX samplingPacketInterval
	fSampInt     = 50  // IPFIX samplerRandomInterval / v9 FLOW_SAMPLER_RANDOM_INTERVAL
)

func (d *Decoder) decodeV9(exporter netip.Addr, b []byte) ([]Record, error) {
	if len(b) < 20 {
		return nil, errShort
	}
	domain := binary.BigEndian.Uint32(b[16:])
	return d.sets(exporter, domain, b[20:], false)
}

func (d *Decoder) decodeIPFIX(exporter netip.Addr, b []byte) ([]Record, error) {
	if len(b) < 16 {
		return nil, errShort
	}
	l := int(binary.BigEndian.Uint16(b[2:]))
	if l < 16 || l > len(b) {
		return nil, errShort
	}
	domain := binary.BigEndian.Uint32(b[12:])
	return d.sets(exporter, domain, b[16:l], true)
}

// sets walks the flow sets (v9) or sets (IPFIX) of a datagram.
func (d *Decoder) sets(exporter netip.Addr, domain uint32, b []byte, ipfix bool) ([]Record, error) {
	var out []Record
	tmplSet, optSet := uint16(0), uint16(1)
	if ipfix {
		tmplSet, optSet = 2, 3
	}
	for len(b) >= 4 {
		id, l := binary.BigEndian.Uint16(b), int(binary.BigEndian.Uint16(b[2:]))
		if l < 4 || l > len(b) {
			return out, errShort
		}
		body := b[4:l]
		b = b[l:]
		switch {
		case id == tmplSet:
			d.readTemplates(exporter, domain, body, ipfix, false)
		case id == optSet:
			d.readTemplates(exporter, domain, body, ipfix, true)
		case id >= 256:
			d.mu.Lock()
			t := d.templates[tmplKey{exporter, domain, id, ipfix}]
			d.mu.Unlock()
			if t == nil {
				continue // the template comes later
			}
			out = append(out, d.readData(exporter, t, body)...)
		}
	}
	return out, nil
}

func (d *Decoder) readTemplates(exporter netip.Addr, domain uint32, b []byte, ipfix, options bool) {
	for len(b) >= 4 {
		id := binary.BigEndian.Uint16(b)
		t := &template{options: options}
		var count int
		if options && !ipfix {
			// v9: template id, scope length (bytes), option length (bytes)
			if len(b) < 6 {
				return
			}
			sl, ol := int(binary.BigEndian.Uint16(b[2:])), int(binary.BigEndian.Uint16(b[4:]))
			t.scope = sl / 4
			count = (sl + ol) / 4
			b = b[6:]
		} else {
			count = int(binary.BigEndian.Uint16(b[2:]))
			b = b[4:]
			if options {
				if len(b) < 2 {
					return
				}
				t.scope = int(binary.BigEndian.Uint16(b))
				b = b[2:]
			}
		}
		if id < 256 || count == 0 {
			return // padding or a withdrawal
		}
		for i := 0; i < count; i++ {
			if len(b) < 4 {
				return
			}
			f := field{typ: binary.BigEndian.Uint16(b), len: binary.BigEndian.Uint16(b[2:])}
			b = b[4:]
			if ipfix && f.typ&0x8000 != 0 {
				f.typ &= 0x7fff
				f.pen = true
				if len(b) < 4 {
					return
				}
				b = b[4:]
			}
			t.fields = append(t.fields, f)
		}
		d.mu.Lock()
		if len(d.templates) < 10000 {
			d.templates[tmplKey{exporter, domain, id, ipfix}] = t
		}
		d.mu.Unlock()
	}
}

func uintOf(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func (d *Decoder) readData(exporter netip.Addr, t *template, b []byte) []Record {
	var out []Record
	min := 0
	for _, f := range t.fields {
		if f.len != 65535 {
			min += int(f.len)
		}
	}
	if min == 0 {
		return nil
	}
	for len(b) >= min {
		var r Record
		var rate, outBytes, outPkts uint64
		for _, f := range t.fields {
			l := int(f.len)
			if f.len == 65535 { // IPFIX variable length
				if len(b) < 1 {
					return out
				}
				l, b = int(b[0]), b[1:]
				if l == 255 {
					if len(b) < 2 {
						return out
					}
					l, b = int(binary.BigEndian.Uint16(b)), b[2:]
				}
			}
			if len(b) < l {
				return out
			}
			v := b[:l]
			b = b[l:]
			if f.pen {
				continue
			}
			switch f.typ {
			case fBytes, fTotalBytes:
				r.Bytes += uintOf(v)
			case fPackets, fTotalPkts:
				r.Packets += uintOf(v)
			case fOutBytes:
				outBytes = uintOf(v)
			case fOutPackets:
				outPkts = uintOf(v)
			case fProto:
				r.Proto = uint8(uintOf(v)) // #nosec G115 -- one byte
			case fSrcPort:
				r.SrcPort = uint16(uintOf(v)) // #nosec G115 -- two bytes
			case fDstPort:
				r.DstPort = uint16(uintOf(v)) // #nosec G115 -- two bytes
			case fSrc4:
				if l == 4 {
					r.Src = addr4(v)
				}
			case fDst4:
				if l == 4 {
					r.Dst = addr4(v)
				}
			case fSrc6:
				if l == 16 {
					r.Src = netip.AddrFrom16([16]byte(v)).Unmap()
				}
			case fDst6:
				if l == 16 {
					r.Dst = netip.AddrFrom16([16]byte(v)).Unmap()
				}
			case fSamplingInt, fSampPktInt, fSampInt:
				rate = uintOf(v)
			}
		}
		if t.options {
			// an options record announcing the sampling rate
			if rate > 0 && rate < 1<<31 {
				d.mu.Lock()
				d.sampling[exporter] = uint32(rate) // #nosec G115 -- checked
				d.mu.Unlock()
			}
			continue
		}
		if r.Bytes == 0 && outBytes > 0 {
			r.Bytes, r.Packets = outBytes, outPkts
		}
		if !r.Src.IsValid() || !r.Dst.IsValid() {
			continue
		}
		if rate == 0 {
			d.mu.Lock()
			rate = uint64(d.sampling[exporter])
			d.mu.Unlock()
		}
		if rate > 1 {
			r.Bytes *= rate
			r.Packets *= rate
		}
		out = append(out, r)
	}
	return out
}

// ---- sFlow v5: sampled packet headers, scaled by the sampling rate

func decodeSFlow(b []byte) ([]Record, error) {
	if len(b) < 8 {
		return nil, errShort
	}
	off := 8
	switch binary.BigEndian.Uint32(b[4:]) { // agent address type
	case 1:
		off += 4
	case 2:
		off += 16
	default:
		return nil, errors.New("sFlow: unknown agent address type")
	}
	off += 12 // sub agent, sequence, uptime
	if len(b) < off+4 {
		return nil, errShort
	}
	n := int(binary.BigEndian.Uint32(b[off:]))
	off += 4
	var out []Record
	for i := 0; i < n && len(b) >= off+8; i++ {
		format := binary.BigEndian.Uint32(b[off:]) & 0xfff
		enterprise := binary.BigEndian.Uint32(b[off:]) >> 12
		l := int(binary.BigEndian.Uint32(b[off+4:]))
		off += 8
		if l < 0 || len(b) < off+l {
			return out, errShort
		}
		s := b[off : off+l]
		off += l
		if enterprise != 0 {
			continue
		}
		var hdr int // offset of the record count
		switch format {
		case 1: // flow sample
			hdr = 28
		case 3: // expanded flow sample
			hdr = 40
		default:
			continue // counters
		}
		if len(s) < hdr+4 {
			continue
		}
		rate := uint64(binary.BigEndian.Uint32(s[8:]))
		if format == 3 {
			rate = uint64(binary.BigEndian.Uint32(s[12:]))
		}
		if rate == 0 {
			rate = 1
		}
		recs := int(binary.BigEndian.Uint32(s[hdr:]))
		p := hdr + 4
		for j := 0; j < recs && len(s) >= p+8; j++ {
			rf := binary.BigEndian.Uint32(s[p:])
			rl := int(binary.BigEndian.Uint32(s[p+4:]))
			p += 8
			if rl < 0 || len(s) < p+rl {
				break
			}
			rec := s[p : p+rl]
			p += rl
			if rf != 1 || len(rec) < 16 { // raw packet header
				continue
			}
			proto := binary.BigEndian.Uint32(rec)
			frame := uint64(binary.BigEndian.Uint32(rec[4:]))
			hl := int(binary.BigEndian.Uint32(rec[12:]))
			if len(rec) < 16+hl {
				continue
			}
			var r Record
			var ok bool
			switch proto {
			case 1: // Ethernet
				r, ok = parseEthernet(rec[16 : 16+hl])
			case 11: // IPv4
				r, ok = parseIP(rec[16:16+hl], 4)
			case 12: // IPv6
				r, ok = parseIP(rec[16:16+hl], 6)
			}
			if !ok {
				continue
			}
			r.Bytes, r.Packets = frame*rate, rate
			out = append(out, r)
		}
	}
	return out, nil
}

func parseEthernet(b []byte) (Record, bool) {
	if len(b) < 14 {
		return Record{}, false
	}
	et, p := binary.BigEndian.Uint16(b[12:]), 14
	for (et == 0x8100 || et == 0x88a8) && len(b) >= p+4 {
		et, p = binary.BigEndian.Uint16(b[p+2:]), p+4
	}
	switch et {
	case 0x0800:
		return parseIP(b[p:], 4)
	case 0x86dd:
		return parseIP(b[p:], 6)
	}
	return Record{}, false
}

func parseIP(b []byte, v int) (Record, bool) {
	var r Record
	var l4 []byte
	if v == 4 {
		if len(b) < 20 || b[0]>>4 != 4 {
			return r, false
		}
		ihl := int(b[0]&0x0f) * 4
		r.Proto, r.Src, r.Dst = b[9], addr4(b[12:16]), addr4(b[16:20])
		if len(b) >= ihl && binary.BigEndian.Uint16(b[6:])&0x1fff == 0 { // not a later fragment
			l4 = b[ihl:]
		}
	} else {
		if len(b) < 40 || b[0]>>4 != 6 {
			return r, false
		}
		r.Proto, r.Src, r.Dst = b[6], netip.AddrFrom16([16]byte(b[8:24])), netip.AddrFrom16([16]byte(b[24:40]))
		l4 = b[40:]
	}
	if (r.Proto == 6 || r.Proto == 17) && len(l4) >= 4 {
		r.SrcPort, r.DstPort = binary.BigEndian.Uint16(l4), binary.BigEndian.Uint16(l4[2:])
	}
	return r, true
}
