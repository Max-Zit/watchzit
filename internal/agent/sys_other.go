//go:build !linux && !windows

package agent

import (
	"context"
	"os"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

// Other systems build, but report only what Go knows without system calls.

func protectDir(dir string) error { return os.Chmod(dir, 0o700) } // #nosec G302 -- a directory needs the x bit

func markEnrolled(bool) {}

type cpuState struct{}

func cpuPercent(*cpuState) float64                            { return 0 }
func memory() (total, used, swapTotal, swapUsed uint64)       { return }
func uptimeSeconds() float64                                  { return 0 }
func bootTime() time.Time                                     { return time.Time{} }
func load1() *float64                                         { return nil }
func procs() int                                              { return 0 }
func disks() []proto.Disk                                     { return nil }
func netCounters() ([]netCounter, error)                      { return nil, nil }
func serviceStates(context.Context, []string) []proto.Service { return nil }
func basicOS() string                                         { return "" }
func platformInventory(context.Context, *proto.Inventory)     {}
