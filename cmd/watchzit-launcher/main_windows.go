package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const appName = "WatchZit Launcher"

// boxes are Windows message boxes; quiet answers "yes" without showing
// anything (installation scripts and tests).
type boxes struct{ quiet bool }

func (b boxes) show(title, text string, flags uint32) int32 {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(title)
	r, _ := windows.MessageBox(0, t, c, flags|windows.MB_SETFOREGROUND|windows.MB_TOPMOST)
	return r
}

func (b boxes) ask(title, text string) bool {
	if b.quiet {
		return true
	}
	return b.show(title, text, windows.MB_YESNO|windows.MB_ICONQUESTION|windows.MB_DEFBUTTON2) == 6 // IDYES
}

func (b boxes) info(title, text string) {
	if !b.quiet {
		b.show(title, text, windows.MB_OK|windows.MB_ICONINFORMATION)
	}
}

func (b boxes) fail(title, text string) {
	if !b.quiet {
		b.show(title, text, windows.MB_OK|windows.MB_ICONWARNING)
	}
}

func dataDir() string {
	d, err := os.UserConfigDir() // %APPDATA%
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, appName)
}

func installDir() string {
	d, err := os.UserCacheDir() // %LOCALAPPDATA%
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "Programs", appName)
}

func main() {
	args := os.Args[1:]
	quiet := false
	if len(args) > 0 && args[0] == "--quiet" {
		quiet, args = true, args[1:]
	}
	ui := boxes{quiet: quiet}
	logw := openLog()
	logf := func(format string, a ...any) {
		fmt.Fprintf(logw, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
	}
	switch {
	case len(args) == 0:
		if err := install(); err != nil {
			ui.fail(appName, "The launcher could not be installed: "+err.Error())
			os.Exit(1)
		}
		logf("installed %s", version)
		ui.info(appName, "The WatchZit Launcher is installed for your Windows user.\n\nGo back to the console, open the Launcher page and click Pair this computer.")
	case args[0] == "--uninstall":
		uninstall()
		logf("uninstalled")
		ui.info(appName, "The WatchZit Launcher was removed.")
	case args[0] == "--version":
		ui.info(appName, appName+" "+version)
	case strings.HasPrefix(strings.ToLower(args[0]), "watchzit:"):
		cfg, err := loadConfig(dataDir())
		if err != nil {
			ui.fail(appName, err.Error())
			os.Exit(1)
		}
		l := &launcher{cfg: cfg, ui: ui, now: time.Now, find: func(t string) string { return findProgram(cfg, t) },
			temp: filepath.Join(os.TempDir(), appName), run: start, log: logf}
		if err := l.handle(args[0]); err != nil {
			logf("refused: %v", err)
			ui.fail(appName, capitalize(err.Error())+".")
			os.Exit(1)
		}
	default:
		ui.fail(appName, "Unknown command. Run the launcher without arguments to install it.")
		os.Exit(2)
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.TrimSuffix(s[1:], ".")
}

func openLog() io.Writer {
	d := dataDir()
	if err := os.MkdirAll(d, 0o700); err != nil {
		return io.Discard
	}
	p := filepath.Join(d, "launcher.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 1<<20 {
		_ = os.Rename(p, p+".old")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G304 -- own file in %APPDATA%
	if err != nil {
		return io.Discard
	}
	return f
}

// start runs a program detached; console programs (ssh.exe) get their own
// console window.
func start(program string, args []string) error {
	cmd := exec.Command(program, args...) // #nosec G204 G702 -- program and arguments come from a verified, re-checked link (see internal/launch)
	cmd.Dir = filepath.Dir(program)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s could not be started: %v", filepath.Base(program), err)
	}
	return cmd.Process.Release()
}

// ---- finding programs

func expand(p string) string {
	if !strings.Contains(p, "%") {
		return p
	}
	src, _ := windows.UTF16PtrFromString(p)
	buf := make([]uint16, 1024)
	n, err := windows.ExpandEnvironmentStrings(src, &buf[0], uint32(len(buf))) // #nosec G115 -- 1024
	if err != nil || n == 0 || int(n) > len(buf) {
		return p
	}
	return windows.UTF16ToString(buf[:n])
}

func exists(p string) bool {
	st, err := os.Stat(p) // #nosec G703 -- a program path from the launcher.json of this user or a verified link
	return err == nil && !st.IsDir()
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if p = expand(p); p != "" && exists(p) {
			return p
		}
	}
	return ""
}

// findProgram returns the program of a built-in tool or the expanded path of
// a custom one ("" when it is not on this computer).
func findProgram(cfg *config, tool string) string {
	if c, ok := strings.CutPrefix(tool, "custom:"); ok {
		return firstExisting(c)
	}
	if p, ok := cfg.Paths[tool]; ok && exists(expand(p)) {
		return expand(p)
	}
	home, _ := os.UserHomeDir()
	switch tool {
	case "putty":
		if p, err := exec.LookPath("putty.exe"); err == nil {
			return p
		}
		return firstExisting(`%ProgramFiles%\PuTTY\putty.exe`, `%ProgramFiles(x86)%\PuTTY\putty.exe`, `%LOCALAPPDATA%\Programs\PuTTY\putty.exe`)
	case "ssh":
		return firstExisting(`%SystemRoot%\System32\OpenSSH\ssh.exe`, `%ProgramFiles%\OpenSSH\ssh.exe`)
	case "mstsc":
		return firstExisting(`%SystemRoot%\System32\mstsc.exe`)
	case "winbox":
		var c []string
		for _, n := range []string{"winbox64.exe", "winbox.exe", "WinBox.exe"} {
			c = append(c, `%ProgramFiles%\MikroTik\`+n, `%ProgramFiles%\Winbox\`+n, `%LOCALAPPDATA%\Programs\WinBox\`+n,
				filepath.Join(home, "Desktop", n), filepath.Join(home, "Downloads", n))
		}
		return firstExisting(c...)
	case "vnc":
		return firstExisting(`%ProgramFiles%\TightVNC\tvnviewer.exe`, `%ProgramFiles%\TigerVNC\vncviewer.exe`, `%ProgramFiles%\RealVNC\VNC Viewer\vncviewer.exe`,
			`%ProgramFiles(x86)%\TightVNC\tvnviewer.exe`, `%ProgramFiles(x86)%\TigerVNC\vncviewer.exe`)
	}
	return ""
}

// ---- installation for the current user (no administrator rights)

const (
	classKey     = `Software\Classes\watchzit`
	uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\WatchZitLauncher`
)

func install() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	dir := installDir()
	dst := filepath.Join(dir, "watchzit-launcher.exe")
	if !strings.EqualFold(filepath.Clean(self), filepath.Clean(dst)) {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		if err := copyFile(self, dst); err != nil {
			return err
		}
	}
	cmd := `"` + dst + `" "%1"`
	sets := []struct{ key, name, value string }{
		{classKey, "", "URL:WatchZit Launcher"},
		{classKey, "URL Protocol", ""},
		{classKey + `\DefaultIcon`, "", dst + ",0"},
		{classKey + `\shell\open\command`, "", cmd},
		{uninstallKey, "DisplayName", appName},
		{uninstallKey, "DisplayVersion", version},
		{uninstallKey, "Publisher", "MaxZit"},
		{uninstallKey, "InstallLocation", dir},
		{uninstallKey, "DisplayIcon", dst},
		{uninstallKey, "UninstallString", `"` + dst + `" --uninstall`},
	}
	for _, s := range sets {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, s.key, registry.SET_VALUE)
		if err != nil {
			return err
		}
		err = k.SetStringValue(s.name, s.value)
		_ = k.Close()
		if err != nil {
			return err
		}
	}
	if k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE); err == nil {
		_ = k.SetDWordValue("NoModify", 1)
		_ = k.SetDWordValue("NoRepair", 1)
		_ = k.Close()
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) // #nosec G304 -- the launcher's own executable
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755) // #nosec G302 G304 -- a program file in the user's own folder
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func uninstall() {
	for _, k := range []string{classKey + `\shell\open\command`, classKey + `\shell\open`, classKey + `\shell`, classKey + `\DefaultIcon`, classKey, uninstallKey} {
		_ = registry.DeleteKey(registry.CURRENT_USER, k)
	}
	// the running program cannot delete itself: a hidden cmd does it a moment later
	dst := filepath.Join(installDir(), "watchzit-launcher.exe")
	c := exec.Command("cmd.exe", "/c", "ping -n 3 127.0.0.1 >nul & del /q \""+dst+"\" & rmdir \""+installDir()+"\"") // #nosec G204 -- fixed paths of the own installation
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	_ = c.Start()
}
