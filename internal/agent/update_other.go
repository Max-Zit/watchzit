//go:build !linux && !windows

package agent

import "errors"

func installUpdate(string, string, string) error {
	return errors.New("this system updates the agent by hand")
}
