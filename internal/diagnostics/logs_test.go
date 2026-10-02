package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type memorySink struct {
	mu     sync.Mutex
	data   bytes.Buffer
	signal chan struct{}
}

func (s *memorySink) Write(_ context.Context, raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.data.Write(raw)
	if s.signal != nil {
		select {
		case s.signal <- struct{}{}:
		default:
		}
	}
	return err
}
func (s *memorySink) Close() error { return nil }
func TestStructuredLogAttributionPrivacyRateRetention(t *testing.T) {
	sink := &memorySink{signal: make(chan struct{}, 200)}
	h := New(sink)
	defer h.Close()
	l := slog.New(h)
	now := time.Now()
	h.shared.now = func() time.Time { return now }
	for i := 0; i < 1000; i++ {
		l.Info("module event", "module", "synthetic", "version", "1.0.0", "generation", uint64(42), "code", "ready", "token", "PRIVATE_CREDENTIAL", "entity_id", "light.private", "message", "PRIVATE_HOUSEHOLD")
	}
	snap := h.Snapshot()
	if len(snap.Entries) != ModuleLogsPerSecond || snap.Dropped < 936 {
		t.Fatal("log rate unbounded", len(snap.Entries), snap.Dropped)
	}
	for _, e := range snap.Entries {
		if e.Module != "synthetic" || e.Version != "1.0.0" || e.Generation != 42 || e.Code != "ready" {
			t.Fatal("lost attribution", e)
		}
	}
	l.Info("PRIVATE_HOUSEHOLD", "error", "PRIVATE_CREDENTIAL")
	<-sink.signal
	sink.mu.Lock()
	out := sink.data.String()
	sink.mu.Unlock()
	for _, bad := range []string{"PRIVATE", "entity_id", "token"} {
		if strings.Contains(out, bad) {
			t.Fatal("live log leaked", out)
		}
	}
	for i := 0; i < 300; i++ {
		l.Info("runtime stopped")
	}
	if len(h.Snapshot().Entries) != MaxLogs {
		t.Fatal("ring unbounded")
	}
	copy := h.Snapshot()
	copy.Entries[0].Message = "MUTATED"
	if h.Snapshot().Entries[0].Message == "MUTATED" {
		t.Fatal("ring aliases")
	}
	raw, err := json.Marshal(h.Snapshot())
	if err != nil || len(raw) > 64<<10 {
		t.Fatal("oversized ring", len(raw), err)
	}
}

type stalledSink struct {
	entered chan struct{}
	once    sync.Once
}

func (s *stalledSink) Write(ctx context.Context, _ []byte) error {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()
	return ctx.Err()
}
func (s *stalledSink) Close() error { return nil }
func TestBackpressureNeverBlocksProducerAndShutdownJoins(t *testing.T) {
	sink := &stalledSink{entered: make(chan struct{})}
	h := New(sink)
	l := slog.New(h)
	l.Info("runtime started")
	<-sink.entered
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10000; i++ {
			l.Info("runtime stopped")
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("producer blocked on App output")
	}
	if len(h.shared.queue) > LogQueue || h.Snapshot().Dropped == 0 {
		t.Fatal("queue unbounded")
	}
	joined := make(chan struct{})
	go func() { h.Close(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("logger did not join")
	}
}
func TestInheritedPipeDeadlineBoundsActualBlockedOutput(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	fd, err := syscall.Dup(int(writer.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err = syscall.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	bounded := os.NewFile(uintptr(fd), "bounded-synthetic-app-pipe")
	defer bounded.Close()
	started := time.Now()
	err = (fileSink{bounded}).Write(context.Background(), bytes.Repeat([]byte("X"), 4<<20))
	if err == nil || time.Since(started) > time.Second {
		t.Fatal("actual blocked pipe lacked bounded deadline", err, time.Since(started))
	}
}

func TestShutdownFlushesFinalEventWithinBudget(t *testing.T) {
	sink := &memorySink{}
	h := New(sink)
	slog.New(h).Error("Runtime privilege drop failed", "error", "PRIVATE_CREDENTIAL")
	h.Close()
	sink.mu.Lock()
	out := sink.data.String()
	sink.mu.Unlock()
	if !strings.Contains(out, "Runtime privilege drop failed") || strings.Contains(out, "PRIVATE_") {
		t.Fatal("final event lost/leaked", out)
	}
}
