//go:build linux

package agent

import "testing"

func TestParseStat(t *testing.T) {
	line := "1234 (my (odd) name) S 1 1234 1234 0 -1 4194560 300 0 0 0 250 50 0 0 20 0 1 0 100 10000000 512 18446744073709551615"
	p, ok := parseStat(1234, line, 4096)
	if !ok || p.Name != "my (odd) name" || p.CPU != 3 || p.Mem != 512*4096 {
		t.Fatalf("%+v %v", p, ok)
	}
	if _, ok := parseStat(1, "garbage", 4096); ok {
		t.Fatal("garbage parsed")
	}
}
