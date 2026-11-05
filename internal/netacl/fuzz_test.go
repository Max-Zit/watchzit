package netacl

import "testing"

// FuzzParse: allow-lists are typed by administrators; whatever is accepted
// must be a valid prefix.
func FuzzParse(f *testing.F) {
	for _, s := range []string{"10.0.0.0/8", "192.168.1.5", "::1", "fe80::/10", "10.0.0.0/33", " 1.2.3.4/24 ", "", "a/b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := ParsePrefix(s)
		if err == nil && !p.IsValid() {
			t.Fatalf("%q accepted as an invalid prefix", s)
		}
		_, _ = Parse([]string{s, s})
	})
}
