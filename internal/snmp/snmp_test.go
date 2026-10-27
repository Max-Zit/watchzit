package snmp

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOID(t *testing.T) {
	o := MustOID(".1.3.6.1.2.1.1.3.0")
	if o.String() != "1.3.6.1.2.1.1.3.0" {
		t.Fatal(o)
	}
	for _, s := range []string{"1.3.6.1.4.1.14988.1.1.3.10.0", "2.999.4294967295", "0.0"} {
		o := MustOID(s)
		got, err := decOID(encOID(o)[2:])
		if err != nil || got.String() != s {
			t.Fatalf("%s: %v %v", s, got, err)
		}
	}
	for _, bad := range []string{"", "1", "3.1", "1.40", "1.3.x", "1.3.4294967296"} {
		if _, err := ParseOID(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if !MustOID("1.3.6.1.2").HasPrefix(MustOID("1.3.6")) || MustOID("1.3").HasPrefix(MustOID("1.3.6")) {
		t.Fatal("HasPrefix")
	}
	if MustOID("1.3.6.1.2").Compare(MustOID("1.3.6.1.10")) != -1 || MustOID("1.3.6").Compare(MustOID("1.3.6.0")) != -1 {
		t.Fatal("Compare")
	}
}

func TestIntegers(t *testing.T) {
	for _, v := range []int64{0, 1, -1, 127, 128, -128, -129, 255, 256, 1 << 31, -(1 << 31), 1<<63 - 1, -1 << 63} {
		b := encInt(tagInteger, v)
		got, err := decInt(b[2:])
		if err != nil || got != v {
			t.Fatalf("%d → % x → %d %v", v, b, got, err)
		}
	}
	for _, v := range []uint64{0, 127, 128, 1<<32 - 1, 1<<64 - 1} {
		b := encUint(tagCounter64, v)
		got, err := decUint(b[2:])
		if err != nil || got != v {
			t.Fatalf("%d → % x → %d %v", v, b, got, err)
		}
	}
	if b := encInt(tagInteger, 128); !bytes.Equal(b, []byte{2, 2, 0, 128}) {
		t.Fatalf("% x", b)
	}
}

// RFC 3414 A.3: password "maplesyrup", engine ID 00…02.
func TestPasswordToKey(t *testing.T) {
	eid, _ := hex.DecodeString("000000000000000000000002")
	for proto, want := range map[string]string{
		AuthMD5: "526f5eed9fcce26f8964c2930787d82b",         // gitleaks:allow (public RFC 3414 test vector)
		AuthSHA: "6695febc9288e36282235fc7151f128497b38f3f", // gitleaks:allow (public RFC 3414 test vector)
	} {
		if got := hex.EncodeToString(passwordToKey(authAlgs[proto].hash, "maplesyrup", eid)); got != want {
			t.Errorf("%s: %s", proto, got)
		}
	}
}

func TestDecodeFuzzSafe(t *testing.T) {
	msg := encCommunity(1, "public", &pdu{typ: pduResponse, reqID: 7, vars: []Variable{{OID: MustOID("1.3.6.1.2.1.1.1.0")}}})
	for i := range msg {
		for _, cut := range [][]byte{msg[:i], append(append([]byte(nil), msg[:i]...), 0xff)} {
			_, _, _, _ = decCommunity(cut)
			_, _ = decV3(cut)
		}
	}
	for i := range msg {
		m := append([]byte(nil), msg...)
		m[i] ^= 0xa5
		_, _, _, _ = decCommunity(m)
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(encCommunity(1, "public", &pdu{typ: pduGet, vars: []Variable{{OID: MustOID("1.3.6.1")}}}))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _, _, _ = decCommunity(b)
		_, _ = decV3(b)
	})
}

// ---- a small agent for the tests

type agent struct {
	t         *testing.T
	conn      *net.UDPConn
	community string
	user      V3
	engineID  []byte
	boots     int64
	mib       []Variable // sorted
	requests  int
}

func newAgent(t *testing.T) *agent {
	c, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if err != nil {
		t.Fatal(err)
	}
	a := &agent{t: t, conn: c, community: "s3cret", engineID: []byte("\x80\x00\x1f\x88\x04watchzit-test"), boots: 4}
	a.mib = []Variable{
		{OID: MustOID("1.3.6.1.2.1.1.1.0"), Kind: KindOctetString, Bytes: []byte("Test router")},
		{OID: MustOID("1.3.6.1.2.1.1.3.0"), Kind: KindTimeTicks, Uint: 123456},
		{OID: MustOID("1.3.6.1.2.1.1.5.0"), Kind: KindOctetString, Bytes: []byte("r1")},
	}
	for i := uint32(1); i <= 60; i++ {
		a.mib = append(a.mib, Variable{OID: MustOID("1.3.6.1.2.1.2.2.1.10").Append(i), Kind: KindCounter32, Uint: uint64(i) * 1000})
	}
	a.mib = append(a.mib, Variable{OID: MustOID("1.3.6.1.2.1.31.1.1.1.6.1"), Kind: KindCounter64, Uint: 1 << 40})
	slices.SortFunc(a.mib, func(x, y Variable) int { return x.OID.Compare(y.OID) })
	t.Cleanup(func() { _ = c.Close() })
	go a.serve()
	return a
}

func (a *agent) addr() netip.AddrPort { return a.conn.LocalAddr().(*net.UDPAddr).AddrPort() }

func encValue(v Variable) []byte {
	switch v.Kind {
	case KindOctetString:
		return encOctets(v.Bytes)
	case KindTimeTicks:
		return encUint(tagTimeTicks, v.Uint)
	case KindCounter32:
		return encUint(tagCounter32, v.Uint)
	case KindCounter64:
		return encUint(tagCounter64, v.Uint)
	case KindEndOfMibView:
		return []byte{tagEndOfMibView, 0}
	case KindNoSuchObject:
		return []byte{tagNoSuchObject, 0}
	}
	return []byte{tagNull, 0}
}

// answer runs a request PDU against the MIB and returns the response body.
func (a *agent) answer(p *pdu) []byte {
	var out []Variable
	after := func(o OID) Variable {
		for _, v := range a.mib {
			if v.OID.Compare(o) > 0 {
				return v
			}
		}
		return Variable{OID: o, Kind: KindEndOfMibView}
	}
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
		for i := int64(0); i < p.errIndex; i++ {
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
		vbs = append(vbs, tlv(tagSequence, encOID(v.OID), encValue(v)))
	}
	return tlv(pduResponse, encInt(tagInteger, int64(p.reqID)), encInt(tagInteger, 0), encInt(tagInteger, 0), tlv(tagSequence, vbs...))
}

func (a *agent) serve() {
	buf := make([]byte, 65536)
	for {
		n, from, err := a.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		a.requests++
		b := append([]byte(nil), buf[:n]...)
		var reply []byte
		if r, err := newReader(b).sub(tagSequence); err == nil && func() bool { v, _ := r.int(); return v == 3 }() {
			reply = a.serveV3(b)
		} else if _, comm, p, err := decCommunity(b); err == nil && string(comm) == a.community {
			v, _, _, _ := decCommunity(b)
			reply = tlv(tagSequence, encInt(tagInteger, v), encOctets(comm), a.answer(p))
		}
		if reply != nil {
			_, _ = a.conn.WriteToUDPAddrPort(reply, from)
		}
	}
}

func (a *agent) serveV3(b []byte) []byte {
	m, err := decV3(b)
	if err != nil {
		return nil
	}
	e := &engine{id: a.engineID, boots: a.boots, time: 1000}
	report := func(oid string) []byte {
		p := &pdu{typ: pduReport, vars: []Variable{{OID: MustOID(oid)}}}
		if m.pdu != nil {
			p.reqID = m.pdu.reqID
		}
		body := tlv(pduReport, encInt(tagInteger, int64(p.reqID)), encInt(tagInteger, 0), encInt(tagInteger, 0),
			tlv(tagSequence, tlv(tagSequence, encOID(p.vars[0].OID), encUint(tagCounter32, 1))))
		scoped := tlv(tagSequence, encOctets(a.engineID), encOctets(nil), body)
		hdr := tlv(tagSequence, encInt(tagInteger, m.msgID), encInt(tagInteger, 65507), encOctets([]byte{0}), encInt(tagInteger, 3))
		usm := tlv(tagSequence, encOctets(a.engineID), encInt(tagInteger, a.boots), encInt(tagInteger, 1000), encOctets(nil), encOctets(nil), encOctets(nil))
		return tlv(tagSequence, encInt(tagInteger, 3), hdr, encOctets(usm), scoped)
	}
	if len(m.engineID) == 0 {
		return report("1.3.6.1.6.3.15.1.1.4.0")
	}
	if string(m.user) != a.user.User {
		return report("1.3.6.1.6.3.15.1.1.3.0")
	}
	if m.flags&0x03 != a.user.flags()&0x03 {
		return report("1.3.6.1.6.3.15.1.1.1.0")
	}
	if m.flags&0x01 != 0 {
		if err := a.user.verify(m, e); err != nil {
			return report("1.3.6.1.6.3.15.1.1.5.0")
		}
		if m.boots != a.boots || m.time < 1000-150 || m.time > 1000+150 {
			return report("1.3.6.1.6.3.15.1.1.2.0")
		}
	}
	if err := a.user.open(m, e); err != nil {
		return report("1.3.6.1.6.3.15.1.1.6.0")
	}
	resp := a.answer(m.pdu)                                                 // encV3 builds requests; the reportable flag stays, which agents ignore
	out, err := a.user.encV3(int32(m.msgID), e, a.boots, 1000, resp, false) // #nosec G115 -- test
	if err != nil {
		a.t.Error(err)
		return nil
	}
	return out
}

func TestV2c(t *testing.T) {
	a := newAgent(t)
	ctx := context.Background()
	c, err := Dial(a.addr(), Config{Version: 2, Community: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	vs, err := c.Get(ctx, MustOID("1.3.6.1.2.1.1.1.0"), MustOID("1.3.6.1.2.1.1.3.0"), MustOID("1.3.6.1.2.1.1.9.0"))
	if err != nil {
		t.Fatal(err)
	}
	if vs[0].String() != "Test router" || vs[1].String() != "0d 00:20:34" || vs[2].Exists() {
		t.Fatalf("%v", vs)
	}
	walk, err := c.WalkAll(ctx, MustOID("1.3.6.1.2.1.2.2.1.10"))
	if err != nil || len(walk) != 60 || walk[59].Uint != 60000 || !walk[0].IsCounter() {
		t.Fatalf("%d %v", len(walk), err)
	}
	hc, err := c.WalkAll(ctx, MustOID("1.3.6.1.2.1.31"))
	if err != nil || len(hc) != 1 || hc[0].Uint != 1<<40 {
		t.Fatal(hc, err)
	}

	bad, _ := Dial(a.addr(), Config{Version: 2, Community: "wrong", Timeout: 200 * time.Millisecond})
	defer bad.Close()
	if _, err := bad.Get(ctx, MustOID("1.3.6.1.2.1.1.1.0")); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Fatal(err)
	}
}

// Values must not point into the client's read buffer, which the next
// answer overwrites.
func TestValuesSurviveNextRequest(t *testing.T) {
	a := newAgent(t)
	c, _ := Dial(a.addr(), Config{Version: 2, Community: "s3cret", MaxRepetitions: 1})
	defer c.Close()
	vs, err := c.WalkAll(context.Background(), MustOID("1.3.6.1.2.1.1"))
	if err != nil || len(vs) != 3 || vs[0].String() != "Test router" || vs[2].String() != "r1" {
		t.Fatal(vs, err)
	}
}

func TestV1(t *testing.T) {
	a := newAgent(t)
	c, _ := Dial(a.addr(), Config{Version: 1, Community: "s3cret"})
	defer c.Close()
	walk, err := c.WalkAll(context.Background(), MustOID("1.3.6.1.2.1.1"))
	if err != nil || len(walk) != 3 {
		t.Fatal(walk, err)
	}
}

func TestV3(t *testing.T) {
	cases := []V3{
		{User: "noauth"},
		{User: "md5", AuthProto: AuthMD5, AuthPass: "authpass1"},
		{User: "sha-des", AuthProto: AuthSHA, AuthPass: "authpass1", PrivProto: PrivDES, PrivPass: "privpass1"},
		{User: "sha-aes", AuthProto: AuthSHA, AuthPass: "authpass1", PrivProto: PrivAES, PrivPass: "privpass1"},
		{User: "sha-aes256", AuthProto: AuthSHA, AuthPass: "authpass1", PrivProto: PrivAES256, PrivPass: "privpass1"},
		{User: "sha256-aes192", AuthProto: AuthSHA256, AuthPass: "authpass1", PrivProto: PrivAES192, PrivPass: "privpass1"},
		{User: "sha512-aes256", AuthProto: AuthSHA512, AuthPass: "authpass1", PrivProto: PrivAES256, PrivPass: "privpass1", Context: "ctx"},
		{User: "sha224-aes", AuthProto: AuthSHA224, AuthPass: "authpass1", PrivProto: PrivAES, PrivPass: "privpass1"},
		{User: "sha384-des", AuthProto: AuthSHA384, AuthPass: "authpass1", PrivProto: PrivDES, PrivPass: "privpass1"},
	}
	ctx := context.Background()
	for _, u := range cases {
		t.Run(u.User, func(t *testing.T) {
			a := newAgent(t)
			a.user = u
			c, err := Dial(a.addr(), Config{Version: 3, V3: u, Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			walk, err := c.WalkAll(ctx, MustOID("1.3.6.1.2.1.2.2.1.10"))
			if err != nil || len(walk) != 60 {
				t.Fatalf("%d %v", len(walk), err)
			}
			vs, err := c.Get(ctx, MustOID("1.3.6.1.2.1.1.5.0"))
			if err != nil || vs[0].String() != "r1" {
				t.Fatal(vs, err)
			}
		})
	}

	t.Run("errors", func(t *testing.T) {
		a := newAgent(t)
		a.user = cases[3]
		for _, tc := range []struct {
			u    V3
			want string
		}{
			{V3{User: "other", AuthProto: AuthSHA, AuthPass: "authpass1", PrivProto: PrivAES, PrivPass: "privpass1"}, "unknown SNMPv3 user"},
			{V3{User: "sha-aes", AuthProto: AuthSHA, AuthPass: "wrongpass", PrivProto: PrivAES, PrivPass: "privpass1"}, "wrong authentication"},
			{V3{User: "sha-aes", AuthProto: AuthSHA, AuthPass: "authpass1", PrivProto: PrivAES, PrivPass: "wrongpass"}, "privacy"},
			{V3{User: "sha-aes", AuthProto: AuthSHA, AuthPass: "authpass1"}, "security level"},
		} {
			Forget(a.addr())
			c, _ := Dial(a.addr(), Config{Version: 3, V3: tc.u, Timeout: 300 * time.Millisecond})
			_, err := c.Get(ctx, MustOID("1.3.6.1.2.1.1.5.0"))
			_ = c.Close()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: %v (want %q)", tc.u.User, err, tc.want)
			}
		}
	})

	if err := (&V3{User: "x", AuthProto: AuthSHA, AuthPass: "short"}).Check(); err == nil {
		t.Fatal("short password accepted")
	}
	if err := (&V3{User: "x", PrivProto: PrivAES, PrivPass: "privpass1"}).Check(); err == nil {
		t.Fatal("privacy without authentication accepted")
	}
}

func TestClosedPort(t *testing.T) {
	l, _ := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	addr := l.LocalAddr().(*net.UDPAddr).AddrPort()
	_ = l.Close()
	c, _ := Dial(addr, Config{Version: 2, Community: "public", Timeout: 300 * time.Millisecond})
	defer c.Close()
	_, err := c.Get(context.Background(), MustOID("1.3.6.1.2.1.1.5.0"))
	if err == nil {
		t.Fatal("answer from a closed port")
	}
	if !errors.Is(err, ErrTimeout) && !strings.Contains(err.Error(), "refused") && !strings.Contains(err.Error(), "no answer") {
		t.Fatal(err)
	}
}
