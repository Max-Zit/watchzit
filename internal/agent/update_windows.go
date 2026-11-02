//go:build windows

package agent

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// installUpdate runs the MSI detached: it stops and replaces this service.
func installUpdate(msi, _, logPath string) error {
	// only an agent the MSI installed: another copy would run beside it
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if pf := os.Getenv("ProgramFiles"); pf == "" || !strings.HasPrefix(strings.ToLower(self), strings.ToLower(filepath.Join(pf, "WatchZit Agent")+string(filepath.Separator))) {
		return errors.New("this agent was not installed with the MSI; update it by hand")
	}
	exe := filepath.Join(os.Getenv("SystemRoot"), "System32", "msiexec.exe")
	cmd := exec.Command(exe, "/i", msi, "/qn", "/norestart", "/l*v", logPath) // #nosec G204 G702 -- verified installer in our own directory
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
