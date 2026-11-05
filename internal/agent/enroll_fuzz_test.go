package agent

import "testing"

// FuzzParseEnrollCode: enrollment codes are pasted by people (and into the
// MSI); a broken one must give an error, never a panic.
func FuzzParseEnrollCode(f *testing.F) {
	f.Add("WZ1-")
	f.Add("WZ1-eyJ1IjoiaHR0cHM6Ly8xMC4wLjAuMTo4NDQ0In0")
	f.Add("WZ1-!!!!")
	f.Add("  wz1-abc  ")
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = ParseEnrollCode(s)
	})
}
