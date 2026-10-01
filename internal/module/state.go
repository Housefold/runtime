package module

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/housefold/runtime/internal/ha"
	"github.com/housefold/runtime/internal/state"
)

const MaxResetBytes = 8 << 20
const MaxResetEntities = 10000

// State messages reuse canonical HA identity, timestamps and open JSON
// attributes. Unknown domains/attributes have exactly the same representation.
type ResetBoundary struct {
	state.Position
	Fresh bool `json:"fresh"`
}
type EventReader interface {
	Next(context.Context) (state.Event, error)
	Close()
}

func StreamState(ctx context.Context, s *Session, source *ha.StateSession) error {
	sub, err := source.Subscribe(ctx)
	if err != nil {
		return err
	}
	return StreamEvents(ctx, s, sub)
}
func StreamEvents(ctx context.Context, s *Session, sub EventReader) error {
	defer sub.Close()
	defer s.Close()
	for {
		event, err := sub.Next(ctx)
		if err != nil {
			return err
		}
		if err = s.SendEvent(ctx, event); err != nil {
			return err
		}
	}
}
func (s *Session) SendEvent(ctx context.Context, event state.Event) error {
	send := func(kind string, value any) error {
		f, err := FrameOf(kind, value)
		if err != nil {
			return err
		}
		return s.Send(ctx, f)
	}
	if event.Kind != state.Reset {
		return send("state_event", event)
	}
	if event.Snapshot == nil || len(event.Snapshot.States) > MaxResetEntities {
		return ErrProtocol
	}
	raw, err := json.Marshal(event)
	if err != nil || len(raw) > MaxResetBytes {
		return state.ErrInitialTooLarge
	}
	boundary := ResetBoundary{Position: event.Position, Fresh: event.Fresh}
	if err = send("reset_begin", boundary); err != nil {
		return err
	}
	keys := make([]string, 0, len(event.Snapshot.States))
	for id := range event.Snapshot.States {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		entity := event.Snapshot.States[id]
		if err = send("reset_entity", entity); err != nil {
			return err
		}
	}
	return send("reset_end", boundary)
}

// Replica atomically publishes complete resets. It is owned by a single reader;
// Snapshot returns a copy. A failed stream requires Disconnected and new reset.
type Replica struct {
	current state.Snapshot
	staging *state.Snapshot
	bytes   int
	ready   bool
}

func (r *Replica) Disconnected() { r.current.Fresh = false; r.staging = nil; r.ready = false }
func (r *Replica) Snapshot() state.Snapshot {
	raw, _ := json.Marshal(r.current)
	var copy state.Snapshot
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func (r *Replica) Apply(f Frame) error {
	fail := func() error { r.Disconnected(); return ErrProtocol }
	if len(f.Body) > MaxFrame || !validJSON(f.Body) {
		return fail()
	}
	switch f.Type {
	case "state_unavailable":
		r.Disconnected()
		return nil
	case "reset_begin":
		var b ResetBoundary
		if r.staging != nil || json.Unmarshal(f.Body, &b) != nil {
			return fail()
		}
		r.staging = &state.Snapshot{Generation: b.Generation, Revision: b.Revision, Fresh: b.Fresh, States: make(map[string]state.Entity)}
		r.bytes = len(f.Body)
		r.ready = false
	case "reset_entity":
		var e state.Entity
		if r.staging == nil || json.Unmarshal(f.Body, &e) != nil || e.EntityID == "" {
			return fail()
		}
		if _, ok := r.staging.States[e.EntityID]; ok {
			return fail()
		}
		r.bytes += len(f.Body)
		if r.bytes > MaxResetBytes || len(r.staging.States) >= MaxResetEntities {
			return fail()
		}
		r.staging.States[e.EntityID] = e
	case "reset_end":
		var b ResetBoundary
		if r.staging == nil || json.Unmarshal(f.Body, &b) != nil || b.Generation != r.staging.Generation || b.Revision != r.staging.Revision || b.Fresh != r.staging.Fresh {
			return fail()
		}
		r.current = *r.staging
		r.staging = nil
		r.ready = true
	case "state_event":
		var e state.Event
		if !r.ready || r.staging != nil || json.Unmarshal(f.Body, &e) != nil || e.Generation != r.current.Generation {
			return fail()
		}
		if e.Kind == state.Freshness {
			if e.Revision != r.current.Revision {
				return fail()
			}
			r.current.Fresh = e.Fresh
			return nil
		}
		if e.Revision != r.current.Revision+1 {
			return fail()
		}
		_, found := r.current.States[e.EntityID]
		switch e.Kind {
		case state.Add, state.Update:
			if e.Entity == nil || e.Entity.EntityID != e.EntityID || found != (e.Kind == state.Update) {
				return fail()
			}
			oldRaw, _ := json.Marshal(r.current.States[e.EntityID])
			newRaw, err := json.Marshal(e.Entity)
			nextBytes := r.bytes + len(newRaw)
			if found {
				nextBytes -= len(oldRaw)
			}
			if err != nil || nextBytes > MaxResetBytes || (!found && len(r.current.States) >= MaxResetEntities) {
				return fail()
			}
			r.bytes = nextBytes
			r.current.States[e.EntityID] = *e.Entity
		case state.Remove:
			if !found {
				return fail()
			}
			oldRaw, _ := json.Marshal(r.current.States[e.EntityID])
			r.bytes -= len(oldRaw)
			delete(r.current.States, e.EntityID)
		default:
			return fail()
		}
		r.current.Revision = e.Revision
		r.current.Fresh = e.Fresh
	default:
		return fail()
	}
	return nil
}
