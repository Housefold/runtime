package ha

import (
	"context"
	"log/slog"
	"time"
)

const (
	firstRetryDelay = time.Second
	maxRetryDelay   = 30 * time.Second
	connectedPoll   = 30 * time.Second
)

// Monitor probes Home Assistant immediately, retries failures with bounded
// exponential backoff, and rechecks connected status periodically. It logs
// only transitions among the coarse connection statuses.
func Monitor(ctx context.Context, token string, logger *slog.Logger) {
	monitor(ctx, func(probeCtx context.Context) Result {
		return Probe(probeCtx, token)
	}, waitContext, logger)
}

type probeFunc func(context.Context) Result
type waitFunc func(context.Context, time.Duration) bool

func monitor(ctx context.Context, probe probeFunc, wait waitFunc, logger *slog.Logger) {
	monitorObserved(ctx, probe, wait, logger, nil)
}

// MonitorWithObserver reports every completed probe without exposing probe
// details. The observer receives only the coarse result and its check time.
func MonitorWithObserver(ctx context.Context, token string, logger *slog.Logger, observer func(Status, time.Time)) {
	monitorObserved(ctx, func(probeCtx context.Context) Result {
		return Probe(probeCtx, token)
	}, waitContext, logger, observer)
}

func monitorObserved(ctx context.Context, probe probeFunc, wait waitFunc, logger *slog.Logger, observer func(Status, time.Time)) {
	var previous Status
	retryDelay := firstRetryDelay
	for ctx.Err() == nil {
		result := probe(ctx)
		if ctx.Err() != nil {
			return
		}
		checkedAt := time.Now().UTC()
		if observer != nil {
			observer(result.Status, checkedAt)
		}
		if result.Status != previous {
			logger.Info("Home Assistant connection status changed", "status", result.Status)
			previous = result.Status
		}

		if result.Status == StatusConnected {
			retryDelay = firstRetryDelay
			if !wait(ctx, connectedPoll) {
				return
			}
			continue
		}

		if !wait(ctx, retryDelay) {
			return
		}
		retryDelay *= 2
		if retryDelay > maxRetryDelay {
			retryDelay = maxRetryDelay
		}
	}
}

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
