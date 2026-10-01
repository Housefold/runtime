package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/housefold/runtime/internal/ha"
	"github.com/housefold/runtime/internal/state"
)

const OrderedSyncTimeout = 30 * time.Second

// OrderedTransport is a dedicated, authenticated Core session. Implementations
// enforce frame bounds before allocation and context cancellation before return.
type OrderedTransport interface {
	Transport
	Next(context.Context, int) ([]byte, error)
	Close() error
}
type OrderedFrame struct {
	Kind     string         `json:"kind"`
	Epoch    string         `json:"epoch"`
	Sequence uint64         `json:"sequence"`
	Entities []state.Entity `json:"entities,omitempty"`
	Change   *state.Event   `json:"change,omitempty"`
}
type OrderedStream struct {
	peer     OrderedTransport
	sources  *ha.Sources
	writer   *ha.SourceWriter
	limits   Limits
	epoch    string
	sequence uint64
}

func orderedCompatible(cap Capability) bool {
	if cap.Name != "ordered_state" || cap.Version.Major != 1 || cap.Limits.Frame <= 0 || cap.Limits.Chunks <= 0 || cap.Limits.Total <= 0 {
		return false
	}
	barrier, sequence := false, false
	for _, v := range cap.Required {
		switch v {
		case "snapshot_barrier":
			barrier = true
		case "contiguous_sequence":
			sequence = true
		default:
			return false
		}
	}
	return barrier && sequence
}
func decodeOrdered(raw []byte, limit int) (OrderedFrame, error) {
	var envelope struct {
		Type  string          `json:"type"`
		ID    uint64          `json:"id"`
		Event json.RawMessage `json:"event"`
	}
	if !boundedJSON(raw, limit) || json.Unmarshal(raw, &envelope) != nil || envelope.Type != "event" || envelope.ID != 1 {
		return OrderedFrame{}, ErrProtocol
	}
	var f OrderedFrame
	decoder := json.NewDecoder(bytes.NewReader(envelope.Event))
	decoder.UseNumber()
	if decoder.Decode(&f) != nil || f.Epoch == "" || len(f.Epoch) > 128 {
		return f, ErrProtocol
	}
	return f, nil
}

// PrepareOrdered keeps the existing writer authoritative while consuming a
// complete bounded snapshot and contiguous buffered deltas, then commits once.
func PrepareOrdered(ctx context.Context, sources *ha.Sources, peer OrderedTransport, cap Capability) (stream *OrderedStream, err error) {
	if !orderedCompatible(cap) {
		return nil, ErrProtocol
	}
	ctx, cancel := context.WithTimeout(ctx, OrderedSyncTimeout)
	defer cancel()
	limits := Limits{Frame: min(cap.Limits.Frame, MaxFrame), Chunks: min(cap.Limits.Chunks, MaxChunks), Total: min(cap.Limits.Total, MaxTotal)}
	defer func() {
		if err != nil {
			_ = peer.Close()
		}
	}()
	request, _ := json.Marshal(Command{ID: 1, Type: "housefold/state/subscribe", Body: struct {
		Major  int    `json:"major"`
		Limits Limits `json:"limits"`
	}{1, limits}})
	raw, err := peer.Exchange(ctx, request, limits.Frame)
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var result Response
	if !boundedJSON(raw, limits.Frame) || json.Unmarshal(raw, &result) != nil || result.ID != 1 || result.Type != "result" || !result.Success {
		return nil, ErrProtocol
	}
	var begin OrderedFrame
	decoder := json.NewDecoder(bytes.NewReader(result.Result))
	decoder.UseNumber()
	if decoder.Decode(&begin) != nil || begin.Kind != "snapshot_begin" || begin.Epoch == "" || len(begin.Epoch) > 128 || len(begin.Entities) > 0 || begin.Change != nil {
		return nil, ErrProtocol
	}
	writer, err := sources.Begin(ha.BridgeSource, begin.Epoch, begin.Sequence)
	if err != nil {
		return nil, err
	}
	defer writer.Abort()
	total, chunks := len(raw), 0
	sequence := begin.Sequence
	for frames := 0; frames < ha.MaxSourceEvents+MaxChunks+1; frames++ {
		raw, err = peer.Next(ctx, limits.Frame)
		if err != nil {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		total += len(raw)
		if total > limits.Total {
			return nil, ErrProtocol
		}
		f, decodeErr := decodeOrdered(raw, limits.Frame)
		if decodeErr != nil || f.Epoch != begin.Epoch {
			return nil, ErrProtocol
		}
		switch f.Kind {
		case "snapshot_chunk":
			chunks++
			if chunks > limits.Chunks || f.Sequence != begin.Sequence || f.Change != nil {
				return nil, ErrProtocol
			}
			if err = writer.Stage(f.Entities); err != nil {
				return nil, err
			}
		case "delta":
			if f.Change == nil || len(f.Entities) > 0 {
				return nil, ErrProtocol
			}
			delta := ha.SourceDelta{Epoch: f.Epoch, Sequence: f.Sequence, Change: *f.Change}
			if err = writer.Buffer(delta); err != nil {
				return nil, err
			}
			sequence = f.Sequence
		case "snapshot_end":
			if len(f.Entities) > 0 || f.Change != nil {
				return nil, ErrProtocol
			}
			if err = writer.Complete(f.Epoch, f.Sequence); err != nil {
				return nil, err
			}
			if err = writer.Commit(); err != nil {
				return nil, err
			}
			return &OrderedStream{peer: peer, sources: sources, writer: writer, limits: limits, epoch: begin.Epoch, sequence: sequence}, nil
		default:
			return nil, ErrProtocol
		}
	}
	return nil, ErrProtocol
}
func (s *OrderedStream) next(ctx context.Context) error {
	raw, err := s.peer.Next(ctx, s.limits.Frame)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f, err := decodeOrdered(raw, s.limits.Frame)
	if err != nil {
		return err
	}
	if f.Epoch != s.epoch || len(f.Entities) > 0 {
		return ErrProtocol
	}
	if f.Kind == "heartbeat" {
		if f.Sequence != s.sequence || f.Change != nil {
			return ErrProtocol
		}
		return nil
	}
	if f.Kind != "delta" || f.Change == nil {
		return ErrProtocol
	}
	if err = s.writer.Apply(ha.SourceDelta{Epoch: f.Epoch, Sequence: f.Sequence, Change: *f.Change}); err != nil {
		return err
	}
	s.sequence = f.Sequence
	return nil
}

// Run fences a failed selected stream, marks retained state stale and reserves
// a complete fresh native resynchronization. It never forwards Bridge wire data
// to modules or merges native/Bridge streams. A stale stream cannot fence a newer
// active source. The returned failure remains visible even after healthy fallback.
func (s *OrderedStream) Run(ctx context.Context, resync ha.NativeResync) error {
	defer s.peer.Close()
	for {
		operation, cancel := context.WithTimeout(ctx, Timeout)
		err := s.next(operation)
		cancel()
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			_ = s.writer.Fail()
			return ctx.Err()
		}
		if errors.Is(err, ha.ErrSourceFenced) {
			return err
		}
		fallbackErr := s.sources.Fallback(ctx, s.writer, resync)
		if fallbackErr != nil {
			return errors.Join(err, fallbackErr)
		}
		return err
	}
}
func (s *OrderedStream) Close() error { _ = s.writer.Fail(); return s.peer.Close() }
