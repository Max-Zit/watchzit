package store

import "testing"

// FuzzParseSteps: web scenario steps are typed by users; parsing must not
// panic.
func FuzzParseSteps(f *testing.F) {
	f.Add("GET /login\nPOST /login user=admin&password={secret} | 302\nGET /dashboard | Welcome")
	f.Add("| | |")
	f.Add("GET")
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = ParseSteps(s)
	})
}
