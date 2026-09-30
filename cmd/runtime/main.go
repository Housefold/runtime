package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/housefold/runtime/internal/ha"
	"github.com/housefold/runtime/internal/supervisor"
)

const processShutdownTimeout = 9 * time.Second

var errProcessShutdownTimeout = errors.New("runtime shutdown deadline exceeded")

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, ":8099", logger); err != nil {
		logger.Error("runtime stopped with error", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("runtime stopped")
}

func run(ctx context.Context, address string, logger *slog.Logger) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for runtime health: %w", err)
	}
	return runListener(ctx, listener, os.Getenv("SUPERVISOR_TOKEN"), logger, func() stateSession { return ha.NewStateSession(logger) })
}

type stateSession interface {
	Run(context.Context, string)
	Metadata() ha.StateMetadata
	Changes() <-chan struct{}
}

type stateSessionFactory func() stateSession

func runListener(ctx context.Context, listener net.Listener, token string, logger *slog.Logger, newSession stateSessionFactory) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	logger.Info("runtime started", "health", "/healthz")
	status := &supervisor.StatusStore{}
	service := supervisor.NewService(status)
	serviceDone := make(chan error, 1)
	go func() { serviceDone <- service.Run(runCtx, listener) }()

	session := newSession()
	updateStatus := func() {
		metadata := session.Metadata()
		freshness := "none"
		if metadata.Fresh {
			freshness = "fresh"
		} else if metadata.Generation > 0 {
			freshness = "stale"
		} else if metadata.Phase == ha.PhaseSyncing {
			freshness = "synchronizing"
		}
		var lastSuccessful time.Time
		if metadata.LastSuccessfulSync != nil {
			lastSuccessful = *metadata.LastSuccessfulSync
		}
		status.UpdateHA(string(metadata.Phase), string(metadata.Status), freshness, metadata.Generation, metadata.EntityCount, lastSuccessful)
	}
	updateStatus()
	sessionRunDone := make(chan struct{})
	go func() {
		defer close(sessionRunDone)
		session.Run(runCtx, token)
	}()
	sessionNotifyDone := make(chan struct{})
	go func() {
		defer close(sessionNotifyDone)
		for {
			select {
			case <-runCtx.Done():
				return
			case <-sessionRunDone:
				return
			case <-session.Changes():
				updateStatus()
			}
		}
	}()

	// The Supervisor grants ten seconds. Start one whole-process deadline
	// when shutdown is requested, rather than spending eight seconds in HTTP
	// shutdown and then waiting indefinitely for worker joins.
	var err error
	serviceWait, sessionWait, notifyWait := serviceDone, sessionRunDone, sessionNotifyDone
	select {
	case err = <-serviceWait:
		serviceWait = nil
	case <-ctx.Done():
	}
	cancel()
	deadline := time.NewTimer(processShutdownTimeout)
	defer deadline.Stop()
	for serviceWait != nil || sessionWait != nil || notifyWait != nil {
		select {
		case err = <-serviceWait:
			serviceWait = nil
		case <-sessionWait:
			sessionWait = nil
		case <-notifyWait:
			notifyWait = nil
		case <-deadline.C:
			_ = listener.Close()
			return errProcessShutdownTimeout
		}
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
