package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestProcWatcher: instances of a name are counted together (with or
// without .exe, case ignored), memory adds up and CPU comes from the
// difference of CPU times between two reports.
func TestProcWatcher(t *testing.T) {
	cpu := 10.0
	w := &ProcWatcher{prev: map[int]float64{}, list: func() ([]procInfo, error) {
		return []procInfo{{PID: 1, Name: "nginx", CPU: cpu, Mem: 100}, {PID: 2, Name: "nginx", CPU: cpu, Mem: 50}, {PID: 3, Name: "sqlservr.exe", CPU: 1, Mem: 7}}, nil
	}}
	ps := w.States(context.Background(), []string{"NGINX", "sqlservr", "missing"})
	if len(ps) != 3 || ps[0].Count != 2 || ps[0].Mem != 150 || ps[0].CPU != 0 || ps[1].Count != 1 || ps[2].Count != 0 {
		t.Fatalf("first %+v", ps)
	}
	w.prevAt = w.prevAt.Add(-10 * time.Second) // ten seconds later, each nginx used 1 s more
	cpu = 11
	ps = w.States(context.Background(), []string{"nginx"})
	want := 2.0 / 10 / float64(runtime.NumCPU()) * 100
	if d := ps[0].CPU - want; d > 0.1 || d < -0.1 {
		t.Fatalf("cpu %v, want %v", ps[0].CPU, want)
	}
	if w.States(context.Background(), nil) != nil {
		t.Fatal("nothing watched, something reported")
	}
}

// TestProcessListHere: this test program is among the processes.
func TestProcessListHere(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("no process list on this system")
	}
	exe, _ := os.Executable()
	ps := NewProcWatcher().States(context.Background(), []string{filepath.Base(exe)})
	if len(ps) != 1 || ps[0].Count < 1 || ps[0].Mem == 0 {
		t.Fatalf("%+v", ps)
	}
}
