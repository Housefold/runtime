package ha

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const maxStatePayloadBytes = 64 * 1024 * 1024
const maxStateTombstones = 4096
const maxStateTombstoneBytes = 8 * 1024 * 1024

// Phase describes the current state-session lifecycle.
type Phase string

const (
	PhaseDisconnected   Phase = "Disconnected"
	PhaseConnecting     Phase = "Connecting"
	PhaseAuthenticating Phase = "Authenticating"
	PhaseSubscribing    Phase = "Subscribing"
	PhaseSyncing        Phase = "Syncing"
	PhaseReady          Phase = "Ready"
)

// EntityState is the latest in-memory Home Assistant state for one entity.
// Context identifiers and prior-state history are intentionally omitted.
type EntityState struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged time.Time      `json:"last_changed"`
	LastUpdated time.Time      `json:"last_updated"`
}

// StateMetadata describes readiness without copying the entity map.
// Ready means the subscription/snapshot/replay sequence completed; it does
// not assert that HA provides an atomic snapshot/event barrier.
type StateMetadata struct {
	Phase              Phase      `json:"phase"`
	Status             Status     `json:"status"`
	Fresh              bool       `json:"fresh"`
	Generation         uint64     `json:"generation"`
	EntityCount        int        `json:"entity_count"`
	LastSuccessfulSync *time.Time `json:"last_successful_sync,omitempty"`
}

// StateSnapshot is an isolated copy of the current generation.
type StateSnapshot struct {
	Generation uint64                 `json:"generation"`
	Fresh      bool                   `json:"fresh"`
	States     map[string]EntityState `json:"states"`
}

type stateCandidate struct {
	states         map[string]EntityState
	watermarks     map[string]stateWatermark
	tombstones     map[string]time.Time
	tombstoneBytes int
	bytes          int
	maxBytes       int
}

type stateWatermark struct {
	lastUpdated time.Time
	timeFired   time.Time
}

var errStatePayloadTooLarge = errors.New("state payload exceeds configured bound")

func newStateCandidate(snapshot []json.RawMessage) (*stateCandidate, error) {
	return newStateCandidateWithLimit(snapshot, maxStatePayloadBytes)
}

func newStateCandidateWithLimit(snapshot []json.RawMessage, limit int) (*stateCandidate, error) {
	candidate := &stateCandidate{states: make(map[string]EntityState, len(snapshot)), watermarks: make(map[string]stateWatermark, len(snapshot)), tombstones: make(map[string]time.Time), bytes: 2, maxBytes: limit}
	for _, raw := range snapshot {
		state, err := decodeEntityState(raw)
		if err != nil {
			return nil, err
		}
		if _, exists := candidate.states[state.EntityID]; exists {
			return nil, fmt.Errorf("duplicate entity state")
		}
		if err := candidate.set(state.EntityID, state); err != nil {
			return nil, err
		}
		candidate.watermarks[state.EntityID] = stateWatermark{lastUpdated: state.LastUpdated}
	}
	return candidate, nil
}

func (c *stateCandidate) set(entityID string, state EntityState) error {
	entrySize, err := stateEntrySize(entityID, state)
	if err != nil {
		return err
	}
	oldSize, exists := 0, false
	if old, ok := c.states[entityID]; ok {
		exists = true
		oldSize, err = stateEntrySize(entityID, old)
		if err != nil {
			return err
		}
	}
	newSize := c.bytes - oldSize + entrySize
	if !exists && len(c.states) > 0 {
		newSize++
	}
	if newSize > c.maxBytes {
		return errStatePayloadTooLarge
	}
	c.states[entityID] = state
	if c.watermarks == nil {
		c.watermarks = make(map[string]stateWatermark)
	}
	watermark := c.watermarks[entityID]
	watermark.lastUpdated = state.LastUpdated
	c.watermarks[entityID] = watermark
	if deletedAt, deleted := c.tombstones[entityID]; deleted {
		c.tombstoneBytes -= tombstonePayloadSize(entityID, deletedAt)
		delete(c.tombstones, entityID)
	}
	c.bytes = newSize
	return nil
}

func tombstonePayloadSize(entityID string, deletedAt time.Time) int {
	key, _ := json.Marshal(entityID)
	value, _ := json.Marshal(deletedAt.UTC().Format(time.RFC3339Nano))
	return len(key) + len(value) + 24 // field names, separators, and braces
}

func (c *stateCandidate) remove(entityID string) {
	old, exists := c.states[entityID]
	if !exists {
		return
	}
	entrySize, err := stateEntrySize(entityID, old)
	if err != nil {
		return
	}
	c.bytes -= entrySize
	if len(c.states) > 1 {
		c.bytes--
	}
	delete(c.states, entityID)
}

func stateEntrySize(entityID string, state EntityState) (int, error) {
	key, err := json.Marshal(entityID)
	if err != nil {
		return 0, err
	}
	value, err := json.Marshal(state)
	if err != nil {
		return 0, err
	}
	return len(key) + 1 + len(value), nil
}

func stateMapSize(states map[string]EntityState) (int, error) {
	size := 2 // opening and closing braces
	for entityID, state := range states {
		entrySize, err := stateEntrySize(entityID, state)
		if err != nil {
			return 0, err
		}
		size += entrySize
	}
	if len(states) > 1 {
		size += len(states) - 1
	}
	return size, nil
}

func cloneStateMap(states map[string]EntityState) map[string]EntityState {
	clone := make(map[string]EntityState, len(states))
	for entityID, state := range states {
		clone[entityID] = cloneEntityState(state)
	}
	return clone
}

func cloneEntityState(state EntityState) EntityState {
	if state.Attributes != nil {
		state.Attributes = cloneValue(state.Attributes).(map[string]any)
	}
	return state
}

func cloneValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(value))
		for key, nested := range value {
			clone[key] = cloneValue(nested)
		}
		return clone
	case []any:
		clone := make([]any, len(value))
		for i, nested := range value {
			clone[i] = cloneValue(nested)
		}
		return clone
	default:
		return value
	}
}
