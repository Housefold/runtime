package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/housefold/runtime/internal/bootstrap"
	"github.com/housefold/runtime/internal/catalog"
	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/estate"
	"github.com/housefold/runtime/internal/ha"
	"github.com/housefold/runtime/internal/hacontrol"
	"github.com/housefold/runtime/internal/supervisor"
)

var buildVersion = "dev"
var buildSource = "unknown"

const processShutdownTimeout = 9 * time.Second

var errProcessShutdownTimeout = errors.New("runtime shutdown deadline exceeded")

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dataErr := bootstrap.Prepare("/data/housefold", 10001)
	if err := bootstrap.Drop(10001); err != nil {
		logger.Error("Runtime privilege drop failed")
		os.Exit(1)
	}
	if dataErr != nil {
		logger.Error("Runtime storage recovery required")
	}
	if err := run(ctx, ":8099", logger, dataErr != nil); err != nil {
		logger.Error("runtime stopped with error", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("runtime stopped")
}

func run(ctx context.Context, address string, logger *slog.Logger, recovery ...bool) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for runtime health: %w", err)
	}
	required := len(recovery) > 0 && recovery[0]
	return runListener(ctx, listener, os.Getenv("SUPERVISOR_TOKEN"), logger, func() stateSession { return ha.NewStateSession(logger) }, runtimeComposition{RecoveryRequired: required, NewEstate: func(source stateSession, status *supervisor.StatusStore) backgroundService {
		native := hacontrol.NewNative(os.Getenv("SUPERVISOR_TOKEN"), source.(*ha.StateSession))
		if required {
			native.SetSignals(func() hacontrol.OperationalSnapshot {
				return hacontrol.OperationalSnapshot{Runtime: "recovery_required", Catalog: "unconfigured"}
			})
			return native
		}
		arch := "amd64"
		if runtime.GOARCH == "arm64" {
			arch = "aarch64"
		}
		client := catalog.New(durable.NewFile("/data/housefold/catalog.json"), arch)
		owner := estate.New(estate.Config{Root: "/data/housefold", Source: source.(*ha.StateSession), Logger: logger, OnRecovery: status.SetRecoveryRequired, Authority: catalog.OfficialAuthority(), Catalog: client, Actions: native, Discovery: native})
		status.SetEstate(owner)
		native.SetDiscoverySink(owner.PublishDiscovery)
		native.SetSignals(owner.OperationalSnapshot)
		return joinedServices{owner, native}
	}})
}

type stateSession interface {
	Run(context.Context, string)
	Metadata() ha.StateMetadata
	Changes() <-chan struct{}
}

type stateSessionFactory func() stateSession
type backgroundService interface{ Run(context.Context) }
type runtimeComposition struct {
	RecoveryRequired bool
	NewEstate        func(stateSession, *supervisor.StatusStore) backgroundService
}

func runListener(ctx context.Context, listener net.Listener, token string, logger *slog.Logger, newSession stateSessionFactory, composition ...runtimeComposition) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	logger.Info("runtime started", "health", "/healthz", "version", buildVersion, "source", buildSource)
	status := &supervisor.StatusStore{}
	if len(composition) > 0 && composition[0].RecoveryRequired {
		status.SetRecoveryRequired()
	}
	service := supervisor.NewService(status)
	serviceDone := make(chan error, 1)
	go func() { serviceDone <- service.Run(runCtx, listener) }()

	session := newSession()
	var estateDone chan struct{}
	if len(composition) > 0 && composition[0].NewEstate != nil {
		if owner := composition[0].NewEstate(session, status); owner != nil {
			estateDone = make(chan struct{})
			go func() { defer close(estateDone); owner.Run(runCtx) }()
		}
	}
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
	for serviceWait != nil || sessionWait != nil || notifyWait != nil || estateDone != nil {
		select {
		case err = <-serviceWait:
			serviceWait = nil
		case <-sessionWait:
			sessionWait = nil
		case <-notifyWait:
			notifyWait = nil
		case <-estateDone:
			estateDone = nil
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

// Each dependency owner survives estate recovery and joins on process shutdown.
type joinedServices []backgroundService

func (services joinedServices) Run(ctx context.Context) {
	var workers sync.WaitGroup
	for _, service := range services {
		workers.Add(1)
		go func(owner backgroundService) { defer workers.Done(); owner.Run(ctx) }(service)
	}
	workers.Wait()
}
