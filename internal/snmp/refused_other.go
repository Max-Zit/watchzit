//go:build !windows

package snmp

import (
	"errors"
	"syscall"
)

func isPlatformRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }
