package ha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/housefold/runtime/internal/state"
)

func syntheticState(id string, tick int, value string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"entity_id": id, "state": value,
		"attributes":   map[string]any{"nested": []any{map[string]any{"value": value}}},
		"last_changed": time.Unix(int64(tick), 0).UTC().Format(time.RFC3339Nano),
		"last_updated": time.Unix(int64(tick), 0).UTC().Format(time.RFC3339Nano)})
	return raw
}
func syntheticEvent(id string, tick int, value string, remove bool) stateEvent {
	event := stateEvent{EventType: "state_changed", TimeFired: time.Unix(int64(tick), 0).UTC().Format(time.RFC3339Nano)}
	event.Data.EntityID = id
	event.Data.NewState = syntheticState(id, tick, value)
	if remove {
		event.Data.NewState = json.RawMessage("null")
	}
	return event
}
func publishSynthetic(t testing.TB, s *StateSession, tick int) {
	t.Helper()
	c, err := newStateCandidate([]json.RawMessage{syntheticState("sensor.a", tick, "base")})
	if err != nil {
		t.Fatal(err)
	}
	s.publish(c)
}
func subscribeTest(t testing.TB, s *StateSession) *Subscription {
	t.Helper()
	sub, err := s.Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return sub
}
func nextTest(t testing.TB, sub *Subscription) state.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	e, err := sub.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func mutateAttributes(e EntityState) {
	e.Attributes["nested"].([]any)[0].(map[string]any)["value"] = "mutated"
}

func TestPrivateReadsAndOrderedIndependentSubscriptions(t *testing.T) {
	s := newStateSession("ws://unused", waitContext)
	if r := s.Read("missing"); r.Known || r.Found {
		t.Fatalf("unknown read: %+v", r)
	}
	a, b := subscribeTest(t, s), subscribeTest(t, s)
	for _, sub := range []*Subscription{a, b} {
		e := nextTest(t, sub)
		if e.Kind != state.Reset || e.Generation != 0 || e.Snapshot == nil || len(e.Snapshot.States) != 0 {
			t.Fatal(e)
		}
	}
	publishSynthetic(t, s, 1)
	resetA, resetB := nextTest(t, a), nextTest(t, b)
	mutateAttributes(resetA.Snapshot.States["sensor.a"])
	if resetB.Snapshot.States["sensor.a"].Attributes["nested"].([]any)[0].(map[string]any)["value"] != "base" {
		t.Fatal("reset shared")
	}
	r := s.Read("sensor.a")
	mutateAttributes(r.Entity)
	snap := s.Snapshot()
	mutateAttributes(snap.States["sensor.a"])
	if r = s.Read("sensor.a"); !r.Known || !r.Found || !r.Fresh || r.Revision != 0 || r.Entity.Attributes["nested"].([]any)[0].(map[string]any)["value"] != "base" {
		t.Fatal(r)
	}
	if r := s.Read("missing"); !r.Known || r.Found {
		t.Fatal(r)
	}
	for i, change := range []struct {
		id, value string
		remove    bool
		kind      state.Kind
	}{
		{"sensor.b", "added", false, state.Add}, {"sensor.a", "updated", false, state.Update}, {"sensor.b", "", true, state.Remove},
	} {
		event := syntheticEvent(change.id, i+2, change.value, change.remove)
		if !s.applyLive(event) {
			t.Fatal("live failed")
		}
		if !s.applyLive(event) {
			t.Fatal("duplicate failed")
		}
		ea, eb := nextTest(t, a), nextTest(t, b)
		if ea.Kind != change.kind || ea.Revision != uint64(i+1) || eb.Position != ea.Position {
			t.Fatal(ea, eb)
		}
		if ea.Entity != nil {
			mutateAttributes(*ea.Entity)
			if eb.Entity.Attributes["nested"].([]any)[0].(map[string]any)["value"] != change.value {
				t.Fatal("delta shared")
			}
		}
	}
	if !s.applyLive(syntheticEvent("sensor.a", 1, "old", false)) || s.Metadata().Revision != 3 {
		t.Fatal("old advanced revision")
	}
	s.markDisconnected(StatusUnavailable)
	s.markDisconnected(StatusUnavailable)
	for _, sub := range []*Subscription{a, b} {
		e := nextTest(t, sub)
		if e.Kind != state.Freshness || e.Fresh || e.Revision != 3 {
			t.Fatal(e)
		}
	}
	publishSynthetic(t, s, 10)
	for _, sub := range []*Subscription{a, b} {
		e := nextTest(t, sub)
		if e.Kind != state.Reset || e.Generation != 2 || e.Revision != 0 || len(e.Snapshot.States) != 1 {
			t.Fatal(e)
		}
	}
}

func TestPrivateSubscriptionAtomicRegistration(t *testing.T) {
	for trial := 0; trial < 100; trial++ {
		s := newStateSession("ws://unused", waitContext)
		publishSynthetic(t, s, 1)
		start := make(chan struct{})
		ready := make(chan *Subscription, 1)
		done := make(chan struct{})
		go func() {
			<-start
			sub, err := s.Subscribe(context.Background())
			if err != nil {
				panic(err)
			}
			ready <- sub
		}()
		go func() {
			defer close(done)
			<-start
			s.applyLive(syntheticEvent("sensor.a", 2, "next", false))
			s.markDisconnected(StatusUnavailable)
			publishSynthetic(t, s, 3)
		}()
		close(start)
		sub := <-ready
		<-done
		initial := nextTest(t, sub)
		// Reconstruct without reading the live map: registration can land at any
		// boundary, but all subsequent visible transitions must be contiguous.
		view := *initial.Snapshot
		for view.Generation != 2 {
			e := nextTest(t, sub)
			switch e.Kind {
			case state.Update:
				if e.Generation != view.Generation || e.Revision != view.Revision+1 {
					t.Fatal("lost update", e, view)
				}
				view.Revision = e.Revision
				view.States[e.EntityID] = *e.Entity
			case state.Freshness:
				if e.Position != (state.Position{Generation: view.Generation, Revision: view.Revision}) {
					t.Fatal("wrong freshness", e)
				}
				view.Fresh = e.Fresh
			case state.Reset:
				if e.Generation != view.Generation+1 || e.Revision != 0 {
					t.Fatal("wrong reset", e)
				}
				view = *e.Snapshot
			default:
				t.Fatal(e)
			}
		}
		if view.States["sensor.a"].State != "base" || !view.Fresh {
			t.Fatal(view)
		}
		sub.Close()
	}
}

func TestPrivateSubscriptionLimitsAndTerminals(t *testing.T) {
	t.Run("count overflow isolates slow consumer", func(t *testing.T) {
		s := newStateSession("ws://unused", waitContext)
		publishSynthetic(t, s, 1)
		slow, fast := subscribeTest(t, s), subscribeTest(t, s)
		nextTest(t, slow)
		nextTest(t, fast)
		for i := 0; i <= maxSubscriptionEvents; i++ {
			if !s.applyLive(syntheticEvent("sensor.a", i+2, fmt.Sprint(i), false)) {
				t.Fatal("live failed")
			}
			if e := nextTest(t, fast); e.Revision != uint64(i+1) {
				t.Fatal(e)
			}
		}
		if _, err := slow.Next(context.Background()); !errors.Is(err, state.ErrOverflow) {
			t.Fatal(err)
		}
		if s.subscriberBytes != 0 || len(s.subscribers) != 1 {
			t.Fatal("resources not released")
		}
	})
	t.Run("initial and reset bytes", func(t *testing.T) {
		s := newStateSession("ws://unused", waitContext)
		c, err := newStateCandidate([]json.RawMessage{syntheticState("sensor.a", 1, strings.Repeat("x", maxSubscriptionBytes/5))})
		if err != nil {
			t.Fatal(err)
		}
		s.publish(c)
		slow, fast := subscribeTest(t, s), subscribeTest(t, s)
		nextTest(t, fast)
		// The value occurs twice (state and nested attributes); each reset fits
		// alone, but three queued copies exceed eight MiB.
		s.publish(c)
		nextTest(t, fast)
		s.publish(c)
		nextTest(t, fast)
		if _, err := slow.Next(context.Background()); err != state.ErrOverflow {
			t.Fatal(err)
		}
		large, err := newStateCandidate([]json.RawMessage{syntheticState("sensor.a", 2, strings.Repeat("x", maxSubscriptionBytes))})
		if err != nil {
			t.Fatal(err)
		}
		s.publish(large)
		if _, err := s.Subscribe(context.Background()); err != state.ErrInitialTooLarge {
			t.Fatal(err)
		}
	})
	t.Run("capacity cancellation shutdown", func(t *testing.T) {
		s := newStateSession("ws://unused", waitContext)
		subs := make([]*Subscription, maxSubscribers)
		for i := range subs {
			subs[i] = subscribeTest(t, s)
		}
		if _, err := s.Subscribe(context.Background()); err != state.ErrCapacity {
			t.Fatal(err)
		}
		subs[0].Close()
		if _, err := subs[0].Next(context.Background()); err != state.ErrCanceled {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		sub, err := s.Subscribe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case <-sub.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("cancellation stuck")
		}
		if _, err := sub.Next(context.Background()); err != state.ErrCanceled {
			t.Fatal(err)
		}
		s.shutdownSubscriptions()
		for _, sub := range subs[1:] {
			if _, err := sub.Next(context.Background()); err != state.ErrShutdown {
				t.Fatal(err)
			}
		}
		if _, err := s.Subscribe(context.Background()); err != state.ErrShutdown {
			t.Fatal(err)
		}
		if s.subscriberBytes != 0 || len(s.subscribers) != 0 {
			t.Fatal("shutdown leaked")
		}
	})
}

func TestPrivateConcurrentOwnershipAndTermination(t *testing.T) {
	s := newStateSession("ws://unused", waitContext)
	publishSynthetic(t, s, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, err := s.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 3; i++ {
		wg.Go(func() {
			<-start
			for j := 0; j < 100; j++ {
				snap := s.Snapshot()
				for _, e := range snap.States {
					mutateAttributes(e)
				}
				r := s.Read("sensor.a")
				if r.Found {
					mutateAttributes(r.Entity)
				}
			}
		})
	}
	wg.Go(func() {
		<-start
		for j := 2; j < 102; j++ {
			s.applyLive(syntheticEvent("sensor.a", j, "next", false))
		}
	})
	wg.Go(func() {
		<-start
		for j := 0; j < 100; j++ {
			_, err := sub.Next(ctx)
			if err != nil {
				return
			}
		}
	})
	wg.Go(func() { <-start; cancel(); s.markDisconnected(StatusUnavailable); s.shutdownSubscriptions() })
	close(start)
	wg.Wait()
	if s.subscriberBytes != 0 || len(s.subscribers) != 0 {
		t.Fatal("terminal leak")
	}
}
