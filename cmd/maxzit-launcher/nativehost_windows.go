//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/max-zit/watchzit/internal/connect"
)

// serveHost answers the one message of a browser that started the launcher.
func serveHost(logf func(string, ...any)) {
	raw, err := connect.ReadMessage(os.Stdin)
	if err != nil {
		_ = connect.WriteMessage(os.Stdout, connect.Response{Error: "unreadable message"})
		return
	}
	resp := handleMessage(raw, connect.Env{GOOS: "windows", LookPath: hostLookPath}, runAction)
	if resp.Error != "" {
		logf("PassZit request refused: %s", resp.Error)
	} else if !strings.Contains(string(raw), `"ping"`) {
		logf("PassZit opened a connection")
	}
	_ = connect.WriteMessage(os.Stdout, resp)
}

func hostDirs() []string {
	pf, pf86 := os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")
	return []string{
		filepath.Join(os.Getenv("SystemRoot"), "System32"),
		filepath.Join(os.Getenv("SystemRoot"), "System32", "OpenSSH"),
		filepath.Join(pf, "PuTTY"), filepath.Join(pf86, "PuTTY"),
		filepath.Join(pf, "WinBox"), filepath.Join(pf86, "WinBox"), filepath.Join(pf, "MikroTik"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "WinBox"),
		filepath.Join(pf, "WinSCP"), filepath.Join(pf86, "WinSCP"), filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "WinSCP"),
		filepath.Join(pf, "TightVNC"), filepath.Join(pf86, "TightVNC"), filepath.Join(pf, "TigerVNC"), filepath.Join(pf86, "TigerVNC"),
		filepath.Join(pf, "RealVNC", "VNC Viewer"), filepath.Join(pf, "uvnc bvba", "UltraVNC"),
	}
}

// hostLookPath finds a tool: the path set in launcher.json first, then
// PATH and the usual install folders.
func hostLookPath(names ...string) (string, error) {
	cfg, _ := loadConfig(dataDir())
	for _, n := range names {
		if cfg != nil {
			if p, ok := cfg.Paths[strings.TrimSuffix(strings.ToLower(n), ".exe")]; ok && exists(expand(p)) {
				return expand(p), nil
			}
		}
		if p, err := exec.LookPath(n); err == nil {
			return p, nil
		}
		for _, dir := range hostDirs() {
			if p := filepath.Join(dir, n); exists(p) {
				return p, nil
			}
		}
	}
	return "", errors.New("not found")
}

// runAction writes the secrets, starts the program and leaves the clean-up
// to a detached copy of the launcher (the browser may stop this one).
func runAction(a *connect.Action) error {
	var files []string
	args := append([]string(nil), a.Args...)
	for _, s := range a.Secrets {
		f, err := os.CreateTemp("", tempPrefix+"*")
		if err != nil {
			return err
		}
		_, werr := f.Write(s.Data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		files = append(files, f.Name())
		if werr != nil {
			removeFiles(files)
			return werr
		}
		for i := range args {
			if args[i] == s.Arg {
				args[i] = f.Name()
			}
		}
	}
	if a.Cred != nil {
		if err := writeCred(a.Cred); err != nil {
			removeFiles(files)
			return fmt.Errorf("the Windows credential could not be stored: %w", err)
		}
	}
	cmd := exec.Command(a.Program, args...) // #nosec G204 -- a known tool with checked host, port and user (internal/connect)
	if a.Stdin != "" {
		cmd.Stdin = strings.NewReader(a.Stdin)
	}
	cmd, err := startDetached(cmd, a.Console)
	if err != nil {
		removeFiles(files)
		if a.Cred != nil {
			deleteCred(a.Cred.Target)
		}
		return fmt.Errorf("%s could not be started: %w", filepath.Base(a.Program), err)
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	if len(files) > 0 || a.Cred != nil {
		target := ""
		if a.Cred != nil {
			target = a.Cred.Target
		}
		cargs := cleanupArgs(a.Keep, files, target)
		self, err := os.Executable()
		if err == nil {
			_, err = startDetached(exec.Command(self, cargs...), false) // #nosec G204 -- this program
		}
		if err != nil { // clean up here as a last resort
			time.Sleep(a.Keep)
			cleanup(cargs[1:], deleteCred)
		}
	}
	return nil
}

func removeFiles(files []string) {
	for _, f := range files {
		_ = os.Remove(f)
	}
}

// startDetached starts a program apart from the launcher and from the
// browser that started it (browsers keep native hosts in a job that ends
// with them). A console program gets a window of its own.
func startDetached(c *exec.Cmd, console bool) (*exec.Cmd, error) {
	flags := uint32(windows.CREATE_NEW_PROCESS_GROUP)
	if console {
		flags |= windows.CREATE_NEW_CONSOLE
	} else {
		flags |= windows.DETACHED_PROCESS
	}
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags | windows.CREATE_BREAKAWAY_FROM_JOB}
	if err := c.Start(); err == nil {
		return c, nil
	}
	// the job does not allow breaking away: start it inside
	c2 := exec.Command(c.Path, c.Args[1:]...) // #nosec G204 -- the same checked command
	c2.Stdin = c.Stdin
	c2.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
	return c2, c2.Start()
}

// ---- Windows credentials for Remote Desktop

var (
	advapi32       = windows.NewLazySystemDLL("advapi32.dll")
	procCredWrite  = advapi32.NewProc("CredWriteW")
	procCredDelete = advapi32.NewProc("CredDeleteW")
)

const (
	credTypeDomainPassword = 2
	credPersistSession     = 1
)

type credential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

// writeCred stores a credential for Remote Desktop, only for this logon
// session; the clean-up removes it again after mstsc started.
func writeCred(c *connect.WinCred) error {
	target, err := windows.UTF16PtrFromString(c.Target)
	if err != nil {
		return err
	}
	user, err := windows.UTF16PtrFromString(c.User)
	if err != nil {
		return err
	}
	pw, err := windows.UTF16FromString(c.Password)
	if err != nil {
		return err
	}
	pw = pw[:len(pw)-1] // without the final NUL
	blob := make([]byte, len(pw)*2)
	for i, v := range pw {
		blob[2*i], blob[2*i+1] = byte(v), byte(v>>8) // #nosec G115 -- the two bytes of a UTF-16 unit
	}
	defer clear(blob)
	cred := credential{Type: credTypeDomainPassword, TargetName: target, UserName: user, Persist: credPersistSession,
		CredentialBlobSize: uint32(len(blob))} // #nosec G115 -- a password of at most 1024 characters
	if len(blob) > 0 {
		cred.CredentialBlob = &blob[0]
	}
	r, _, e := procCredWrite.Call(uintptr(unsafe.Pointer(&cred)), 0) // #nosec G103 -- the Windows API wants a pointer
	if r == 0 {
		return e
	}
	return nil
}

func deleteCred(targetName string) {
	t, err := windows.UTF16PtrFromString(targetName)
	if err != nil {
		return
	}
	_, _, _ = procCredDelete.Call(uintptr(unsafe.Pointer(t)), credTypeDomainPassword, 0) // #nosec G103 -- the Windows API wants a pointer
}

// ---- registration with the browsers (per user)

var chromeHostKeys = []string{
	`Software\Google\Chrome\NativeMessagingHosts\` + connect.HostName,
	`Software\Microsoft\Edge\NativeMessagingHosts\` + connect.HostName,
	`Software\Chromium\NativeMessagingHosts\` + connect.HostName,
	`Software\BraveSoftware\Brave-Browser\NativeMessagingHosts\` + connect.HostName,
	`Software\Vivaldi\NativeMessagingHosts\` + connect.HostName,
}

var firefoxHostKey = `Software\Mozilla\NativeMessagingHosts\` + connect.HostName

// registerHost lets an extension start this launcher: Chromium ids are
// added to those already allowed.
func registerHost(exe string, p pairing) error {
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return err
	}
	set := func(key, file string) error {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, key, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		return k.SetStringValue("", file)
	}
	if p.Browser == "firefox" {
		m, err := connect.FirefoxManifest(exe)
		if err != nil {
			return err
		}
		f := filepath.Join(dataDir(), "firefox-host.json")
		if err := os.WriteFile(f, m, 0o600); err != nil { // #nosec G703 -- the launcher's own file in %APPDATA%
			return err
		}
		return set(firefoxHostKey, f)
	}
	f := filepath.Join(dataDir(), "chrome-host.json")
	old, _ := os.ReadFile(f) // #nosec G304 -- the launcher's own file
	ids := chromeIDs(old)
	if !slices.Contains(ids, p.ID) {
		ids = append(ids, p.ID)
	}
	m, err := connect.ChromeManifest(exe, ids)
	if err != nil {
		return err
	}
	if err := os.WriteFile(f, m, 0o600); err != nil { // #nosec G703 -- the launcher's own file in %APPDATA%
		return err
	}
	for _, k := range chromeHostKeys {
		if err := set(k, f); err != nil {
			return err
		}
	}
	return nil
}

func unregisterHost() {
	for _, k := range append(append([]string(nil), chromeHostKeys...), firefoxHostKey) {
		_ = registry.DeleteKey(registry.CURRENT_USER, k)
	}
	_ = os.Remove(filepath.Join(dataDir(), "chrome-host.json"))
	_ = os.Remove(filepath.Join(dataDir(), "firefox-host.json"))
}
