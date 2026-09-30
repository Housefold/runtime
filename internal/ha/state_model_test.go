package ha

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/housefold/runtime/internal/state"
)

// The oracle tracks only visible values; it does not use applyEvent, set,
// decodeEntityState, clone helpers or stateEntrySize to compute expectations.
func runStateModel(t *testing.T, operations []byte) {
	t.Helper()
	if len(operations) > 256 {
		operations = operations[:256]
	}
	s := newStateSession("ws://unused", waitContext)
	c, _ := newStateCandidate(nil)
	s.publish(c)
	sub := subscribeTest(t, s)
	nextTest(t, sub)
	values := map[string]string{}
	previous := map[string]stateEvent{}
	revision := uint64(0)
	for i, b := range operations {
		id := fmt.Sprintf("sensor.%d", (b/6)%8)
		tick := i + 10
		op := b % 6
		old, exists := values[id]
		visible := false
		var event stateEvent
		switch op {
		case 0, 1:
			value := fmt.Sprintf("v%d", tick)
			event = syntheticEvent(id, tick, value, false)
			values[id] = value
			visible = true
		case 2:
			event = syntheticEvent(id, tick, "", true)
			delete(values, id)
			visible = exists
		case 3:
			var ok bool
			event, ok = previous[id]
			if !ok {
				continue
			}
		case 4:
			last, ok := previous[id]
			if !ok {
				continue
			}
			fired, _ := time.Parse(time.RFC3339Nano, last.TimeFired)
			event = syntheticEvent(id, tick, "ignored", false)
			event.TimeFired = fired.Add(-time.Second).Format(time.RFC3339Nano)
		case 5:
			if !exists {
				continue
			}
			last, ok := previous[id]
			if !ok {
				continue
			}
			event = last
			event.Data.NewState = syntheticState(id, tick, "conflict")
			if s.applyLive(event) {
				t.Fatal("ambiguous equal time accepted")
			}
			if s.Metadata().Fresh {
				t.Fatal("failed live event left fresh state")
			}
			e := nextTest(t, sub)
			if e.Kind != state.Freshness || e.Revision != revision {
				t.Fatal(e)
			}
			if s.Read(id).Entity.State != old {
				t.Fatal("failed live event replaced state")
			}
			// Restart a complete generation from the independent visible oracle.
			raw := []json.RawMessage{}
			for id, v := range values {
				raw = append(raw, syntheticState(id, tick, v))
			}
			c, err := newStateCandidate(raw)
			if err != nil {
				t.Fatal(err)
			}
			s.publish(c)
			nextTest(t, sub)
			previous = map[string]stateEvent{}
			revision = 0
			continue
		}
		if !s.applyLive(event) {
			t.Fatalf("operation %d/%d failed", i, op)
		}
		if op <= 2 {
			previous[id] = event
		}
		if visible {
			revision++
			e := nextTest(t, sub)
			if e.Revision != revision || e.EntityID != id {
				t.Fatal("position lost", e)
			}
			want := state.Update
			if !exists {
				want = state.Add
			}
			if op == 2 {
				want = state.Remove
			}
			if e.Kind != want {
				t.Fatal("wrong delta", e, want)
			}
			if e.Entity != nil {
				if e.Entity.State != values[id] {
					t.Fatal("wrong delta value")
				}
				mutateAttributes(*e.Entity)
			}
		}
		snap := s.Snapshot()
		if snap.Revision != revision || !snap.Fresh || len(snap.States) != len(values) {
			t.Fatal("oracle metadata mismatch")
		}
		for id, v := range values {
			got, ok := snap.States[id]
			if !ok || got.State != v || got.Attributes["nested"].([]any)[0].(map[string]any)["value"] != v {
				t.Fatal("oracle state mismatch", id)
			}
		}
		raw, err := json.Marshal(snap.States)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) != s.stateBytes {
			t.Fatalf("payload accounting %d != %d", s.stateBytes, len(raw))
		}
		total := 0
		for id, deleted := range s.tombstones {
			raw, _ := json.Marshal(map[string]any{"entity_id": id, "deleted_at": deleted})
			total += len(raw)
		}
		if total != s.tombstoneBytes {
			t.Fatalf("tombstone accounting %d != %d", s.tombstoneBytes, total)
		}
		if sub.bytes != 0 || s.subscriberBytes != 0 {
			t.Fatal("subscriber oracle accounting mismatch")
		}
	}
}

func TestStateModelReconciliation(t *testing.T) {
	runStateModel(t, []byte{0, 3, 4, 5, 1, 2, 3, 4, 0, 12, 14, 12, 1, 2, 1, 5})
	ops := make([]byte, 256)
	for i := range ops {
		ops[i] = byte(i * 37)
	}
	runStateModel(t, ops)
}
func FuzzStateModel(f *testing.F) {
	f.Add([]byte{0, 3, 4, 5, 1, 2, 3, 4, 0, 12, 14, 12})
	f.Add([]byte{255, 0, 1, 2, 3, 4, 5})
	f.Fuzz(func(t *testing.T, data []byte) { runStateModel(t, data) })
}
func FuzzDecodeState(f *testing.F) {
	f.Add([]byte(syntheticState("sensor.a", 1, "seed")))
	f.Add([]byte(`{"attributes":null}`))
	f.Add([]byte(`{broken`))
	f.Add([]byte(append(syntheticState("sensor.a", 1, "seed"), []byte(` {}`)...)))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 32768 {
			return
		}
		entity, err := decodeEntityState(raw)
		if err != nil {
			return
		}
		if !json.Valid(raw) {
			t.Fatal("accepted invalid/trailing JSON")
		}
		canonical, err := json.Marshal(entity)
		if err != nil {
			t.Fatal(err)
		}
		round, err := decodeEntityState(canonical)
		if err != nil {
			t.Fatal(err)
		}
		if !sameState(entity, round) {
			t.Fatal("normalization changed state")
		}
		copy := cloneEntityState(entity)
		copy.Attributes["owned"] = "consumer"
		after, _ := json.Marshal(entity)
		if string(after) != string(canonical) {
			t.Fatal("ownership violation")
		}
	})
}

func TestExactPayloadAndInputBoundaries(t *testing.T) {
	raw := syntheticState("sensor.a", 1, "value")
	stateValue, err := decodeEntityState(raw)
	if err != nil {
		t.Fatal(err)
	}
	independent, _ := json.Marshal(map[string]EntityState{"sensor.a": stateValue})
	if _, err := newStateCandidateWithLimit([]json.RawMessage{raw}, len(independent)); err != nil {
		t.Fatal(err)
	}
	if _, err := newStateCandidateWithLimit([]json.RawMessage{raw}, len(independent)-1); err == nil {
		t.Fatal("cache boundary accepted")
	}
	if _, err := newStateCandidate([]json.RawMessage{raw, raw}); err == nil {
		t.Fatal("duplicate ID accepted")
	}
	for _, attrs := range []string{`null`, `[]`, `"text"`, strings.Repeat("[", 10001) + "0" + strings.Repeat("]", 10001)} {
		data := fmt.Sprintf(`{"entity_id":"a","state":"x","attributes":%s,"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:00Z"}`, attrs)
		if _, err := decodeEntityState([]byte(data)); err == nil {
			t.Fatal("invalid attributes accepted")
		}
	}
	for _, tail := range []string{` {}`, ` false`, ` garbage`} {
		if _, err := decodeEntityState(append(append([]byte{}, raw...), []byte(tail)...)); err == nil {
			t.Fatal("trailing data accepted")
		}
	}
	if _, size, ok := appendBuffered(nil, maxBufferedEventBytes-1, 1, stateEvent{}); !ok || size != maxBufferedEventBytes {
		t.Fatal("exact event bytes rejected")
	}
	if _, _, ok := appendBuffered(make([]stateEvent, maxBufferedEvents-1), 0, 1, stateEvent{}); !ok {
		t.Fatal("exact event count rejected")
	}
	tomb := syntheticEvent("removed", 2, "", true)
	c, _ := newStateCandidate(nil)
	fired, _ := time.Parse(time.RFC3339Nano, tomb.TimeFired)
	record, _ := json.Marshal(map[string]any{"entity_id": "removed", "deleted_at": fired})
	c.tombstoneBytes = maxStateTombstoneBytes - len(record)
	if err := applyEvent(c, tomb); err != nil || c.tombstoneBytes != maxStateTombstoneBytes {
		t.Fatal("exact tombstone bound", err)
	}
	c, _ = newStateCandidate(nil)
	c.tombstoneBytes = maxStateTombstoneBytes - len(record) + 1
	if err := applyEvent(c, tomb); err == nil || len(c.tombstones) != 0 {
		t.Fatal("tombstone overflow mutated candidate")
	}
	s := newStateSession("ws://unused", waitContext)
	publishSynthetic(t, s, 1)
	initial := state.Event{Position: s.positionLocked(), Kind: state.Reset, Fresh: true, Snapshot: &StateSnapshot{Generation: 1, Fresh: true, States: s.states}}
	canonical, _ := json.Marshal(initial)
	sub := subscribeTest(t, s)
	if sub.bytes != len(canonical) || s.subscriberBytes != len(canonical) {
		t.Fatal("initial envelope not accounted")
	}
}

func TestFailedWireCandidatesRetainPublishedState(t *testing.T) {
	for _, result := range []string{`null`, `[{}]`, string(append(append([]byte{'['}, syntheticState("sensor.a", 1, "a")...), ']'))} {
		t.Run(result[:min(8, len(result))], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				if !serveAuthenticated(t, conn, r.Context()) {
					return
				}
				// An ambiguous buffered event fails even an otherwise valid snapshot.
				_ = writeJSON(r.Context(), conn, map[string]any{"id": 1, "type": "event", "event": syntheticEvent("sensor.a", 1, "conflict", false)})
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"id":2,"type":"result","success":true,"result":`+result+`}`))
			}))
			defer server.Close()
			s := newStateSession("ws"+strings.TrimPrefix(server.URL, "http"), waitContext)
			publishSynthetic(t, s, 1)
			s.markDisconnected(StatusUnavailable)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			status := s.runConnection(ctx, "synthetic")
			if status != StatusUnavailable || s.Metadata().Generation != 1 || s.Snapshot().States["sensor.a"].State != "base" || s.Metadata().Fresh {
				t.Fatal("failed candidate replaced publication")
			}
		})
	}
}
