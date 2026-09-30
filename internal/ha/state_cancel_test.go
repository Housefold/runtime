package ha

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/housefold/runtime/internal/state"
)

func TestSessionCancellationAtWireStages(t *testing.T) {
	for _, stage := range []string{"dial", "auth", "subscribe", "snapshot", "live_ping"} {
		t.Run(stage, func(t *testing.T) {
			reached := make(chan struct{})
			release := make(chan struct{})
			pongAttempted := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				block := func() { close(reached); <-release }
				if stage == "dial" {
					block()
					return
				}
				if stage == "live_ping" {
					w = &pongGateWriter{ResponseWriter: w, attempted: pongAttempted, release: release}
				}
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				_ = writeJSON(r.Context(), conn, map[string]any{"type": "auth_required"})
				_, _, err = conn.Read(r.Context())
				if err != nil {
					return
				}
				if stage == "auth" {
					block()
					return
				}
				_ = writeJSON(r.Context(), conn, map[string]any{"type": "auth_ok"})
				_, _, err = conn.Read(r.Context())
				if err != nil {
					return
				}
				if stage == "subscribe" {
					block()
					return
				}
				_ = writeJSON(r.Context(), conn, map[string]any{"id": 1, "type": "result", "success": true})
				_, _, err = conn.Read(r.Context())
				if err != nil {
					return
				}
				if stage == "snapshot" {
					block()
					return
				}
				_ = writeJSON(r.Context(), conn, map[string]any{"id": 2, "type": "result", "success": true, "result": []any{}})
				close(reached)
				// Reading dispatches control frames. Gate the pong write to put the
				// client in a deterministically pending ping before canceling it.
				_, _, _ = conn.Read(r.Context())
			}))
			defer server.Close()
			defer close(release)
			s := newStateSession("ws"+strings.TrimPrefix(server.URL, "http"), waitContext)
			s.pingInterval = time.Millisecond
			s.pongTimeout = time.Hour
			sub := subscribeTest(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { s.Run(ctx, "synthetic"); close(done) }()
			select {
			case <-reached:
			case <-time.After(3 * time.Second):
				t.Fatal("stage not reached")
			}
			if stage == "live_ping" {
				select {
				case <-pongAttempted:
				case <-time.After(3 * time.Second):
					t.Fatal("ping not observed")
				}
			}
			before := time.Now()
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("session cancellation stuck")
			}
			t.Logf("cancel-to-session-exit=%s", time.Since(before))
			if _, err := sub.Next(context.Background()); err != state.ErrShutdown {
				t.Fatal(err)
			}
			if s.Metadata().Fresh {
				t.Fatal("shutdown left state fresh")
			}
		})
	}
}

// A counted context deterministically cancels between decode/replay steps;
// no timing sleeps or production hooks are needed to prove partial candidates
// remain unpublished. It is used synchronously by these helpers only.
type stepCancelContext struct {
	context.Context
	remaining int
}

func (c *stepCancelContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}
func TestCancelCandidateDecodeAndReplay(t *testing.T) {
	raw := []json.RawMessage{syntheticState("a", 1, "one"), syntheticState("b", 1, "two"), syntheticState("c", 1, "three")}
	ctx := &stepCancelContext{Context: context.Background(), remaining: 3}
	if c, err := newStateCandidateContext(ctx, raw, maxStatePayloadBytes); err != context.Canceled || c != nil {
		t.Fatal("partial decoded candidate returned", err)
	}
	c, _ := newStateCandidate(nil)
	ctx = &stepCancelContext{Context: context.Background(), remaining: 2}
	events := []stateEvent{syntheticEvent("a", 2, "one", false), syntheticEvent("b", 3, "two", false)}
	if err := replayCandidate(ctx, c, events); err != context.Canceled || len(c.states) != 1 {
		t.Fatal("replay cancellation not observed", err)
	}
	s := newStateSession("ws://unused", waitContext)
	publishSynthetic(t, s, 1)
	before := s.Snapshot()
	s.markDisconnected(StatusUnavailable)
	if got := s.Snapshot(); got.Generation != before.Generation || got.States["sensor.a"].State != before.States["sensor.a"].State {
		t.Fatal("unpublished partial candidate changed view")
	}
}

type pongGateWriter struct {
	http.ResponseWriter
	attempted chan struct{}
	release   chan struct{}
}

func (w *pongGateWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	gate := &pongGateConn{Conn: conn, attempted: w.attempted, release: w.release}
	if err := rw.Writer.Flush(); err != nil {
		return nil, nil, err
	}
	return gate, bufio.NewReadWriter(rw.Reader, bufio.NewWriter(gate)), nil
}

type pongGateConn struct {
	net.Conn
	attempted chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (c *pongGateConn) Write(raw []byte) (int, error) {
	if len(raw) > 0 && raw[0] == 0x8a {
		c.once.Do(func() { close(c.attempted) })
		<-c.release
	}
	return c.Conn.Write(raw)
}
