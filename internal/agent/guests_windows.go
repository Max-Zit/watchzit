//go:build windows

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

// guests lists the Hyper-V machines when the Hyper-V service (vmms) is
// installed.
func guests(ctx context.Context) *proto.Guests {
	m, err := openSCM()
	if err != nil {
		return nil
	}
	s, err := openService(m, "vmms")
	_ = m.Disconnect()
	if err != nil {
		return nil
	}
	_ = s.Close()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	out, err := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", hyperVScript).Output() // #nosec G204 G702 -- fixed script, path from SystemRoot
	if err != nil {
		return nil
	}
	g, err := parseHyperV(out)
	if err != nil {
		return nil
	}
	return g
}
