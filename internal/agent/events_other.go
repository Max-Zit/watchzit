//go:build !linux && !windows

package agent

import (
	"context"
	"time"

	"github.com/max-zit/watchzit/internal/proto"
)

func readEvents(context.Context, time.Time) ([]proto.Event, error) { return nil, nil }
