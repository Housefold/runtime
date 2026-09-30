// Package ha owns bounded Home Assistant state ingestion over WebSocket.
// Operational readiness and freshness have one authority: StateSession.
package ha

import (
	"context"
	"time"
)

const (
	defaultWebSocketURL = "ws://supervisor/core/websocket"
	firstRetryDelay     = time.Second
	maxRetryDelay       = 30 * time.Second
)

// Status describes the coarse operational WebSocket connection status.
type Status string

const (
	StatusConnected   Status = "connected"
	StatusDenied      Status = "denied"
	StatusUnavailable Status = "unavailable"
)

type waitFunc func(context.Context, time.Duration) bool

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
