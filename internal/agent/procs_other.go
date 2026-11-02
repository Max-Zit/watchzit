//go:build !linux && !windows

package agent

import "errors"

func processList() ([]procInfo, error) { return nil, errors.New("not supported on this system") }
