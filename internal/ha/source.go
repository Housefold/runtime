package ha

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/housefold/runtime/internal/state"
)

// Sources has one canonical publication owner. Native sessions and Bridge
// sessions feed independent candidates; neither writes directly into View.
const MaxSourceCandidates = 2
const MaxSourceEntities = 10000
const MaxSourceBytes = 8 << 20
const MaxSourceEvents = 256
const MaxSourceEventBytes = 8 << 20

type Source string

const (
	NativeSource Source = "native"
	BridgeSource Source = "bridge"
)

var ErrSourceFenced = errors.New("state source writer fenced")
var ErrSourceInvalid = errors.New("invalid or incomplete source candidate")

type SourceDelta struct {
	Epoch    string      `json:"epoch"`
	Sequence uint64      `json:"sequence"`
	Change   state.Event `json:"change"`
}
type sourceCandidate struct {
	source         Source
	remoteEpoch    string
	expected       uint64
	base, sequence uint64
	data           *stateCandidate
	buffered       []SourceDelta
	bytes          int
	complete       bool
}
type Sources struct {
	mu                  sync.Mutex
	view                *StateSession
	epoch, next, active uint64
	lost                uint64
	selected            Source
	remoteEpoch         string
	sequence            uint64
	pending             map[uint64]*sourceCandidate
	closed              bool
}

// Writer is an unforgeable local handle, bound to a candidate and admission epoch.
// Epoch/sequence payloads never identify or authorize a caller on their own.
type SourceWriter struct {
	owner *Sources
	token uint64
}

func NewSources() *Sources {
	view := NewStateSession(nil)
	view.externallyManaged = true
	return &Sources{view: view, pending: map[uint64]*sourceCandidate{}}
}
func (s *Sources) View() *StateSession { return s.view }
func (s *Sources) Selected() Source    { s.mu.Lock(); defer s.mu.Unlock(); return s.selected }
func (s *Sources) Begin(source Source, remoteEpoch string, barrier uint64) (*SourceWriter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.pending) >= MaxSourceCandidates || (source != NativeSource && source != BridgeSource) || remoteEpoch == "" || len(remoteEpoch) > 128 || barrier == math.MaxUint64 {
		return nil, ErrSourceInvalid
	}
	s.next++
	if s.next == 0 {
		return nil, ErrSourceInvalid
	}
	data := &stateCandidate{states: map[string]EntityState{}, watermarks: map[string]stateWatermark{}, tombstones: map[string]time.Time{}, bytes: 2, maxBytes: MaxSourceBytes}
	s.pending[s.next] = &sourceCandidate{source: source, remoteEpoch: remoteEpoch, expected: s.epoch, base: barrier, sequence: barrier, data: data}
	return &SourceWriter{owner: s, token: s.next}, nil
}
func (w *SourceWriter) pendingLocked() (*sourceCandidate, error) {
	c, ok := w.owner.pending[w.token]
	if !ok || c.expected != w.owner.epoch || w.owner.closed {
		return nil, ErrSourceFenced
	}
	return c, nil
}
func (w *SourceWriter) Stage(entities []state.Entity) error {
	s := w.owner
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := w.pendingLocked()
	if err != nil {
		return err
	}
	if c.complete {
		return ErrSourceInvalid
	}
	// Validate and copy each bounded entity before changing the candidate. A
	// staging failure invalidates only this candidate, never published state.
	for _, entity := range entities {
		raw, err := json.Marshal(entity)
		if err != nil || len(raw) > MaxSourceBytes {
			return w.rejectLocked()
		}
		owned, err := decodeEntityState(raw)
		if err != nil {
			return w.rejectLocked()
		}
		if _, exists := c.data.states[owned.EntityID]; exists || len(c.data.states) >= MaxSourceEntities {
			return w.rejectLocked()
		}
		if err = c.data.set(owned.EntityID, owned); err != nil {
			return w.rejectLocked()
		}
	}
	return nil
}
func (w *SourceWriter) rejectLocked() error {
	delete(w.owner.pending, w.token)
	return ErrSourceInvalid
}
func validDelta(delta SourceDelta) bool {
	return delta.Sequence != 0 && (delta.Change.Kind == state.Add || delta.Change.Kind == state.Update || delta.Change.Kind == state.Remove)
}
func (w *SourceWriter) Buffer(delta SourceDelta) error {
	s := w.owner
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := w.pendingLocked()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(delta)
	if err != nil || !validDelta(delta) || delta.Epoch != c.remoteEpoch || delta.Sequence != c.sequence+1 || len(c.buffered) >= MaxSourceEvents || c.bytes+len(raw) > MaxSourceEventBytes {
		return w.rejectLocked()
	}
	var owned SourceDelta
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&owned) != nil {
		return w.rejectLocked()
	}
	if c.complete {
		if orderedApply(c.data, owned.Change) != nil {
			return w.rejectLocked()
		}
		c.sequence = delta.Sequence
		return nil
	}
	c.buffered = append(c.buffered, owned)
	c.bytes += len(raw)
	c.sequence = delta.Sequence
	return nil
}
func orderedApply(c *stateCandidate, change state.Event) error {
	_, found := c.states[change.EntityID]
	if change.EntityID == "" {
		return ErrSourceInvalid
	}
	switch change.Kind {
	case state.Remove:
		if !found {
			return ErrSourceInvalid
		}
		c.remove(change.EntityID)
		delete(c.watermarks, change.EntityID)
	case state.Add, state.Update:
		if change.Entity == nil || change.Entity.EntityID != change.EntityID || found != (change.Kind == state.Update) || (!found && len(c.states) >= MaxSourceEntities) {
			return ErrSourceInvalid
		}
		raw, err := json.Marshal(change.Entity)
		if err != nil {
			return err
		}
		owned, err := decodeEntityState(raw)
		if err != nil {
			return err
		}
		return c.set(change.EntityID, owned)
	default:
		return ErrSourceInvalid
	}
	return nil
}
func (w *SourceWriter) Complete(epoch string, barrier uint64) error {
	s := w.owner
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := w.pendingLocked()
	if err != nil {
		return err
	}
	if c.complete || epoch != c.remoteEpoch || barrier != c.base {
		return w.rejectLocked()
	}
	for _, delta := range c.buffered {
		if err = orderedApply(c.data, delta.Change); err != nil {
			return w.rejectLocked()
		}
	}
	c.buffered = nil
	c.bytes = 0
	c.complete = true
	return nil
}
func (w *SourceWriter) Commit() error {
	s := w.owner
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := w.pendingLocked()
	if err != nil {
		return err
	}
	if !c.complete {
		return ErrSourceInvalid
	}
	// Source fencing and publication serialize with all live writers. Subscribers
	// receive a new local generation/revision zero complete reset, never a merge.
	s.epoch++
	s.active = w.token
	s.lost = 0
	s.selected = c.source
	s.remoteEpoch = c.remoteEpoch
	s.sequence = c.sequence
	s.pending = map[uint64]*sourceCandidate{}
	s.view.publish(c.data)
	return nil
}
func (w *SourceWriter) Apply(delta SourceDelta) error {
	s := w.owner
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.active != w.token {
		return ErrSourceFenced
	}
	if delta.Epoch != s.remoteEpoch || delta.Sequence != s.sequence+1 || !validDelta(delta) {
		s.loseLocked()
		return ErrSourceInvalid
	}
	view := s.view
	view.mu.Lock()
	old, existed := view.states[delta.Change.EntityID]
	c := &stateCandidate{states: view.states, watermarks: view.watermarks, tombstones: view.tombstones, bytes: view.stateBytes, maxBytes: MaxSourceBytes}
	if err := orderedApply(c, delta.Change); err != nil {
		view.mu.Unlock()
		s.loseLocked()
		return ErrSourceInvalid
	}
	view.stateBytes = c.bytes
	view.metadata.EntityCount = len(view.states)
	s.sequence = delta.Sequence
	current, exists := view.states[delta.Change.EntityID]
	if existed == exists && exists && sameState(old, current) {
		view.mu.Unlock()
		return nil
	}
	view.metadata.Revision++
	change := delta.Change
	change.Position = view.positionLocked()
	change.Fresh = true
	// The candidate decoder copied nested data; publish only Runtime-owned data.
	if change.Kind != state.Remove {
		entity := view.states[change.EntityID]
		change.Entity = &entity
	}
	view.broadcastLocked(change)
	view.mu.Unlock()
	view.signal()
	return nil
}
func (s *Sources) loseLocked() {
	s.lost = s.active
	s.active = 0
	s.selected = ""
	s.epoch++
	s.pending = map[uint64]*sourceCandidate{}
	s.view.markDisconnected(StatusUnavailable)
}
func (w *SourceWriter) Fail() error {
	s := w.owner
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == 0 && s.lost == w.token {
		return nil
	}
	if s.active != w.token {
		return ErrSourceFenced
	}
	s.loseLocked()
	return nil
}
func (w *SourceWriter) Abort() {
	s := w.owner
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, w.token)
}

// NativeResync must start a new native subscription/snapshot reconciliation,
// not return a cached view captured before selected-source continuity was lost.
type NativeResync func(context.Context) (state.Snapshot, error)

func (s *Sources) Fallback(ctx context.Context, failed *SourceWriter, resync NativeResync) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if resync == nil {
		return ErrSourceInvalid
	}
	if failed != nil {
		if err := failed.Fail(); err != nil && !errors.Is(err, ErrSourceInvalid) {
			return err
		}
	}
	// Reserve before IO: a later successful candidate fences this fallback.
	writer, err := s.Begin(NativeSource, "native-resync", 0)
	if err != nil {
		return err
	}
	defer writer.Abort()
	snapshot, err := resync(ctx)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !snapshot.Fresh || snapshot.Generation == 0 || len(snapshot.States) > MaxSourceEntities {
		return ErrSourceInvalid
	}
	remoteEpoch := fmt.Sprintf("native/%d", snapshot.Generation)
	s.mu.Lock()
	candidate, pendingErr := writer.pendingLocked()
	if pendingErr == nil {
		candidate.remoteEpoch = remoteEpoch
		candidate.base = snapshot.Revision
		candidate.sequence = snapshot.Revision
	}
	s.mu.Unlock()
	if pendingErr != nil {
		return pendingErr
	}
	for key, entity := range snapshot.States {
		if key != entity.EntityID {
			return ErrSourceInvalid
		}
		if err = writer.Stage([]state.Entity{entity}); err != nil {
			return err
		}
	}
	if err = writer.Complete(remoteEpoch, snapshot.Revision); err != nil {
		return err
	}
	return writer.Commit()
}
func (s *Sources) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.active = 0
	s.pending = nil
	s.view.shutdownSubscriptions()
}

func (s *Sources) ActiveWriter() *SourceWriter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == 0 {
		return nil
	}
	return &SourceWriter{owner: s, token: s.active}
}
