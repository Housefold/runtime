// Package module implements private Runtime-owned module IPC. It has no listener.
package module

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"
)

const Major = 1
const MaxFrame = 1 << 20
const MaxDepth = 32
const IOTimeout = 5 * time.Second

var ErrProtocol = errors.New("invalid module protocol")

type Identity struct {
	Module     string `json:"module"`
	Version    string `json:"version"`
	Boot       string `json:"boot"`
	Generation uint64 `json:"generation"`
}
type Hello struct {
	Major        int      `json:"major"`
	Minor        int      `json:"minor"`
	Capabilities []string `json:"capabilities"`
	Required     []string `json:"required"`
}
type Frame struct {
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"`
	Body json.RawMessage `json:"body"`
}

// Session identity is supplied by the launcher, never inferred from peer data.
// There is one bounded synchronous writer and reader; no unbounded send queue.
type Session struct {
	conn        net.Conn
	identity    Identity
	read, write chan struct{}
}

func NewSession(conn net.Conn, identity Identity) *Session {
	s := &Session{conn: conn, identity: identity, read: make(chan struct{}, 1), write: make(chan struct{}, 1)}
	s.read <- struct{}{}
	s.write <- struct{}{}
	return s
}
func (s *Session) Identity() Identity { return s.identity }
func (s *Session) Close() error       { return s.conn.Close() }
func acquire(ctx context.Context, gate chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gate:
	}
	if ctx.Err() != nil {
		gate <- struct{}{}
		return ctx.Err()
	}
	return nil
}
func deadline(ctx context.Context) time.Time {
	d := time.Now().Add(IOTimeout)
	if v, ok := ctx.Deadline(); ok && v.Before(d) {
		d = v
	}
	return d
}

// IO cancellation interrupts only its direction and its deadline is reset after
// the callback has joined, preventing an old cancellation poisoning the next IO.
func (s *Session) operation(ctx context.Context, read bool, f func() error) error {
	gate := s.write
	set := s.conn.SetWriteDeadline
	if read {
		gate = s.read
		set = s.conn.SetReadDeadline
	}
	if err := acquire(ctx, gate); err != nil {
		return err
	}
	defer func() { gate <- struct{}{} }()
	if err := set(deadline(ctx)); err != nil {
		return err
	}
	joined := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = set(time.Now()); close(joined) })
	err := f()
	if !stop() {
		<-joined
	}
	_ = set(time.Time{})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
func validJSON(raw []byte) bool {
	if !json.Valid(raw) {
		return false
	}
	depth := 0
	quoted, escape := false, false
	for _, b := range raw {
		if quoted {
			if escape {
				escape = false
			} else if b == '\\' {
				escape = true
			} else if b == '"' {
				quoted = false
			}
			continue
		}
		switch b {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > MaxDepth {
				return false
			}
		case '}', ']':
			depth--
		}
	}
	return true
}
func (s *Session) Receive(ctx context.Context) (Frame, error) {
	var frame Frame
	err := s.operation(ctx, true, func() error {
		var header [4]byte
		if _, err := io.ReadFull(s.conn, header[:]); err != nil {
			return err
		}
		n := binary.BigEndian.Uint32(header[:])
		if n == 0 || n > MaxFrame {
			return ErrProtocol
		}
		raw := make([]byte, n)
		if _, err := io.ReadFull(s.conn, raw); err != nil {
			return err
		}
		if !validJSON(raw) {
			return ErrProtocol
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&frame); err != nil || frame.Type == "" || len(frame.Type) > 64 || len(frame.ID) > 128 || !validJSON(frame.Body) {
			return ErrProtocol
		}
		return nil
	})
	if err != nil {
		_ = s.Close()
	}
	return frame, err
}
func (s *Session) Send(ctx context.Context, frame Frame) error {
	if frame.Type == "" || len(frame.Type) > 64 || len(frame.ID) > 128 {
		return ErrProtocol
	}
	raw, err := json.Marshal(frame)
	if err != nil || len(raw) > MaxFrame || !validJSON(raw) {
		return ErrProtocol
	}
	return s.operation(ctx, false, func() error {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
		for _, part := range [][]byte{header[:], raw} {
			for len(part) > 0 {
				n, err := s.conn.Write(part)
				if err != nil {
					_ = s.Close()
					return err
				}
				if n == 0 {
					_ = s.Close()
					return io.ErrNoProgress
				}
				part = part[n:]
			}
		}
		return nil
	})
}
func FrameOf(kind string, value any) (Frame, error) {
	raw, err := json.Marshal(value)
	return Frame{Type: kind, Body: raw}, err
}
func (s *Session) Negotiate(ctx context.Context, supported Hello) (Hello, error) {
	frame, err := s.Receive(ctx)
	if err != nil {
		return Hello{}, err
	}
	var peer Hello
	if frame.Type != "hello" || json.Unmarshal(frame.Body, &peer) != nil || peer.Major != Major || peer.Minor < 0 || len(peer.Capabilities) > 32 || len(peer.Required) > 32 {
		_ = s.Close()
		return Hello{}, ErrProtocol
	}
	has := func(list []string, v string) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	for _, v := range peer.Required {
		if len(v) > 64 || !has(supported.Capabilities, v) {
			_ = s.Close()
			return Hello{}, ErrProtocol
		}
	}
	for _, v := range supported.Required {
		if !has(peer.Capabilities, v) {
			_ = s.Close()
			return Hello{}, ErrProtocol
		}
	}
	negotiated := Hello{Major: Major, Minor: min(supported.Minor, peer.Minor)}
	for _, v := range peer.Capabilities {
		if len(v) > 64 {
			_ = s.Close()
			return Hello{}, ErrProtocol
		}
		if has(supported.Capabilities, v) && !has(negotiated.Capabilities, v) {
			negotiated.Capabilities = append(negotiated.Capabilities, v)
		}
	}
	reply, err := FrameOf("hello_ok", struct {
		Hello    Hello    `json:"hello"`
		Identity Identity `json:"identity"`
	}{negotiated, s.identity})
	if err != nil {
		return Hello{}, err
	}
	if err = s.Send(ctx, reply); err != nil {
		return Hello{}, err
	}
	return negotiated, nil
}
