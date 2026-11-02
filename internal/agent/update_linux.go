//go:build linux

package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// installUpdate puts the new program in place of the running one, after it
// shows it runs here and is the version expected, and stops this agent:
// systemd starts the new one (Restart=always).
func installUpdate(bin, version, _ string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output() // #nosec G204 G702 -- verified program in our own directory
	if err != nil || !strings.Contains(string(out), version) {
		return fmt.Errorf("the new agent does not run on this computer (%v)", err)
	}
	tmp := self + ".new"
	data, err := os.ReadFile(bin) // #nosec G304 -- our own updates folder
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o755); err != nil { // #nosec G306 -- a program
		return err
	}
	if err := os.Rename(tmp, self); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	go func() {
		time.Sleep(2 * time.Second) // let the log line go out
		os.Exit(0)
	}()
	return nil
}
