package launch

import (
	"crypto/ed25519"
	"testing"
	"time"
)

// FuzzParse: a watchzit:// link comes from any web page the user opens, so
// Parse must never panic, and nothing it accepts may verify with a key that
// did not sign it.
func FuzzParse(f *testing.F) {
	pub, key, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	f.Add(PairURL(pub, "WatchZit lab"))
	if u, err := OpenURL(key, Payload{Tool: ToolSSH, Name: "SSH", Host: "10.0.0.1", Port: 22, User: "admin", Device: "core"}, now); err == nil {
		f.Add(u)
	}
	f.Add("watchzit://open?d=AAAA&s=BBBB")
	f.Add("watchzit://pair?k=%zz")
	other, _, _ := ed25519.GenerateKey(nil)
	f.Fuzz(func(t *testing.T, s string) {
		l, err := Parse(s)
		if err != nil || l == nil {
			return
		}
		_ = l.ServerID()
		if p, err := l.Verify(other, now); err == nil {
			t.Fatalf("a link verified with a key that never signed it: %+v", p)
		}
		_, _ = l.Verify(pub, now)
	})
}
