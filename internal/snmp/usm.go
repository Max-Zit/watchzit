package snmp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des" // #nosec G502 -- DES privacy is part of SNMPv3 (RFC 3414); offered for old devices
	"crypto/hmac"
	"crypto/md5" // #nosec G501 -- HMAC-MD5-96 is part of SNMPv3 (RFC 3414); offered for old devices
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- HMAC-SHA-96 is part of SNMPv3 (RFC 3414)
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"strings"
	"sync"
)

// Authentication protocols (RFC 3414, RFC 7860).
const (
	AuthNone   = ""
	AuthMD5    = "MD5"
	AuthSHA    = "SHA"
	AuthSHA224 = "SHA-224"
	AuthSHA256 = "SHA-256"
	AuthSHA384 = "SHA-384"
	AuthSHA512 = "SHA-512"
)

// Privacy protocols (RFC 3414, RFC 3826; AES-192/256 with the key
// extension used by Net-SNMP and most devices).
const (
	PrivNone   = ""
	PrivDES    = "DES"
	PrivAES    = "AES"
	PrivAES192 = "AES-192"
	PrivAES256 = "AES-256"
)

// AuthProtocols and PrivProtocols list the choices for forms.
var (
	AuthProtocols = []string{AuthMD5, AuthSHA, AuthSHA224, AuthSHA256, AuthSHA384, AuthSHA512}
	PrivProtocols = []string{PrivDES, PrivAES, PrivAES192, PrivAES256}
)

type authAlg struct {
	hash   func() hash.Hash
	macLen int // truncated digest length in msgAuthenticationParameters
}

var authAlgs = map[string]authAlg{
	AuthMD5:    {md5.New, 12},
	AuthSHA:    {sha1.New, 12},
	AuthSHA224: {sha256.New224, 16},
	AuthSHA256: {sha256.New, 24},
	AuthSHA384: {sha512.New384, 32},
	AuthSHA512: {sha512.New, 48},
}

var privKeyLen = map[string]int{PrivDES: 16, PrivAES: 16, PrivAES192: 24, PrivAES256: 32}

// passwordToKey is the password-to-key algorithm of RFC 3414 A.2: the
// password repeated over 1 MiB is hashed, then localized with the engine ID.
func passwordToKey(h func() hash.Hash, password string, engineID []byte) []byte {
	d := h()
	pw := []byte(password)
	buf := make([]byte, 64)
	for n := 0; n < 1048576; n += 64 {
		for i := range buf {
			buf[i] = pw[(n+i)%len(pw)]
		}
		d.Write(buf)
	}
	ku := d.Sum(nil)
	d.Reset()
	d.Write(ku)
	d.Write(engineID)
	d.Write(ku)
	return d.Sum(nil)
}

// extendKey lengthens a localized key for AES-192/256 when the
// authentication hash is shorter (Kul' = Kul || H(Kul) || H(H(Kul)) …).
func extendKey(h func() hash.Hash, key []byte, n int) []byte {
	out := append([]byte(nil), key...)
	last := key
	for len(out) < n {
		d := h()
		d.Write(last)
		last = d.Sum(nil)
		out = append(out, last...)
	}
	return out[:n]
}

// The 1 MiB hashing costs a few milliseconds; localized keys are kept per
// engine, protocol and password.
var keyCache = struct {
	sync.Mutex
	m map[string][]byte
}{m: map[string][]byte{}}

func localizedKey(proto, password string, engineID []byte) []byte {
	ck := string(engineID) + "\x00" + proto + "\x00" + password
	keyCache.Lock()
	k, ok := keyCache.m[ck]
	keyCache.Unlock()
	if ok {
		return k
	}
	k = passwordToKey(authAlgs[proto].hash, password, engineID)
	keyCache.Lock()
	if len(keyCache.m) > 4096 {
		clear(keyCache.m)
	}
	keyCache.m[ck] = k
	keyCache.Unlock()
	return k
}

// V3 holds the user-based security settings of one SNMPv3 user.
type V3 struct {
	User      string
	AuthProto string
	AuthPass  string
	PrivProto string
	PrivPass  string
	Context   string
}

// Check validates the combination (RFC 3414 asks for at least 8
// characters in passwords).
func (v *V3) Check() error {
	if v.User == "" || len(v.User) > 32 {
		return errors.New("the SNMPv3 user name must be 1–32 characters")
	}
	if v.AuthProto == AuthNone {
		if v.PrivProto != PrivNone {
			return errors.New("privacy needs authentication")
		}
		return nil
	}
	if _, ok := authAlgs[v.AuthProto]; !ok {
		return fmt.Errorf("unknown authentication protocol %q", v.AuthProto)
	}
	if len(v.AuthPass) < 8 {
		return errors.New("the authentication password must have at least 8 characters")
	}
	if v.PrivProto != PrivNone {
		if _, ok := privKeyLen[v.PrivProto]; !ok {
			return fmt.Errorf("unknown privacy protocol %q", v.PrivProto)
		}
		if len(v.PrivPass) < 8 {
			return errors.New("the privacy password must have at least 8 characters")
		}
	}
	return nil
}

func (v *V3) flags() byte {
	f := byte(0x04) // reportable
	if v.AuthProto != AuthNone {
		f |= 0x01
		if v.PrivProto != PrivNone {
			f |= 0x02
		}
	}
	return f
}

func (v *V3) authKey(engineID []byte) []byte {
	return localizedKey(v.AuthProto, v.AuthPass, engineID)
}

func (v *V3) privKey(engineID []byte) []byte {
	k := localizedKey(v.AuthProto, v.PrivPass, engineID)
	if n := privKeyLen[v.PrivProto]; len(k) < n {
		k = extendKey(authAlgs[v.AuthProto].hash, k, n)
	}
	return k
}

// engine is what the client knows about the agent's SNMP engine.
type engine struct {
	id    []byte
	boots int64
	time  int64 // at the moment noted in since
	since int64 // local monotonic seconds
}

var saltCounter = func() *uint64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	v := binary.BigEndian.Uint64(b[:])
	return &v
}()

var saltMu sync.Mutex

func nextSalt() uint64 {
	saltMu.Lock()
	defer saltMu.Unlock()
	*saltCounter++
	return *saltCounter
}

// encrypt returns the encrypted scoped PDU and msgPrivacyParameters.
func (v *V3) encrypt(e *engine, boots, etime int64, plain []byte) (data, params []byte, err error) {
	key := v.privKey(e.id)
	salt := make([]byte, 8)
	switch v.PrivProto {
	case PrivDES:
		binary.BigEndian.PutUint32(salt, uint32(boots))          // #nosec G115 -- engine boots are 31-bit
		binary.BigEndian.PutUint32(salt[4:], uint32(nextSalt())) // #nosec G115 -- the low half is the counter
		iv := make([]byte, 8)
		for i := range iv {
			iv[i] = key[8+i] ^ salt[i]
		}
		blk, err := des.NewCipher(key[:8]) // #nosec G405 -- see the import
		if err != nil {
			return nil, nil, err
		}
		if pad := len(plain) % 8; pad != 0 {
			plain = append(plain, make([]byte, 8-pad)...)
		}
		out := make([]byte, len(plain))
		cipher.NewCBCEncrypter(blk, iv).CryptBlocks(out, plain)
		return out, salt, nil
	default: // AES (CFB-128, RFC 3826)
		binary.BigEndian.PutUint64(salt, nextSalt())
		blk, err := aes.NewCipher(key[:privKeyLen[v.PrivProto]])
		if err != nil {
			return nil, nil, err
		}
		out := make([]byte, len(plain))
		//lint:ignore SA1019 CFB is what RFC 3826 specifies for SNMPv3
		cipher.NewCFBEncrypter(blk, aesIV(boots, etime, salt)).XORKeyStream(out, plain)
		return out, salt, nil
	}
}

func (v *V3) decrypt(e *engine, boots, etime int64, data, params []byte) ([]byte, error) {
	if len(params) != 8 {
		return nil, errors.New("snmp: bad privacy parameters")
	}
	key := v.privKey(e.id)
	switch v.PrivProto {
	case PrivDES:
		if len(data)%8 != 0 || len(data) == 0 {
			return nil, errors.New("snmp: cannot decrypt the answer")
		}
		iv := make([]byte, 8)
		for i := range iv {
			iv[i] = key[8+i] ^ params[i]
		}
		blk, err := des.NewCipher(key[:8]) // #nosec G405 -- see the import
		if err != nil {
			return nil, err
		}
		out := make([]byte, len(data))
		cipher.NewCBCDecrypter(blk, iv).CryptBlocks(out, data)
		return out, nil
	default:
		blk, err := aes.NewCipher(key[:privKeyLen[v.PrivProto]])
		if err != nil {
			return nil, err
		}
		out := make([]byte, len(data))
		//lint:ignore SA1019 CFB is what RFC 3826 specifies for SNMPv3
		cipher.NewCFBDecrypter(blk, aesIV(boots, etime, params)).XORKeyStream(out, data)
		return out, nil
	}
}

func aesIV(boots, etime int64, salt []byte) []byte {
	iv := make([]byte, 16)
	binary.BigEndian.PutUint32(iv, uint32(boots))     // #nosec G115 -- engine boots are 31-bit
	binary.BigEndian.PutUint32(iv[4:], uint32(etime)) // #nosec G115 -- engine time is 31-bit
	copy(iv[8:], salt)
	return iv
}

// ---- v3 messages

type v3msg struct {
	msgID    int64
	flags    byte
	engineID []byte
	boots    int64
	time     int64
	user     []byte
	authOff  int // offset and size of msgAuthenticationParameters in raw
	authLen  int
	privPar  []byte
	ctxEng   []byte
	pdu      *pdu
	raw      []byte
	scopedOK bool
	enc      []byte // encrypted scoped PDU
}

// encV3 builds a request; when the user authenticates, the digest is
// filled in over the whole message.
func (v *V3) encV3(msgID int32, e *engine, boots, etime int64, body []byte, discovery bool) ([]byte, error) {
	flags := v.flags()
	user := []byte(v.User)
	if discovery {
		flags, user = 0x04, nil
	}
	scoped := tlv(tagSequence, encOctets(e.id), encOctets([]byte(v.Context)), body)
	var authParams, privParams []byte
	if flags&0x01 != 0 {
		authParams = make([]byte, authAlgs[v.AuthProto].macLen)
	}
	data := scoped
	if flags&0x02 != 0 {
		enc, salt, err := v.encrypt(e, boots, etime, scoped)
		if err != nil {
			return nil, err
		}
		data, privParams = encOctets(enc), salt
	}
	hdr := tlv(tagSequence, encInt(tagInteger, int64(msgID)), encInt(tagInteger, 65507), encOctets([]byte{flags}), encInt(tagInteger, 3))
	usm := tlv(tagSequence, encOctets(e.id), encInt(tagInteger, boots), encInt(tagInteger, etime), encOctets(user), encOctets(authParams), encOctets(privParams))
	msg := tlv(tagSequence, encInt(tagInteger, 3), hdr, encOctets(usm), data)
	if flags&0x01 != 0 {
		// find the zeroed digest by parsing the message just built
		m, err := decV3(msg)
		if err != nil {
			return nil, err
		}
		mac := hmac.New(authAlgs[v.AuthProto].hash, v.authKey(e.id))
		mac.Write(msg)
		copy(msg[m.authOff:m.authOff+m.authLen], mac.Sum(nil))
	}
	return msg, nil
}

// decV3 parses a message; the scoped PDU stays encrypted until open.
func decV3(b []byte) (*v3msg, error) {
	r, err := newReader(b).sub(tagSequence)
	if err != nil {
		return nil, err
	}
	if ver, err := r.int(); err != nil || ver != 3 {
		return nil, errBER
	}
	h, err := r.sub(tagSequence)
	if err != nil {
		return nil, err
	}
	m := &v3msg{raw: b}
	if m.msgID, err = h.int(); err != nil {
		return nil, err
	}
	if _, err = h.int(); err != nil {
		return nil, err
	}
	fl, _, err := h.expect(tagOctetString)
	if err != nil || len(fl) != 1 {
		return nil, errBER
	}
	m.flags = fl[0]
	if sm, err := h.int(); err != nil || sm != 3 {
		return nil, errors.New("snmp: unsupported security model")
	}
	sp, err := r.sub(tagOctetString)
	if err != nil {
		return nil, err
	}
	u, err := sp.sub(tagSequence)
	if err != nil {
		return nil, err
	}
	if m.engineID, _, err = u.expect(tagOctetString); err != nil {
		return nil, err
	}
	if m.boots, err = u.int(); err != nil {
		return nil, err
	}
	if m.time, err = u.int(); err != nil {
		return nil, err
	}
	if m.user, _, err = u.expect(tagOctetString); err != nil {
		return nil, err
	}
	ap, off, err := u.expect(tagOctetString)
	if err != nil {
		return nil, err
	}
	m.authOff, m.authLen = off, len(ap)
	if m.privPar, _, err = u.expect(tagOctetString); err != nil {
		return nil, err
	}
	if m.flags&0x02 != 0 {
		enc, _, err := r.expect(tagOctetString)
		if err != nil {
			return nil, err
		}
		m.privPar = append([]byte(nil), m.privPar...)
		m.enc = enc
		return m, nil
	}
	return m, m.parseScoped(r)
}

func (m *v3msg) parseScoped(r *reader) error {
	s, err := r.sub(tagSequence)
	if err != nil {
		return err
	}
	if m.ctxEng, _, err = s.expect(tagOctetString); err != nil {
		return err
	}
	if _, _, err = s.expect(tagOctetString); err != nil {
		return err
	}
	m.pdu, err = decPDU(s)
	m.scopedOK = err == nil
	return err
}

// verify checks the digest of an authenticated message.
func (v *V3) verify(m *v3msg, e *engine) error {
	alg := authAlgs[v.AuthProto]
	if m.authLen != alg.macLen {
		return errors.New("snmp: answer not authenticated (check the authentication settings)")
	}
	got := append([]byte(nil), m.raw[m.authOff:m.authOff+m.authLen]...)
	tmp := append([]byte(nil), m.raw...)
	clear(tmp[m.authOff : m.authOff+m.authLen])
	mac := hmac.New(alg.hash, v.authKey(e.id))
	mac.Write(tmp)
	if !hmac.Equal(mac.Sum(nil)[:alg.macLen], got) {
		return errors.New("snmp: answer has a wrong digest")
	}
	return nil
}

// open decrypts an encrypted scoped PDU.
func (v *V3) open(m *v3msg, e *engine) error {
	if m.flags&0x02 == 0 {
		return nil
	}
	plain, err := v.decrypt(e, m.boots, m.time, m.enc, m.privPar)
	if err != nil {
		return err
	}
	// The plain text may carry DES padding after the sequence.
	if err := m.parseScoped(newReader(plain)); err != nil {
		return errors.New("snmp: cannot decrypt the answer (check the privacy password and protocol)")
	}
	return nil
}

// usmStats names the USM report counters (RFC 3414 usmStats).
var usmStats = map[string]string{
	"1.3.6.1.6.3.15.1.1.1.0": "the device does not accept this security level",
	"1.3.6.1.6.3.15.1.1.2.0": "not in time window",
	"1.3.6.1.6.3.15.1.1.3.0": "unknown SNMPv3 user name",
	"1.3.6.1.6.3.15.1.1.4.0": "unknown engine ID",
	"1.3.6.1.6.3.15.1.1.5.0": "wrong authentication password or protocol",
	"1.3.6.1.6.3.15.1.1.6.0": "wrong privacy password or protocol",
}

func reportReason(p *pdu) string {
	if p == nil || len(p.vars) == 0 {
		return "the device reported an error"
	}
	if s, ok := usmStats[p.vars[0].OID.String()]; ok {
		return s
	}
	return "the device reported " + strings.TrimSpace(p.vars[0].OID.String())
}
