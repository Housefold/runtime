package ha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/housefold/runtime/internal/state"
	"sync"
	"testing"
	"time"
)

func sourceEntity(id, value string) state.Entity {
	return state.Entity{EntityID: id, State: value, Attributes: map[string]any{"unknown": []any{map[string]any{"number": json.Number("9007199254740993")}}}, LastChanged: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), LastUpdated: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func commitSource(t *testing.T, s *Sources, source Source, epoch string, entities ...state.Entity) *SourceWriter {
	t.Helper()
	w, err := s.Begin(source, epoch, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Stage(entities); err != nil {
		t.Fatal(err)
	}
	if err = w.Complete(epoch, 10); err != nil {
		t.Fatal(err)
	}
	if err = w.Commit(); err != nil {
		t.Fatal(err)
	}
	return w
}
func TestSourceCandidateAtomicFencingAndOwnership(t *testing.T) {
	s := NewSources()
	defer s.Close()
	native := commitSource(t, s, NativeSource, "native-1", sourceEntity("sensor.synthetic", "native"))
	sub, err := s.View().Subscribe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	sub.Next(context.Background())
	bridge, err := s.Begin(BridgeSource, "bridge-1", 20)
	if err != nil {
		t.Fatal(err)
	}
	entity := sourceEntity("sensor.synthetic", "bridge")
	bridge.Stage([]state.Entity{entity})
	entity.Attributes["unknown"].([]any)[0].(map[string]any)["number"] = "changed"
	if s.View().Snapshot().States[entity.EntityID].State != "native" || bridge.Commit() != ErrSourceInvalid {
		t.Fatal("incomplete candidate published")
	}
	delta := SourceDelta{Epoch: "bridge-1", Sequence: 21, Change: state.Event{Kind: state.Update, EntityID: entity.EntityID, Entity: ptrEntity(sourceEntity(entity.EntityID, "replayed"))}}
	if err = bridge.Buffer(delta); err != nil {
		t.Fatal(err)
	}
	bridge.Complete("bridge-1", 20)
	bridge.Commit()
	reset, err := sub.Next(context.Background())
	if err != nil || reset.Kind != state.Reset || reset.Revision != 0 || reset.Generation != 2 || reset.Snapshot.States[entity.EntityID].State != "replayed" {
		t.Fatal(reset, err)
	}
	if err = native.Apply(SourceDelta{Epoch: "native-1", Sequence: 11, Change: delta.Change}); err != ErrSourceFenced {
		t.Fatal("late writer", err)
	}
	snapshot := s.View().Snapshot()
	snapshot.States[entity.EntityID].Attributes["unknown"].([]any)[0].(map[string]any)["number"] = "mutated"
	if s.View().Snapshot().States[entity.EntityID].Attributes["unknown"].([]any)[0].(map[string]any)["number"] != json.Number("9007199254740993") {
		t.Fatal("ownership or precision")
	}
}
func ptrEntity(e state.Entity) *state.Entity { return &e }
func TestCandidateFailureLeavesNativeHealthy(t *testing.T) {
	for _, fault := range []string{"gap", "epoch", "duplicate", "barrier", "count"} {
		s := NewSources()
		native := commitSource(t, s, NativeSource, "native", sourceEntity("sensor.synthetic", "healthy"))
		bridge, _ := s.Begin(BridgeSource, "bridge", 0)
		switch fault {
		case "gap":
			bridge.Buffer(SourceDelta{Epoch: "bridge", Sequence: 2, Change: state.Event{Kind: state.Remove, EntityID: "sensor.synthetic"}})
		case "epoch":
			bridge.Complete("other", 0)
		case "duplicate":
			entity := sourceEntity("sensor.fixture", "x")
			bridge.Stage([]state.Entity{entity, entity})
		case "barrier":
			bridge.Complete("bridge", 1)
		case "count":
			for i := 0; i <= MaxSourceEntities; i++ {
				if bridge.Stage([]state.Entity{sourceEntity(fmt.Sprintf("sensor.fixture_%d", i), "x")}) != nil {
					break
				}
			}
		}
		if bridge.Commit() == nil || !s.View().Snapshot().Fresh || s.Selected() != NativeSource {
			t.Fatal(fault)
		}
		if err := native.Apply(SourceDelta{Epoch: "native", Sequence: 11, Change: state.Event{Kind: state.Update, EntityID: "sensor.synthetic", Entity: ptrEntity(sourceEntity("sensor.synthetic", "updated"))}}); err != nil {
			t.Fatal(fault, err)
		}
		s.Close()
	}
}
func TestSelectedFailureFullFallbackAndReentry(t *testing.T) {
	s := NewSources()
	defer s.Close()
	bridge := commitSource(t, s, BridgeSource, "bridge", sourceEntity("sensor.synthetic", "bridge"))
	if err := bridge.Apply(SourceDelta{Epoch: "bridge", Sequence: 12, Change: state.Event{Kind: state.Remove, EntityID: "sensor.synthetic"}}); err != ErrSourceInvalid {
		t.Fatal(err)
	}
	if s.View().Snapshot().Fresh {
		t.Fatal("gap retained freshness")
	}
	calls := 0
	err := s.Fallback(context.Background(), bridge, func(context.Context) (state.Snapshot, error) {
		calls++
		return state.Snapshot{Generation: 3, Revision: 4, Fresh: true, States: map[string]state.Entity{"sensor.native_only": sourceEntity("sensor.native_only", "native")}}, nil
	})
	if err != nil || calls != 1 || s.Selected() != NativeSource {
		t.Fatal(err, calls)
	}
	snapshot := s.View().Snapshot()
	if snapshot.Generation != 2 || snapshot.Revision != 0 || len(snapshot.States) != 1 || !snapshot.Fresh {
		t.Fatal(snapshot)
	}
	native := s.ActiveWriter()
	native.Apply(SourceDelta{Epoch: "native/3", Sequence: 5, Change: state.Event{Kind: state.Update, EntityID: "sensor.native_only", Entity: ptrEntity(sourceEntity("sensor.native_only", "live"))}})
	candidate := commitSource(t, s, BridgeSource, "new-bridge", sourceEntity("sensor.bridge_only", "reentry"))
	if candidate == nil || native.Apply(SourceDelta{}) != ErrSourceFenced || s.View().Snapshot().Generation != 3 {
		t.Fatal("reentry fencing")
	}
}
func TestSourceCutoverRaceAndFallbackRace(t *testing.T) {
	s := NewSources()
	defer s.Close()
	native := commitSource(t, s, NativeSource, "native", sourceEntity("sensor.synthetic", "native"))
	bridge, _ := s.Begin(BridgeSource, "bridge", 0)
	bridge.Stage([]state.Entity{sourceEntity("sensor.synthetic", "bridge")})
	bridge.Complete("bridge", 0)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		native.Apply(SourceDelta{Epoch: "native", Sequence: 11, Change: state.Event{Kind: state.Update, EntityID: "sensor.synthetic", Entity: ptrEntity(sourceEntity("sensor.synthetic", "late native"))}})
	}()
	go func() {
		defer wg.Done()
		<-start
		if err := bridge.Commit(); err != nil {
			t.Error(err)
		}
	}()
	close(start)
	wg.Wait()
	if s.View().Snapshot().States["sensor.synthetic"].State != "bridge" {
		t.Fatal("mixed sources")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Fallback(context.Background(), bridge, func(context.Context) (state.Snapshot, error) {
			close(entered)
			<-release
			return state.Snapshot{Generation: 1, Fresh: true, States: map[string]state.Entity{}}, nil
		})
	}()
	<-entered
	commitSource(t, s, BridgeSource, "reentry", sourceEntity("sensor.new", "healthy"))
	close(release)
	if err := <-done; !errors.Is(err, ErrSourceFenced) {
		t.Fatal("old fallback replaced reentry", err)
	}
	if s.Selected() != BridgeSource {
		t.Fatal("lost preferred source")
	}
}
func TestSourceBufferBoundAndPostSealUpdate(t *testing.T) {
	s := NewSources()
	defer s.Close()
	w, _ := s.Begin(BridgeSource, "bridge", 0)
	w.Stage([]state.Entity{sourceEntity("sensor.synthetic", "x")})
	for i := uint64(1); i <= MaxSourceEvents+1; i++ {
		err := w.Buffer(SourceDelta{Epoch: "bridge", Sequence: i, Change: state.Event{Kind: state.Update, EntityID: "sensor.synthetic", Entity: ptrEntity(sourceEntity("sensor.synthetic", "x"))}})
		if i <= MaxSourceEvents && err != nil || i > MaxSourceEvents && err == nil {
			t.Fatal(i, err)
		}
	}
	w, _ = s.Begin(BridgeSource, "new", 0)
	w.Stage([]state.Entity{sourceEntity("sensor.synthetic", "x")})
	w.Complete("new", 0)
	if err := w.Buffer(SourceDelta{Epoch: "new", Sequence: 1, Change: state.Event{Kind: state.Update, EntityID: "sensor.synthetic", Entity: ptrEntity(sourceEntity("sensor.synthetic", "after seal"))}}); err != nil {
		t.Fatal(err)
	}
	w.Commit()
	if s.View().Snapshot().States["sensor.synthetic"].State != "after seal" {
		t.Fatal("lost post-seal delta")
	}
}

func TestSourceSequenceDoesNotInventVisibleChangesOrWrap(t *testing.T) {
	s := NewSources()
	defer s.Close()
	e := sourceEntity("sensor.synthetic", "same")
	w := commitSource(t, s, NativeSource, "native", e)
	if err := w.Apply(SourceDelta{Epoch: "native", Sequence: 11, Change: state.Event{Kind: state.Update, EntityID: e.EntityID, Entity: &e}}); err != nil {
		t.Fatal(err)
	}
	if s.View().Snapshot().Revision != 0 {
		t.Fatal("invisible update changed canonical revision")
	}
	e.State = "changed"
	if err := w.Apply(SourceDelta{Epoch: "native", Sequence: 12, Change: state.Event{Kind: state.Update, EntityID: e.EntityID, Entity: &e}}); err != nil {
		t.Fatal(err)
	}
	if s.View().Snapshot().Revision != 1 {
		t.Fatal("visible update not counted")
	}
	if _, err := s.Begin(BridgeSource, "overflow", ^uint64(0)); err == nil {
		t.Fatal("wrapped barrier allowed")
	}
	if err := w.Apply(SourceDelta{Epoch: "native", Sequence: 0, Change: state.Event{Kind: state.Remove, EntityID: e.EntityID}}); err == nil || s.View().Snapshot().Fresh {
		t.Fatal("zero sequence allowed")
	}
}
