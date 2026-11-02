//go:build windows

package agent

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

// processMemoryCounters is PROCESS_MEMORY_COUNTERS.
type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// processList walks a snapshot of the processes; CPU times and the working
// set come from each process that can be opened (the agent runs as SYSTEM:
// all but a few protected ones).
func processList() ([]procInfo, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e)) // #nosec G103 G115 -- the size of the entry, as the API wants it
	var out []procInfo
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		p := procInfo{PID: int(e.ProcessID), Name: windows.UTF16ToString(e.ExeFile[:])}
		if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, e.ProcessID); err == nil {
			var created, exited, kernel, user windows.Filetime
			if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) == nil {
				p.CPU = float64(ft64(kernel)+ft64(user)) / 1e7
			}
			var m processMemoryCounters
			m.CB = uint32(unsafe.Sizeof(m))                                                                               // #nosec G103 G115 -- the size of the struct, as the API wants it
			if r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&m)), uintptr(m.CB)); r != 0 { // #nosec G103 -- the Windows API wants a pointer
				p.Mem = uint64(m.WorkingSetSize)
			}
			_ = windows.CloseHandle(h)
		}
		out = append(out, p)
	}
	return out, nil
}

// ft64 is a FILETIME as 100-nanosecond units.
func ft64(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }
