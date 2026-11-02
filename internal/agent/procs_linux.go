//go:build linux

package agent

import (
	"os"
	"strconv"
	"strings"
)

// clockTicks is USER_HZ, 100 on every Linux the agent runs on.
const clockTicks = 100

// processList reads /proc/<pid>/stat of every process.
func processList() ([]procInfo, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	page := uint64(os.Getpagesize()) // #nosec G115 -- a page size
	var out []procInfo
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat") // #nosec G304 -- /proc/<number>/stat
		if err != nil {
			continue
		}
		if p, ok := parseStat(pid, string(b), page); ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// parseStat reads a /proc/<pid>/stat line: "pid (comm) state …", utime and
// stime are fields 14 and 15, rss (pages) field 24.
func parseStat(pid int, s string, page uint64) (procInfo, bool) {
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return procInfo{}, false
	}
	f := strings.Fields(s[close+1:]) // f[0] is field 3 (state)
	if len(f) < 22 {
		return procInfo{}, false
	}
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	rss, _ := strconv.ParseUint(f[21], 10, 64)
	return procInfo{PID: pid, Name: s[open+1 : close], CPU: (ut + st) / clockTicks, Mem: rss * page}, true
}
