// Package diagnostics owns bounded, privacy-allowlisted Runtime/App logging.
package diagnostics

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"reflect"
	"regexp"
	"sync"
	"syscall"
	"time"
)

const MaxLogs = 128
const LogQueue = 64
const ModuleLogsPerSecond = 64
const WriteTimeout = 100 * time.Millisecond

var safeModule = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)
var safeValue = regexp.MustCompile(`^[a-zA-Z0-9_.:/-]{1,64}$`)

type Event struct {
	At         time.Time `json:"at"`
	Level      string    `json:"level"`
	Message    string    `json:"message"`
	Module     string    `json:"module,omitempty"`
	Version    string    `json:"version,omitempty"`
	Generation uint64    `json:"generation,omitempty"`
	Code       string    `json:"code,omitempty"`
	Phase      string    `json:"phase,omitempty"`
	Status     string    `json:"status,omitempty"`
	Source     string    `json:"source,omitempty"`
}
type Snapshot struct {
	Entries      []Event
	Dropped      uint64
	OutputFailed uint64
}
type Sink interface {
	Write(context.Context, []byte) error
	Close() error
}
type fileSink struct{ file *os.File }

func (s fileSink) Write(ctx context.Context, raw []byte) error {
	deadline := time.Now().Add(WriteTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := s.file.SetWriteDeadline(deadline); err != nil {
		return err
	}
	for len(raw) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := s.file.Write(raw)
		if err != nil {
			return err
		}
		raw = raw[n:]
	}
	return nil
}
func (s fileSink) Close() error { return s.file.Close() }

// Dup + nonblocking makes inherited App pipes deadline-capable. Only this
// writer receives the duplicated descriptor; children inherit fd3 IPC only.
func NewStdout() (*Handler, error) {
	fd, err := syscall.Dup(1)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err = syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "bounded-app-log")
	return New(fileSink{file}), nil
}

type shared struct {
	mu              sync.Mutex
	logs            []Event
	dropped, failed uint64
	window          time.Time
	count           int
	queue           chan []byte
	cancel          context.CancelFunc
	done            chan struct{}
	sink            Sink
	now             func() time.Time
}
type Handler struct {
	shared *shared
	attrs  []slog.Attr
}

func New(sink Sink) *Handler {
	ctx, cancel := context.WithCancel(context.Background())
	s := &shared{queue: make(chan []byte, LogQueue), cancel: cancel, done: make(chan struct{}), sink: sink, now: time.Now}
	go func() {
		defer close(s.done)
		defer sink.Close()
		// Flush final/fatal events only within a finite shutdown budget.
		defer func() {
			flush, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			for flush.Err() == nil {
				select {
				case raw := <-s.queue:
					if err := sink.Write(flush, raw); err != nil {
						s.mu.Lock()
						s.failed++
						s.mu.Unlock()
					}
				default:
					return
				}
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case raw := <-s.queue:
				writeCtx := ctx
				var flushCancel context.CancelFunc
				if ctx.Err() != nil {
					writeCtx, flushCancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
				}
				if err := sink.Write(writeCtx, raw); err != nil {
					s.mu.Lock()
					s.failed++
					s.mu.Unlock()
				}
				if flushCancel != nil {
					flushCancel()
					return
				}

			}
		}
	}()
	return &Handler{shared: s}
}
func (h *Handler) Close()                                   { h.shared.cancel(); <-h.shared.done }
func (h *Handler) Enabled(context.Context, slog.Level) bool { return true }
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{h.shared, append(append([]slog.Attr(nil), h.attrs...), attrs...)}
}
func (h *Handler) WithGroup(string) slog.Handler { return &Handler{h.shared, nil} } // No free-form nested payloads.
func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	e := Event{At: r.Time.UTC(), Level: r.Level.String(), Message: r.Message}
	switch e.Message {
	case "runtime started", "runtime stopped", "runtime stopped with error", "Runtime privilege drop failed", "Runtime storage recovery required", "Runtime estate recovery required", "Runtime estate storage degraded", "cold snapshot checkpoint unavailable", "Home Assistant state session phase changed", "Home Assistant state session status changed", "module event":
	default:
		e.Message = "runtime event"
	}
	add := func(a slog.Attr) bool {
		v := a.Value.Resolve()
		if a.Key == "generation" {
			if v.Kind() == slog.KindUint64 {
				e.Generation = v.Uint64()
			}
			return true
		}
		if v.Kind() != slog.KindString && v.Kind() != slog.KindAny {
			return true
		}
		value := ""
		if v.Kind() == slog.KindString {
			value = v.String()
		} else {
			reflected := reflect.ValueOf(v.Any())
			if reflected.IsValid() && reflected.Kind() == reflect.String {
				value = reflected.String()
			}
		}

		if a.Key == "module" {
			if safeModule.MatchString(value) {
				e.Module = value
			}
			return true
		}
		if !safeValue.MatchString(value) {
			return true
		}
		switch a.Key {
		case "module":
			e.Module = value
		case "version":
			e.Version = value
		case "source":
			e.Source = value
		case "phase":
			e.Phase = value
		case "status":
			e.Status = value
		case "code":
			switch value {
			case "ready", "starting", "state_stale", "action_unknown", "work_completed", "error":
				e.Code = value
			}
		}
		return true
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(add)
	// Named enum aliases arrive as Any; do not stringify arbitrary objects.
	if e.Message == "module event" && (e.Module == "" || e.Version == "" || e.Generation == 0 || e.Code == "") {
		h.shared.mu.Lock()
		h.shared.dropped++
		h.shared.mu.Unlock()
		return nil
	}
	s := h.shared
	s.mu.Lock()
	now := s.now()
	if e.Message == "module event" {
		if s.window.IsZero() || now.Sub(s.window) >= time.Second {
			s.window = now
			s.count = 0
		}
		if s.count >= ModuleLogsPerSecond {
			s.dropped++
			s.mu.Unlock()
			return nil
		}
		s.count++
	}
	if len(s.logs) == MaxLogs {
		copy(s.logs, s.logs[1:])
		s.logs[len(s.logs)-1] = e
	} else {
		s.logs = append(s.logs, e)
	}
	s.mu.Unlock()
	raw, err := json.Marshal(e)
	if err != nil {
		return nil
	}
	raw = append(raw, '\n')
	if len(raw) > 1024 {
		s.mu.Lock()
		s.dropped++
		s.mu.Unlock()
		return nil
	}
	select {
	case s.queue <- raw:
	default:
		s.mu.Lock()
		s.dropped++
		s.mu.Unlock()
	}
	return nil
}
func (h *Handler) Snapshot() Snapshot {
	s := h.shared
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{append([]Event(nil), s.logs...), s.dropped, s.failed}
}

var _ io.Closer = fileSink{}
