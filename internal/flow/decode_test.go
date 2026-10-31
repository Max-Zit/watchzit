package flow

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"
)

type buf []byte

func (b buf) u8(v uint8) buf   { return append(b, v) }
func (b buf) u16(v uint16) buf { return binary.BigEndian.AppendUint16(b, v) }
func (b buf) u32(v uint32) buf { return binary.BigEndian.AppendUint32(b, v) }
func (b buf) ip(s string) buf  { return append(b, netip.MustParseAddr(s).AsSlice()...) }

var exp = netip.MustParseAddr("10.0.0.1")

func TestNetFlowV5(t *testing.T) {
	b := buf{}.u16(5).u16(2).u32(1000).u32(0).u32(0).u32(1).u8(0).u8(0).u16(0x4000 | 10) // 1 in 10 sampled
	rec := func(src, dst string, pk, by uint32, sp, dp uint16, proto uint8) buf {
		r := buf{}.ip(src).ip(dst).ip("0.0.0.0").u16(1).u16(2).u32(pk).u32(by).u32(0).u32(0).u16(sp).u16(dp).u8(0).u8(0).u8(proto).u8(0).u16(0).u16(0).u8(0).u8(0).u16(0)
		if len(r) != 48 {
			t.Fatalf("record %d bytes", len(r))
		}
		return r
	}
	b = append(b, rec("192.168.1.10", "8.8.8.8", 3, 300, 51000, 53, 17)...)
	b = append(b, rec("192.168.1.11", "1.1.1.1", 10, 15000, 443, 50111, 6)...)
	rs, err := NewDecoder().Decode(exp, b)
	if err != nil || len(rs) != 2 {
		t.Fatalf("%v %+v", err, rs)
	}
	if r := rs[0]; r.Src.String() != "192.168.1.10" || r.Dst.String() != "8.8.8.8" || r.Bytes != 3000 || r.Packets != 30 || r.DstPort != 53 || r.Proto != 17 || servicePort(r) != 53 {
		t.Fatalf("first: %+v", r)
	}
	if servicePort(rs[1]) != 443 {
		t.Fatalf("service port of %+v", rs[1])
	}
}

func TestNetFlowV9(t *testing.T) {
	d := NewDecoder()
	hdr := func(count uint16) buf { return buf{}.u16(9).u16(count).u32(0).u32(0).u32(0).u32(7) }
	// data before its template is skipped
	data := buf{}.u16(260).u16(4 + 18).ip("10.1.1.1").ip("10.2.2.2").u32(5000).u16(22).u16(60000).u8(6).u8(0)
	if rs, err := d.Decode(exp, append(hdr(1), data...)); err != nil || len(rs) != 0 {
		t.Fatalf("before template: %v %+v", err, rs)
	}
	tmpl := buf{}.u16(0).u16(4 + 4 + 6*4).u16(260).u16(6).u16(8).u16(4).u16(12).u16(4).u16(1).u16(4).u16(7).u16(2).u16(11).u16(2).u16(4).u16(1)
	// options template (scope system, option SAMPLING_INTERVAL) and its data
	opt := buf{}.u16(1).u16(4 + 6 + 8).u16(261).u16(4).u16(4).u16(1).u16(4).u16(34).u16(4)
	optData := buf{}.u16(261).u16(4 + 8).u32(0).u32(100)
	pkt := append(hdr(3), tmpl...)
	pkt = append(pkt, opt...)
	pkt = append(pkt, optData...)
	pkt = append(pkt, data...)
	rs, err := d.Decode(exp, pkt)
	if err != nil || len(rs) != 1 {
		t.Fatalf("%v %+v", err, rs)
	}
	if r := rs[0]; r.Src.String() != "10.1.1.1" || r.Bytes != 500000 || r.SrcPort != 22 || r.Proto != 6 {
		t.Fatalf("record: %+v", r)
	}
	// another exporter does not share the template
	if rs, _ := d.Decode(netip.MustParseAddr("10.0.0.2"), append(hdr(1), data...)); len(rs) != 0 {
		t.Fatal("template shared between exporters")
	}
}

func TestIPFIX(t *testing.T) {
	// template: sourceIPv6Address, destinationIPv6Address, octetDeltaCount (8),
	// packetDeltaCount (8), an enterprise field, a variable-length field
	tmpl := buf{}.u16(2).u16(4 + 4 + 6*4 + 4).u16(300).u16(6).u16(27).u16(16).u16(28).u16(16).u16(1).u16(8).u16(2).u16(8).u16(0x8000 | 100).u16(2).u32(14988).u16(82).u16(65535)
	data := buf{}.ip("2001:db8::1").ip("2001:db8::2").u32(0).u32(1234).u32(0).u32(5).u16(7).u8(3)
	data = append(data, "eth"...)
	set := append(buf{}.u16(300).u16(uint16(4+len(data))), data...)
	body := append(tmpl, set...)
	pkt := append(buf{}.u16(10).u16(uint16(16+len(body))).u32(0).u32(1).u32(42), body...)
	rs, err := NewDecoder().Decode(exp, pkt)
	if err != nil || len(rs) != 1 {
		t.Fatalf("%v %+v", err, rs)
	}
	if r := rs[0]; r.Src.String() != "2001:db8::1" || r.Dst.String() != "2001:db8::2" || r.Bytes != 1234 || r.Packets != 5 {
		t.Fatalf("record: %+v", r)
	}
}

func TestSFlow(t *testing.T) {
	// Ethernet with a VLAN tag, IPv4, TCP 50000 → 443
	frame := buf{}.u32(0).u16(0).u32(0).u16(0).u16(0x8100).u16(10).u16(0x0800)
	frame = append(frame, buf{}.u8(0x45).u8(0).u16(1500).u16(0).u16(0x4000).u8(64).u8(6).u16(0).ip("172.16.0.5").ip("93.184.216.34").u16(50000).u16(443).u32(0)...)
	for len(frame)%4 != 0 {
		frame = append(frame, 0)
	}
	raw := buf{}.u32(1).u32(1514).u32(4).u32(uint32(len(frame)))
	raw = append(raw, frame...)
	rec := append(buf{}.u32(1).u32(uint32(len(raw))), raw...)
	sample := buf{}.u32(1).u32(5).u32(512).u32(0).u32(0).u32(1).u32(2).u32(1)
	sample = append(sample, rec...)
	counters := buf{}.u32(2).u32(4).u32(0)
	dg := buf{}.u32(5).u32(1).ip("10.0.0.9").u32(0).u32(1).u32(0).u32(2)
	dg = append(dg, buf{}.u32(1).u32(uint32(len(sample)))...)
	dg = append(dg, sample...)
	dg = append(dg, counters...)
	rs, err := NewDecoder().Decode(exp, dg)
	if err != nil || len(rs) != 1 {
		t.Fatalf("%v %+v", err, rs)
	}
	if r := rs[0]; r.Src.String() != "172.16.0.5" || r.Dst.String() != "93.184.216.34" || r.DstPort != 443 || r.Proto != 6 || r.Bytes != 1514*512 || r.Packets != 512 {
		t.Fatalf("record: %+v", r)
	}
}

func TestRowsKeepTop(t *testing.T) {
	sums := map[aggKey]*agg{}
	for i := 0; i < keepTop+5; i++ {
		k := aggKey{exporter: exp, src: netip.AddrFrom4([4]byte{10, 0, byte(i >> 8), byte(i)}), dst: exp, proto: 6, port: 80}
		sums[k] = &agg{bytes: uint64(i + 1), packets: 1}
	}
	rows := Rows(time.Unix(0, 0), sums, map[netip.Addr]int64{exp: 7})
	if len(rows) != keepTop+1 || rows[0].Bytes != keepTop+5 || rows[0].DeviceID != 7 {
		t.Fatalf("%d rows, first %+v", len(rows), rows[0])
	}
	if last := rows[keepTop]; last.Src.IsValid() || last.Bytes != 1+2+3+4+5 || last.Packets != 5 {
		t.Fatalf("rest: %+v", last)
	}
}
