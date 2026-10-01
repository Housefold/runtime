package module

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func pair(t *testing.T) (*Session, *Session) {
	t.Helper()
	a, b := net.Pipe()
	s, p := NewSession(a, Identity{Module: "synthetic", Version: "1", Boot: "boot", Generation: 7}), NewSession(b, Identity{})
	t.Cleanup(func() { s.Close(); p.Close() })
	return s, p
}
func TestNegotiation(t *testing.T) {
	s, p := pair(t)
	done := make(chan error, 1)
	go func() {
		f, _ := FrameOf("hello", Hello{Major: 1, Minor: 2, Capabilities: []string{"state"}, Required: []string{"state"}})
		if err := p.Send(context.Background(), f); err != nil {
			done <- err
			return
		}
		f, err := p.Receive(context.Background())
		if err == nil && (!strings.Contains(string(f.Body), `"generation":7`) || f.Type != "hello_ok") {
			err = ErrProtocol
		}
		done <- err
	}()
	h, err := s.Negotiate(context.Background(), Hello{Major: 1, Minor: 1, Capabilities: []string{"state"}})
	if err != nil || h.Minor != 1 {
		t.Fatal(h, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestRejectedHello(t *testing.T) {
	for _, h := range []Hello{{Major: 2}, {Major: 1, Required: []string{"unknown"}}} {
		s, p := pair(t)
		go func() { f, _ := FrameOf("hello", h); _ = p.Send(context.Background(), f) }()
		if _, err := s.Negotiate(context.Background(), Hello{Major: 1}); !errors.Is(err, ErrProtocol) {
			t.Fatal(err)
		}
	}
}
func TestInvalidFrames(t *testing.T) {
	for _, raw := range []string{`{`, strings.Repeat("[", 33) + "0" + strings.Repeat("]", 33), `{"type":"x","body":{},"identity":"fake"}`, `{"type":"","body":{}}`} {
		s, p := pair(t)
		go func() {
			var h [4]byte
			binary.BigEndian.PutUint32(h[:], uint32(len(raw)))
			p.conn.Write(h[:])
			p.conn.Write([]byte(raw))
		}()
		if _, err := s.Receive(context.Background()); !errors.Is(err, ErrProtocol) {
			t.Fatal(err)
		}
	}
	s, p := pair(t)
	go func() { var h [4]byte; binary.BigEndian.PutUint32(h[:], MaxFrame+1); p.conn.Write(h[:]) }()
	if _, err := s.Receive(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
}
func TestCancellationAndDeadline(t *testing.T) {
	s, _ := pair(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Receive(ctx); done <- err }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s, _ = pair(t)
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	f, _ := FrameOf("x", json.RawMessage(`{}`))
	if err := s.Send(ctx, f); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestBoundedBlockedWrite(t *testing.T) {
	s, _ := pair(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	f, _ := FrameOf("x", struct{}{})
	go func() { done <- s.Send(ctx, f) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	f.Body = json.RawMessage(`"` + strings.Repeat("a", MaxFrame) + `"`)
	if err := s.Send(context.Background(), f); !errors.Is(err, ErrProtocol) {
		t.Fatal(err)
	}
}
