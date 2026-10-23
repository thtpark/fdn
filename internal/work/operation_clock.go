package work

import (
	"context"
	"time"
)

type agentOperationClockContext struct{}

// The request start includes network round-trip time conservatively. Target UTC
// is retained for diagnostics, never used to extend a server-issued control.
type agentOperationClock struct {
	serverAt       int64
	requestStarted time.Time
}

func operationCapturedAt(ctx context.Context) int64 {
	clock, _ := ctx.Value(agentOperationClockContext{}).(agentOperationClock)
	return clock.capturedAt(time.Now())
}

func (clock agentOperationClock) capturedAt(now time.Time) int64 {
	if clock.serverAt <= 0 || clock.requestStarted.IsZero() || now.Before(clock.requestStarted) {
		return 0
	}
	return clock.serverAt + int64(now.Sub(clock.requestStarted)/time.Second)
}
