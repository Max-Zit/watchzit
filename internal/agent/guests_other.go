//go:build !linux && !windows

package agent

import (
	"context"

	"github.com/max-zit/watchzit/internal/proto"
)

func guests(context.Context) *proto.Guests { return nil }
