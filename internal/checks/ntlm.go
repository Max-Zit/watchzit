package checks

import (
	"bytes"
	"crypto/hmac"
	"crypto/md5" // #nosec G501 -- NTLM (MS-NLMP) is defined with HMAC-MD5; WinRM offers nothing else without Kerberos
	"crypto/rc4" // #nosec G503 -- NTLM message sealing is RC4 by definition
	"encoding/binary"
	"errors"
	"strings"
	"time"
	"unicode/utf16"

	//lint:ignore SA1019 NTLM hashes the password with MD4 by definition
	"golang.org/x/crypto/md4" // #nosec G506 -- NTLM (MS-NLMP) needs MD4; nothing else uses it
)

// NTLMv2 sign-in with message signing and sealing (MS-NLMP), for WinRM
// over HTTP, where Windows wants the SOAP messages encrypted.

const (
	ntlmUnicode       = 0x00000001
	ntlmRequestTarget = 0x00000004
	ntlmSign          = 0x00000010
	ntlmSeal          = 0x00000020
	ntlmNTLM          = 0x00000200
	ntlmAlwaysSign    = 0x00008000
	ntlmExtSession    = 0x00080000
	ntlmTargetInfo    = 0x00800000
	ntlmVersion       = 0x02000000
	ntlm128           = 0x20000000
	ntlmKeyExch       = 0x40000000
	ntlm56            = 0x80000000

	ntlmWant = ntlmUnicode | ntlmRequestTarget | ntlmSign | ntlmSeal | ntlmNTLM | ntlmAlwaysSign | ntlmExtSession |
		ntlmTargetInfo | ntlmVersion | ntlm128 | ntlmKeyExch | ntlm56

	avEOL       = 0
	avFlags     = 6
	avTimestamp = 7
	avTarget    = 9
	avChannel   = 10
)

var le = binary.LittleEndian

type ntlmClient struct {
	user, domain, password string
	spn                    string // HTTP/host, sent as the target name
	negotiate              []byte
	flags                  uint32

	sendSign, recvSign []byte
	sendSeal, recvSeal *rc4.Cipher
	sendSeq, recvSeq   uint32
}

func newNTLM(user, password, host string) *ntlmClient {
	c := &ntlmClient{user: user, password: password, spn: "HTTP/" + host}
	if d, u, ok := strings.Cut(user, `\`); ok {
		c.domain, c.user = d, u
	}
	return c
}

func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, r := range u {
		le.PutUint16(b[2*i:], r)
	}
	return b
}

func hmacMD5(key []byte, parts ...[]byte) []byte {
	h := hmac.New(md5.New, key)
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

func md5Of(parts ...[]byte) []byte {
	h := md5.New() // #nosec G401 -- NTLM key derivation (MS-NLMP 3.4.5)
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// ntlmVersionBytes: Windows 10.0, NTLM revision 15 (only informative).
var ntlmVersionBytes = []byte{10, 0, 0x61, 0x4a, 0, 0, 0, 15}

// negotiateMessage is the first message (type 1).
func (c *ntlmClient) negotiateMessage() []byte {
	b := make([]byte, 40)
	copy(b, "NTLMSSP\x00")
	le.PutUint32(b[8:], 1)
	le.PutUint32(b[12:], ntlmWant)
	le.PutUint32(b[20:], 40) // empty domain at the end
	le.PutUint32(b[28:], 40) // empty workstation
	copy(b[32:], ntlmVersionBytes)
	c.negotiate = b
	return b
}

type ntlmChallenge struct {
	flags      uint32
	server     []byte // 8 bytes
	targetInfo []byte
}

func ntlmField(b []byte, at int) ([]byte, error) {
	if at+8 > len(b) {
		return nil, errors.New("short NTLM message")
	}
	n, off := int(le.Uint16(b[at:])), int(le.Uint32(b[at+4:]))
	if off < 0 || n < 0 || off > len(b) || n > len(b)-off {
		return nil, errors.New("bad NTLM field")
	}
	return b[off : off+n], nil
}

func parseChallenge(b []byte) (*ntlmChallenge, error) {
	if len(b) < 48 || !bytes.Equal(b[:8], []byte("NTLMSSP\x00")) || le.Uint32(b[8:]) != 2 {
		return nil, errors.New("the server's answer is not an NTLM challenge")
	}
	ch := &ntlmChallenge{flags: le.Uint32(b[20:]), server: b[24:32]}
	ti, err := ntlmField(b, 40)
	if err != nil {
		return nil, err
	}
	ch.targetInfo = ti
	return ch, nil
}

type avPair struct {
	id  uint16
	val []byte
}

func parseAV(b []byte) ([]avPair, error) {
	var out []avPair
	for len(b) >= 4 {
		id, n := le.Uint16(b), int(le.Uint16(b[2:]))
		if 4+n > len(b) {
			return nil, errors.New("bad NTLM target information")
		}
		if id == avEOL {
			return out, nil
		}
		out = append(out, avPair{id, b[4 : 4+n]})
		b = b[4+n:]
	}
	return nil, errors.New("NTLM target information without an end")
}

func writeAV(pairs []avPair) []byte {
	var b bytes.Buffer
	for _, p := range append(pairs, avPair{id: avEOL}) {
		var h [4]byte
		le.PutUint16(h[:], p.id)
		le.PutUint16(h[2:], uint16(len(p.val))) // #nosec G115 -- values come from a 16-bit length or are short
		b.Write(h[:])
		b.Write(p.val)
	}
	return b.Bytes()
}

func ntowfv2(password, user, domain string) []byte {
	h := md4.New() // #nosec G406 -- NTOWFv2 is MD4 of the password by definition (MS-NLMP 3.3.2)
	h.Write(utf16le(password))
	return hmacMD5(h.Sum(nil), utf16le(strings.ToUpper(user)+domain))
}

// ntlmv2 returns NTProofStr and the NT response (MS-NLMP 3.3.2).
func ntlmv2(keyNT, server, client, timestamp, targetInfo []byte) (proof, resp []byte) {
	var temp bytes.Buffer
	temp.Write([]byte{1, 1, 0, 0, 0, 0, 0, 0})
	temp.Write(timestamp)
	temp.Write(client)
	temp.Write([]byte{0, 0, 0, 0})
	temp.Write(targetInfo)
	temp.Write([]byte{0, 0, 0, 0})
	proof = hmacMD5(keyNT, server, temp.Bytes())
	return proof, append(append([]byte{}, proof...), temp.Bytes()...)
}

func fileTime(t time.Time) []byte {
	b := make([]byte, 8)
	le.PutUint64(b, uint64(t.UnixNano()/100+116444736000000000)) // #nosec G115 -- times after 1601
	return b
}

// authenticateMessage answers the challenge (type 3) and sets the keys.
// cbt is the TLS channel binding hash (nil over HTTP); client and
// session are random 8 and 16 bytes.
func (c *ntlmClient) authenticateMessage(challenge []byte, cbt []byte, now time.Time, client, session []byte) ([]byte, error) {
	ch, err := parseChallenge(challenge)
	if err != nil {
		return nil, err
	}
	if ch.flags&ntlmExtSession == 0 || ch.flags&ntlmUnicode == 0 {
		return nil, errors.New("the server offers only old NTLM versions")
	}
	pairs, err := parseAV(ch.targetInfo)
	if err != nil {
		return nil, err
	}
	var ts []byte
	var flagsAV uint32
	keep := pairs[:0]
	for _, p := range pairs {
		switch p.id {
		case avTimestamp:
			ts = p.val
		case avFlags:
			if len(p.val) == 4 {
				flagsAV = le.Uint32(p.val)
			}
			continue
		case avChannel, avTarget:
			continue
		}
		keep = append(keep, p)
	}
	hasTS := len(ts) == 8
	if !hasTS {
		ts = fileTime(now)
	}
	fv := make([]byte, 4)
	le.PutUint32(fv, flagsAV|0x2) // a MIC is there
	keep = append(keep, avPair{avFlags, fv})
	cb := make([]byte, 16)
	if cbt != nil {
		cb = channelBindings(cbt)
	}
	keep = append(keep, avPair{avChannel, cb}, avPair{avTarget, utf16le(c.spn)})
	targetInfo := writeAV(keep)

	keyNT := ntowfv2(c.password, c.user, c.domain)
	proof, nt := ntlmv2(keyNT, ch.server, client, ts, targetInfo)
	lm := make([]byte, 24)
	if !hasTS {
		lm = append(hmacMD5(keyNT, ch.server, client), client...)
	}
	kxKey := hmacMD5(keyNT, proof)
	c.flags = ch.flags & ntlmWant
	exported, encKey := kxKey, []byte{}
	if c.flags&ntlmKeyExch != 0 {
		exported = session
		encKey = make([]byte, 16)
		rc, _ := rc4.NewCipher(kxKey) // #nosec G405 -- NTLM key exchange is RC4 (MS-NLMP 3.4.5.1)
		rc.XORKeyStream(encKey, session)
	}

	domain, user, ws := utf16le(c.domain), utf16le(c.user), []byte{}
	const head = 88 // 64 + version 8 + MIC 16
	payload := [][]byte{lm, nt, domain, user, ws, encKey}
	at := []int{12, 20, 28, 36, 44, 52}
	msg := make([]byte, head)
	copy(msg, "NTLMSSP\x00")
	le.PutUint32(msg[8:], 3)
	off := head
	for i, p := range payload {
		le.PutUint16(msg[at[i]:], uint16(len(p)))   // #nosec G115 -- short fields
		le.PutUint16(msg[at[i]+2:], uint16(len(p))) // #nosec G115
		le.PutUint32(msg[at[i]+4:], uint32(off))    // #nosec G115
		off += len(p)
	}
	le.PutUint32(msg[60:], c.flags)
	copy(msg[64:], ntlmVersionBytes)
	for _, p := range payload {
		msg = append(msg, p...)
	}
	mic := hmacMD5(exported, c.negotiate, challenge, msg)
	copy(msg[72:88], mic)
	c.keys(exported)
	return msg, nil
}

// channelBindings is the MD5 of gss_channel_bindings_struct with the
// application data "tls-server-end-point:<hash of the certificate>".
func channelBindings(certHash []byte) []byte {
	app := append([]byte("tls-server-end-point:"), certHash...)
	b := make([]byte, 20, 20+len(app))
	le.PutUint32(b[16:], uint32(len(app))) // #nosec G115 -- a short hash
	return md5Of(append(b, app...))
}

func (c *ntlmClient) keys(exported []byte) {
	c.sendSign = md5Of(exported, []byte("session key to client-to-server signing key magic constant\x00"))
	c.recvSign = md5Of(exported, []byte("session key to server-to-client signing key magic constant\x00"))
	seal := exported
	switch {
	case c.flags&ntlm128 != 0:
	case c.flags&ntlm56 != 0:
		seal = exported[:7]
	default:
		seal = exported[:5]
	}
	c.sendSeal, _ = rc4.NewCipher(md5Of(seal, []byte("session key to client-to-server sealing key magic constant\x00"))) // #nosec G405 -- NTLM sealing is RC4 (MS-NLMP 3.4.4)
	c.recvSeal, _ = rc4.NewCipher(md5Of(seal, []byte("session key to server-to-client sealing key magic constant\x00"))) // #nosec G405 -- NTLM sealing is RC4 (MS-NLMP 3.4.4)
	c.sendSeq, c.recvSeq = 0, 0
}

func (c *ntlmClient) mac(sign []byte, seal *rc4.Cipher, seq uint32, msg []byte) []byte {
	var s [4]byte
	le.PutUint32(s[:], seq)
	sum := hmacMD5(sign, s[:], msg)[:8]
	if c.flags&ntlmKeyExch != 0 {
		seal.XORKeyStream(sum, sum)
	}
	sig := make([]byte, 16)
	le.PutUint32(sig, 1)
	copy(sig[4:], sum)
	le.PutUint32(sig[12:], seq)
	return sig
}

// seal encrypts a message to the server; it returns the signature and the
// sealed message.
func (c *ntlmClient) seal(msg []byte) (sig, sealed []byte) {
	sealed = make([]byte, len(msg))
	c.sendSeal.XORKeyStream(sealed, msg)
	sig = c.mac(c.sendSign, c.sendSeal, c.sendSeq, msg)
	c.sendSeq++
	return sig, sealed
}

// unseal decrypts a message from the server and checks its signature.
func (c *ntlmClient) unseal(sig, sealed []byte) ([]byte, error) {
	if len(sig) != 16 {
		return nil, errors.New("bad NTLM signature")
	}
	msg := make([]byte, len(sealed))
	c.recvSeal.XORKeyStream(msg, sealed)
	want := c.mac(c.recvSign, c.recvSeal, c.recvSeq, msg)
	c.recvSeq++
	if !hmac.Equal(want, sig) {
		return nil, errors.New("the server's answer has a wrong NTLM signature")
	}
	return msg, nil
}
