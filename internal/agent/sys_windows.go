//go:build windows

package agent

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/max-zit/watchzit/internal/proto"
)

// protectDir gives only SYSTEM and Administrators access (no inherited
// "Users: read" from ProgramData). When the agent runs as another user
// (enrolled from an elevated prompt, or a test run), that user keeps access.
func protectDir(dir string) error {
	sddl := "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if tu, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil && !tu.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
		sddl += "(A;OICI;FA;;;" + tu.User.Sid.String() + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// markEnrolled records the enrollment in the registry, where the MSI looks
// for it (an upgrade then does not ask for a code again).
func markEnrolled(on bool) {
	const path = `SOFTWARE\MaxZit\WatchZit Agent`
	if !on {
		_ = registry.DeleteKey(registry.LOCAL_MACHINE, path)
		return
	}
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, path, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return
	}
	defer k.Close()
	_ = k.SetStringValue("Enrolled", "1")
}

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetTickCount64       = kernel32.NewProc("GetTickCount64")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
	iphlpapi                 = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetIfTable2          = iphlpapi.NewProc("GetIfTable2")
	procFreeMibTable         = iphlpapi.NewProc("FreeMibTable")
)

type cpuState struct{ idle, kernel, user uint64 }

func ft(f windows.Filetime) uint64 { return uint64(f.HighDateTime)<<32 | uint64(f.LowDateTime) }

func cpuPercent(st *cpuState) float64 {
	var idle, kernel, user windows.Filetime
	if r, _, _ := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&idle)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user))); r == 0 { // #nosec G103 -- Win32 out-parameters
		return 0
	}
	i, k, u := ft(idle), ft(kernel), ft(user)
	di, dk, du := i-st.idle, k-st.kernel, u-st.user
	first := st.kernel == 0
	st.idle, st.kernel, st.user = i, k, u
	total := dk + du // kernel time includes idle time
	if first || total == 0 {
		return 0
	}
	return max(0, min(100, float64(total-di)/float64(total)*100))
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func memory() (total, used, swapTotal, swapUsed uint64) {
	ms := memoryStatusEx{}
	ms.Length = uint32(unsafe.Sizeof(ms))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms))); r == 0 { // #nosec G103 -- Win32 struct
		return
	}
	total, used = ms.TotalPhys, ms.TotalPhys-ms.AvailPhys
	// the commit limit minus RAM is the page file
	if ms.TotalPageFile > ms.TotalPhys {
		swapTotal = ms.TotalPageFile - ms.TotalPhys
		commitUsed := ms.TotalPageFile - ms.AvailPageFile
		if commitUsed > used {
			swapUsed = min(commitUsed-used, swapTotal)
		}
	}
	return
}

func uptimeSeconds() float64 {
	ms, _, _ := procGetTickCount64.Call()
	return float64(ms) / 1000
}

func bootTime() time.Time {
	return time.Now().Add(-time.Duration(uptimeSeconds() * float64(time.Second))).UTC().Truncate(time.Second)
}

func load1() *float64 { return nil }

func procs() int { return 0 }

func disks() []proto.Disk {
	buf := make([]uint16, 256)
	n, err := windows.GetLogicalDriveStrings(uint32(len(buf)), &buf[0]) // #nosec G115 -- 256
	if err != nil || n == 0 {
		return nil
	}
	var out []proto.Disk
	for _, d := range strings.Split(windows.UTF16ToString(buf[:n]), "\x00") {
		if d == "" {
			continue
		}
		p, _ := windows.UTF16PtrFromString(d)
		if windows.GetDriveType(p) != windows.DRIVE_FIXED {
			continue
		}
		var free, total, totalFree uint64
		if windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree) != nil || total == 0 {
			continue
		}
		fsName := make([]uint16, 32)
		_ = windows.GetVolumeInformation(p, nil, 0, nil, nil, nil, &fsName[0], uint32(len(fsName))) // #nosec G115 -- 32
		out = append(out, proto.Disk{Mount: strings.TrimSuffix(d, `\`), FS: windows.UTF16ToString(fsName), Total: total, Used: total - totalFree})
	}
	return out
}

// MIB_IF_ROW2 (netioapi.h), read by offsets: the struct is 1352 bytes.
const (
	ifRowSize     = 1352
	offIndex      = 8
	offGUID       = 12
	offAlias      = 28
	offDescr      = 542
	offPhysLen    = 1056
	offPhys       = 1060
	offType       = 1128
	offFlags      = 1152
	offOperStatus = 1156
	offTxSpeed    = 1192
	offInOctets   = 1208
	offInErrors   = 1240
	offOutOctets  = 1280
	offOutErrors  = 1312
)

func utf16At(b []byte, off, chars int) string {
	u := make([]uint16, chars)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[off+2*i:])
	}
	return windows.UTF16ToString(u)
}

func netCounters() ([]netCounter, error) {
	var table unsafe.Pointer
	if r, _, _ := procGetIfTable2.Call(uintptr(unsafe.Pointer(&table))); r != 0 { // #nosec G103 -- Win32 out-parameter
		return nil, fmt.Errorf("GetIfTable2: error %d", r)
	}
	defer procFreeMibTable.Call(uintptr(table)) //nolint:errcheck // nothing to do on failure
	num := *(*uint32)(table)                    // #nosec G103 -- MIB_IF_TABLE2.NumEntries
	vlans := adapterVLANs()
	var out []netCounter
	for i := uint32(0); i < num && i < 4096; i++ {
		row := unsafe.Slice((*byte)(unsafe.Add(table, 8+uintptr(i)*ifRowSize)), ifRowSize) // #nosec G103 -- rows follow the 8-byte header
		typ := binary.LittleEndian.Uint32(row[offType:])
		flags := row[offFlags]
		hardware, filter, connector := flags&1 != 0, flags&2 != 0, flags&4 != 0
		// skip loopback, tunnels and the filter layers Windows stacks on adapters
		if filter || typ == 24 || typ == 131 || !(hardware || connector) {
			continue
		}
		c := netCounter{
			Index:    int(binary.LittleEndian.Uint32(row[offIndex:])),
			Name:     utf16At(row, offAlias, 257),
			Alias:    utf16At(row, offDescr, 257),
			Up:       binary.LittleEndian.Uint32(row[offOperStatus:]) == 1,
			Speed:    int64(min(binary.LittleEndian.Uint64(row[offTxSpeed:]), 1<<62)), // #nosec G115 -- capped
			InBytes:  binary.LittleEndian.Uint64(row[offInOctets:]),
			InErr:    binary.LittleEndian.Uint64(row[offInErrors:]),
			OutBytes: binary.LittleEndian.Uint64(row[offOutOctets:]),
			OutErr:   binary.LittleEndian.Uint64(row[offOutErrors:]),
		}
		if n := binary.LittleEndian.Uint32(row[offPhysLen:]); n == 6 {
			c.MAC = macOf(row[offPhys : offPhys+6])
		}
		if id := vlans[guidAt(row, offGUID)]; id > 0 {
			c.VLANs = []int{id}
		}
		out = append(out, c)
	}
	return out, nil
}

var stateNames = map[uint32]string{1: "stopped", 2: "starting", 3: "stopping", 4: "running", 5: "continuing", 6: "pausing", 7: "paused"}

// openSCM and openService ask only for read access (the agent never
// changes services).
func openSCM() (*mgr.Mgr, error) {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT|windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return nil, err
	}
	return &mgr.Mgr{Handle: h}, nil
}

func openService(m *mgr.Mgr, name string) (*mgr.Service, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.OpenService(m.Handle, p, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return nil, err
	}
	return &mgr.Service{Name: name, Handle: h}, nil
}

func serviceStates(_ context.Context, names []string) []proto.Service {
	var out []proto.Service
	m, err := openSCM()
	if err != nil {
		for _, n := range names {
			out = append(out, proto.Service{Name: n, State: "unknown"})
		}
		return out
	}
	defer m.Disconnect()
	for _, n := range names {
		s := proto.Service{Name: n, State: "missing"}
		if svc, err := openService(m, n); err == nil {
			if st, err := svc.Query(); err == nil {
				s.State = stateNames[uint32(st.State)]
				s.Running = st.State == 4
			}
			if cfg, err := svc.Config(); err == nil {
				s.Display = cfg.DisplayName
			}
			_ = svc.Close()
		}
		out = append(out, s)
	}
	return out
}

func regString(path, name string) string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func basicOS() string {
	const cv = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	name := regString(cv, "ProductName")
	build, _ := strconv.Atoi(regString(cv, "CurrentBuildNumber"))
	if build >= 22000 {
		name = strings.Replace(name, "Windows 10", "Windows 11", 1) // ProductName still says 10
	}
	if dv := regString(cv, "DisplayVersion"); dv != "" {
		name += " " + dv
	}
	return strings.TrimSpace(name)
}

func platformInventory(_ context.Context, inv *proto.Inventory) {
	const cv = `SOFTWARE\Microsoft\Windows NT\CurrentVersion`
	inv.OSVersion = regString(cv, "CurrentBuildNumber")
	if ubr, err := registry.OpenKey(registry.LOCAL_MACHINE, cv, registry.QUERY_VALUE|registry.WOW64_64KEY); err == nil {
		if v, _, err := ubr.GetIntegerValue("UBR"); err == nil {
			inv.OSVersion += "." + strconv.FormatUint(v, 10)
		}
		_ = ubr.Close()
	}
	inv.Kernel = "NT " + inv.OSVersion
	inv.CPUModel = regString(`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "ProcessorNameString")
	inv.Vendor = regString(`HARDWARE\DESCRIPTION\System\BIOS`, "SystemManufacturer")
	inv.Model = regString(`HARDWARE\DESCRIPTION\System\BIOS`, "SystemProductName")
	m, err := openSCM()
	if err != nil {
		return
	}
	defer m.Disconnect()
	names, err := m.ListServices()
	if err != nil {
		return
	}
	for _, n := range names {
		s, err := openService(m, n)
		if err != nil {
			continue
		}
		cfg, err := s.Config()
		_ = s.Close()
		// drivers and per-user service copies are not offered
		if err != nil || cfg.ServiceType&windows.SERVICE_WIN32 == 0 || strings.Contains(n, "_") && len(n) > 6 && isHexSuffix(n) {
			continue
		}
		inv.Services = append(inv.Services, proto.ServiceInfo{Name: n, Display: cfg.DisplayName, Auto: cfg.StartType == mgr.StartAutomatic})
	}
	sort.Slice(inv.Services, func(i, j int) bool {
		return strings.ToLower(inv.Services[i].Display) < strings.ToLower(inv.Services[j].Display)
	})
}

// isHexSuffix spots per-user services such as "CDPUserSvc_4a1b2c".
func isHexSuffix(n string) bool {
	_, suf, _ := strings.Cut(n, "_")
	if len(suf) < 4 {
		return false
	}
	_, err := strconv.ParseUint(suf, 16, 64)
	return err == nil
}

// macOf formats a hardware address ("" for none).
func macOf(b []byte) string {
	if len(b) != 6 {
		return ""
	}
	return net.HardwareAddr(b).String()
}

// guidAt formats the InterfaceGuid of an MIB_IF_ROW2 the way the registry
// writes it ({XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX}), in upper case.
func guidAt(row []byte, off int) string {
	b := row[off : off+16]
	return strings.ToUpper(fmt.Sprintf("{%08x-%04x-%04x-%x-%x}", binary.LittleEndian.Uint32(b[0:]),
		binary.LittleEndian.Uint16(b[4:]), binary.LittleEndian.Uint16(b[6:]), b[8:10], b[10:16]))
}

// adapterVLANs reads the VLAN ID set on network adapters (driver setting
// "VlanID" of the network adapter class), by adapter GUID.
func adapterVLANs() map[string]int {
	const class = `SYSTEM\CurrentControlSet\Control\Class\{4d36e972-e325-11ce-bfc1-08002be10318}`
	out := map[string]int{}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, class, registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
	if err != nil {
		return out
	}
	defer k.Close()
	subs, _ := k.ReadSubKeyNames(-1)
	for _, s := range subs {
		a, err := registry.OpenKey(k, s, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		guid, _, _ := a.GetStringValue("NetCfgInstanceId")
		v, _, err := a.GetStringValue("VlanID")
		if err != nil {
			if n, _, err2 := a.GetIntegerValue("VlanID"); err2 == nil {
				v, err = strconv.FormatUint(n, 10), nil
			}
		}
		_ = a.Close()
		if id, e := strconv.Atoi(strings.TrimSpace(v)); err == nil && e == nil && id >= 1 && id <= 4094 && guid != "" {
			out[strings.ToUpper(guid)] = id
		}
	}
	return out
}

// vxlans: Windows has no VXLAN interfaces of its own (Hyper-V SDN keeps
// them in the virtual switch).
func vxlans(context.Context) []proto.VXLAN { return nil }
