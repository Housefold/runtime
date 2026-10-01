package module

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/housefold/runtime/internal/state"
	"testing"
)

func apply(t *testing.T, r *Replica, k string, v any) {
	t.Helper()
	f, _ := FrameOf(k, v)
	if err := r.Apply(f); err != nil {
		t.Fatal(k, err)
	}
}
func TestStateResetAtomicAndOwnership(t *testing.T) {
	s, p := pair(t)
	ctx := context.Background()
	snapshot := state.Snapshot{Generation: 1, Fresh: true, States: map[string]state.Entity{"unknown.synthetic": {EntityID: "unknown.synthetic", State: "open", Attributes: map[string]any{"nested": []any{map[string]any{"x": "original"}}}}}}
	event := state.Event{Position: state.Position{Generation: 1}, Kind: state.Reset, Fresh: true, Snapshot: &snapshot}
	done := make(chan error, 1)
	go func() { done <- s.SendEvent(ctx, event) }()
	var r Replica
	for i := 0; i < 3; i++ {
		f, err := p.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = r.Apply(f); err != nil {
			t.Fatal(err)
		}
		if i < 2 && r.Snapshot().Generation != 0 {
			t.Fatal("partial reset exposed")
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	copy := r.Snapshot()
	copy.States["unknown.synthetic"].Attributes["nested"].([]any)[0].(map[string]any)["x"] = "mutated"
	if r.Snapshot().States["unknown.synthetic"].Attributes["nested"].([]any)[0].(map[string]any)["x"] != "original" {
		t.Fatal("ownership")
	}
	r.Disconnected()
	if r.Snapshot().Fresh {
		t.Fatal("disconnect fresh")
	}
	f, _ := FrameOf("state_event", state.Event{Position: state.Position{Generation: 1, Revision: 1}, Kind: state.Remove, EntityID: "unknown.synthetic"})
	if r.Apply(f) == nil {
		t.Fatal("delta after disconnect")
	}
}
func TestStateOrderingModel(t *testing.T) {
	var r Replica
	apply(t, &r, "reset_begin", ResetBoundary{Position: state.Position{Generation: 2}, Fresh: true})
	apply(t, &r, "reset_end", ResetBoundary{Position: state.Position{Generation: 2}, Fresh: true})
	for i := uint64(1); i <= 100; i++ {
		kind := state.Add
		if i%2 == 0 {
			kind = state.Remove
		}
		e := state.Entity{EntityID: "new.synthetic", Attributes: map[string]any{}}
		v := state.Event{Position: state.Position{Generation: 2, Revision: i}, Kind: kind, Fresh: true, EntityID: e.EntityID}
		if kind == state.Add {
			v.Entity = &e
		}
		apply(t, &r, "state_event", v)
		if len(r.Snapshot().States) != int(i%2) {
			t.Fatal(i)
		}
	}
	apply(t, &r, "state_event", state.Event{Position: state.Position{Generation: 2, Revision: 100}, Kind: state.Freshness, Fresh: false})
	if r.Snapshot().Fresh {
		t.Fatal("stale")
	}
	f, _ := FrameOf("state_event", state.Event{Position: state.Position{Generation: 2, Revision: 102}, Kind: state.Remove})
	if r.Apply(f) == nil {
		t.Fatal("gap accepted")
	}
}
func TestStateResetFailureRetainsPublished(t *testing.T) {
	var r Replica
	apply(t, &r, "reset_begin", ResetBoundary{Position: state.Position{Generation: 1}})
	apply(t, &r, "reset_end", ResetBoundary{Position: state.Position{Generation: 1}})
	apply(t, &r, "reset_begin", ResetBoundary{Position: state.Position{Generation: 2}})
	apply(t, &r, "reset_entity", state.Entity{EntityID: "synthetic"})
	f, _ := FrameOf("reset_entity", state.Entity{EntityID: "synthetic"})
	if r.Apply(f) == nil || r.Snapshot().Generation != 1 {
		t.Fatal("failed candidate published")
	}
	r.staging = &state.Snapshot{States: map[string]state.Entity{}}
	r.bytes = MaxResetBytes
	f.Body = json.RawMessage(`{"entity_id":"x"}`)
	if r.Apply(f) == nil {
		t.Fatal("byte bound")
	}
}

type endedReader struct {
	err    error
	closed bool
}

func (e *endedReader) Next(context.Context) (state.Event, error) { return state.Event{}, e.err }
func (e *endedReader) Close()                                    { e.closed = true }
func TestStreamTerminal(t *testing.T) {
	s, _ := pair(t)
	r := &endedReader{err: state.ErrOverflow}
	if err := StreamEvents(context.Background(), s, r); !errors.Is(err, state.ErrOverflow) || !r.closed {
		t.Fatal(err)
	}
}

func TestUnavailableDuringPartialResetPreservesStaleReplica(t *testing.T) {
	replica := &Replica{}
	frame, _ := FrameOf("reset_begin", ResetBoundary{Position: state.Position{Generation: 1}, Fresh: true})
	if err := replica.Apply(frame); err != nil {
		t.Fatal(err)
	}
	frame, _ = FrameOf("reset_entity", state.Entity{EntityID: "sensor.synthetic", State: "retained"})
	if err := replica.Apply(frame); err != nil {
		t.Fatal(err)
	}
	frame, _ = FrameOf("reset_end", ResetBoundary{Position: state.Position{Generation: 1}, Fresh: true})
	if err := replica.Apply(frame); err != nil {
		t.Fatal(err)
	}
	frame, _ = FrameOf("reset_begin", ResetBoundary{Position: state.Position{Generation: 2}, Fresh: true})
	if err := replica.Apply(frame); err != nil {
		t.Fatal(err)
	}
	frame, _ = FrameOf("state_unavailable", map[string]string{"reason": "resync_required"})
	if err := replica.Apply(frame); err != nil {
		t.Fatal(err)
	}
	snapshot := replica.Snapshot()
	if snapshot.Generation != 1 || snapshot.Fresh || snapshot.States["sensor.synthetic"].State != "retained" || replica.staging != nil {
		t.Fatal("partial or fresh unavailable replica", snapshot)
	}
}
