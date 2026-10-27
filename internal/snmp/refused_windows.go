//go:build windows

package snmp

import (
	"errors"

	"golang.org/x/sys/windows"
)

// Windows reports an ICMP port unreachable on a UDP socket as a reset.
func isPlatformRefused(err error) bool {
	return errors.Is(err, windows.WSAECONNRESET) || errors.Is(err, windows.WSAECONNREFUSED)
}
