package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/housefold/runtime/internal/ha"
)

type fakeStateSession struct {
	started chan string
	stopped chan struct{}
	changes chan struct{}
}

func (s *fakeStateSession) Run(ctx context.Context, token string) {
	s.started <- token
	<-ctx.Done()
	close(s.stopped)
}

func (s *fakeStateSession) Metadata() ha.StateMetadata {
	return ha.StateMetadata{Phase: ha.PhaseDisconnected, Status: ha.StatusUnavailable}
}

func (s *fakeStateSession) Changes() <-chan struct{} { return s.changes }

func TestRunListenerServesHealthAndStopsStateSession(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session := &fakeStateSession{started: make(chan string, 1), stopped: make(chan struct{}), changes: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() {
		done <- runListener(ctx, listener, "test-token", slog.New(slog.NewTextHandler(io.Discard, nil)), func() stateSession { return session })
	}()

	select {
	case token := <-session.started:
		if token != "test-token" {
			t.Fatalf("session token = %q, want test-token", token)
		}
	case <-time.After(time.Second):
		t.Fatal("state session did not start")
	}

	client := &http.Client{Timeout: time.Second}
	var response *http.Response
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		response, err = client.Get("http://" + address + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runListener returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime did not stop after cancellation")
	}
	select {
	case <-session.stopped:
	default:
		t.Fatal("runtime returned before the state session stopped")
	}
	if _, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		t.Fatal("health listener accepted a connection after shutdown")
	}
}

func TestRunListenerFailureCancelsMonitor(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	session := &fakeStateSession{started: make(chan string, 1), stopped: make(chan struct{}), changes: make(chan struct{}, 1)}
	err = runListener(context.Background(), listener, "", slog.New(slog.NewTextHandler(io.Discard, nil)), func() stateSession { return session })
	if err == nil {
		t.Fatal("runListener returned nil for a closed listener")
	}
	select {
	case <-session.stopped:
	default:
		t.Fatal("runListener returned before canceling its state session")
	}
}

func TestRunReturnsListenFailure(t *testing.T) {
	if err := run(context.Background(), "127.0.0.1:-1", slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("run returned nil for an invalid listen address")
	}
}
