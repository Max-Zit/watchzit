//go:build !windows

package main

import (
	"fmt"
	"os"
)

// The launcher is a Windows program for now (Linux and macOS later).
func main() {
	fmt.Fprintln(os.Stderr, "maxzit-launcher "+version+" runs on Windows only")
	os.Exit(2)
}
