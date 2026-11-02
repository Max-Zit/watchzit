package agent

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

// Watched processes: for each name the console asks for, how many run and
// their CPU and memory together. CPU comes from the difference of the
// processes' CPU times between two reports (the first report has none).

// procInfo is one running process as the platform code reads it.
type procInfo struct {
	PID  int
	Name string
	CPU  float64 // CPU time used so far, seconds
	Mem  uint64  // resident memory, bytes
}

// ProcWatcher keeps the CPU times of the last report.
type ProcWatcher struct {
	mu     sync.Mutex
	prev   map[int]float64
	prevAt time.Time
	list   func() ([]procInfo, error) // tests replace it
}

// NewProcWatcher reads the processes of this computer.
func NewProcWatcher() *ProcWatcher { return &ProcWatcher{prev: map[int]float64{}, list: processList} }

// procMatch tells whether a process is the one a name means: the same name,
// case ignored, with or without ".exe".
func procMatch(want, have string) bool {
	w, h := strings.ToLower(strings.TrimSpace(want)), strings.ToLower(have)
	return w != "" && (w == h || strings.TrimSuffix(w, ".exe") == strings.TrimSuffix(h, ".exe"))
}

// States reports the watched names.
func (p *ProcWatcher) States(_ context.Context, names []string) []proto.Process {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(names) == 0 {
		return nil
	}
	now := time.Now()
	list, err := p.list()
	if err != nil {
		return nil
	}
	dt := now.Sub(p.prevAt).Seconds()
	cpus := float64(runtime.NumCPU())
	out := make([]proto.Process, len(names))
	cur := map[int]float64{}
	for i, n := range names {
		out[i].Name = n
	}
	for _, pr := range list {
		cur[pr.PID] = pr.CPU
		for i, n := range names {
			if !procMatch(n, pr.Name) {
				continue
			}
			out[i].Count++
			out[i].Mem += pr.Mem
			if old, ok := p.prev[pr.PID]; ok && dt > 0 && pr.CPU >= old {
				out[i].CPU += (pr.CPU - old) / dt / cpus * 100
			}
		}
	}
	for i := range out {
		out[i].CPU = float64(int(out[i].CPU*100+0.5)) / 100
	}
	p.prev, p.prevAt = cur, now
	return out
}
