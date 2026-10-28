package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"testing"
	"time"
)

// TestSampleThisMachine reads the real machine twice: memory, disks and
// network counters must be there, rates on the second sample.
func TestSampleThisMachine(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("no collectors on " + runtime.GOOS)
	}
	s := NewSampler()
	ctx := context.Background()
	s.Sample(ctx, nil)
	time.Sleep(1100 * time.Millisecond)
	r := s.Sample(ctx, []string{"no-such-service-wz"})
	if r.MemTotal == 0 || r.MemUsed == 0 || r.MemUsed > r.MemTotal || r.Uptime <= 0 || r.CPU < 0 || r.CPU > 100 {
		t.Fatalf("%+v", r)
	}
	if len(r.Disks) == 0 || r.Disks[0].Total == 0 {
		t.Fatalf("disks %+v", r.Disks)
	}
	rates := 0
	for _, n := range r.Nets {
		if n.InBps != nil {
			rates++
		}
	}
	if len(r.Nets) == 0 || rates == 0 {
		t.Fatalf("nets %+v", r.Nets)
	}
	if len(r.Services) != 1 || r.Services[0].State != "missing" || r.Services[0].Running {
		t.Fatalf("services %+v", r.Services)
	}
	inv := CollectInventory(ctx)
	if inv.Hostname == "" || inv.OS == "" || inv.CPUs == 0 || inv.MemTotal == 0 {
		t.Fatalf("inventory %+v", inv)
	}
	b, _ := json.MarshalIndent(r, "", " ")
	t.Logf("report: %s\ninventory: %s %s %s cpu=%q services=%d", b, inv.OS, inv.OSVersion, inv.Kernel, inv.CPUModel, len(inv.Services))
}

func TestEnrollCode(t *testing.T) {
	fp := "AB:CD" + string(make([]byte, 0))
	_ = fp
	code := "WZ1-" + "e30"
	if _, err := ParseEnrollCode(code); err == nil {
		t.Fatal("empty code accepted")
	}
	if _, err := ParseEnrollCode("CZ1-xyz"); err == nil {
		t.Fatal("foreign code accepted")
	}
}

func TestParseProcVLAN(t *testing.T) {
	in := "VLAN Dev name    | VLAN ID\nName-Type: VLAN_NAME_TYPE_RAW_PLUS_VID_NO_PAD\neth0.20        | 20  | eth0\neth0.10        | 10  | eth0\nbond0.300      | 300  | bond0\nbad | x | eth1\n"
	got := parseProcVLAN(in)
	if fmt.Sprint(got) != "map[bond0:[300] eth0:[10 20]]" {
		t.Fatal(got)
	}
}
