package ha

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/housefold/runtime/internal/state"
)

const (
	maxBufferedEventBytes = 8 * 1024 * 1024
	maxBufferedEvents     = 4096
	stateSyncTimeout      = 30 * time.Second
	statePingInterval     = 20 * time.Second
	statePongTimeout      = 10 * time.Second
)

type StateSession struct {
	mu              sync.RWMutex
	subscribers     map[*Subscription]struct{}
	subscriberBytes int
	closed          bool
	metadata        StateMetadata
	states          map[string]EntityState
	stateBytes      int
	watermarks      map[string]stateWatermark
	tombstones      map[string]time.Time
	tombstoneBytes  int
	changed         chan struct{}
	url             string
	wait            waitFunc
	syncTimeout     time.Duration
	pingInterval    time.Duration
	pongTimeout     time.Duration
	logger          *slog.Logger
}

func NewStateSession(logger *slog.Logger) *StateSession {
	return newStateSession(defaultWebSocketURL, waitContext, logger)
}

func newStateSession(url string, wait waitFunc, loggers ...*slog.Logger) *StateSession {
	var logger *slog.Logger
	if len(loggers) > 0 {
		logger = loggers[0]
	}
	return &StateSession{url: url, wait: wait, logger: logger, syncTimeout: stateSyncTimeout, pingInterval: statePingInterval, pongTimeout: statePongTimeout, changed: make(chan struct{}, 1), states: make(map[string]EntityState), watermarks: make(map[string]stateWatermark), tombstones: make(map[string]time.Time), metadata: StateMetadata{Phase: PhaseDisconnected, Status: StatusUnavailable}}
}

// Metadata returns lifecycle and freshness information without copying states.
func (s *StateSession) Metadata() StateMetadata {
	s.mu.RLock()
	defer s.mu.RUnlock()
	metadata := s.metadata
	if metadata.LastSuccessfulSync != nil {
		t := *metadata.LastSuccessfulSync
		metadata.LastSuccessfulSync = &t
	}
	return metadata
}

// Snapshot returns a deep copy of the latest published generation.
func (s *StateSession) Snapshot() StateSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return StateSnapshot{Generation: s.metadata.Generation, Revision: s.metadata.Revision, Fresh: s.metadata.Fresh, States: cloneStateMap(s.states)}
}

// Changes yields coalesced notifications for metadata or state changes.
func (s *StateSession) Changes() <-chan struct{} { return s.changed }

func (s *StateSession) signal() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *StateSession) setPhase(phase Phase) {
	s.mu.Lock()
	changed := s.metadata.Phase != phase
	s.metadata.Phase = phase
	s.mu.Unlock()
	if changed && s.logger != nil {
		s.logger.Info("Home Assistant state session phase changed", "phase", phase)
	}
	s.signal()
}

func (s *StateSession) setStatus(status Status) {
	s.mu.Lock()
	changed := s.metadata.Status != status
	s.metadata.Status = status
	s.mu.Unlock()
	if changed && s.logger != nil {
		s.logger.Info("Home Assistant state session status changed", "status", status)
	}
	s.signal()
}

// Run owns the connection lifecycle and retries failed sessions until ctx ends.
func (s *StateSession) Run(ctx context.Context, token string) {
	defer s.shutdownSubscriptions()
	delay := firstRetryDelay
	for ctx.Err() == nil {
		beforeGeneration := s.Metadata().Generation
		status := s.runConnection(ctx, token)
		if ctx.Err() != nil {
			s.markDisconnected(StatusUnavailable)
			return
		}
		if s.Metadata().Generation > beforeGeneration {
			delay = firstRetryDelay
		}
		s.markDisconnected(status)
		if !s.wait(ctx, delay) {
			return
		}
		delay *= 2
		if delay > maxRetryDelay {
			delay = maxRetryDelay
		}
	}
}

func (s *StateSession) runConnection(parent context.Context, token string) Status {
	if strings.TrimSpace(token) == "" {
		return StatusUnavailable
	}
	syncCtx, cancel := context.WithTimeout(parent, s.syncTimeout)
	defer cancel()
	s.setPhase(PhaseConnecting)
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	conn, response, err := websocket.Dial(syncCtx, s.url, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		if response != nil && (response.StatusCode == 401 || response.StatusCode == 403) {
			return StatusDenied
		}
		return StatusUnavailable
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxStatePayloadBytes + maxBufferedEventBytes + 1024)
	_, raw, err := conn.Read(syncCtx)
	if err != nil {
		return StatusUnavailable
	}
	var greeting struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &greeting) != nil || greeting.Type != "auth_required" {
		return StatusUnavailable
	}
	s.setPhase(PhaseAuthenticating)
	if err := writeJSON(syncCtx, conn, map[string]any{"type": "auth", "access_token": token}); err != nil {
		return StatusUnavailable
	}
	_, raw, err = conn.Read(syncCtx)
	if err != nil {
		return StatusUnavailable
	}
	var auth struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &auth) != nil {
		return StatusUnavailable
	}
	if auth.Type == "auth_invalid" {
		return StatusDenied
	}
	if auth.Type != "auth_ok" {
		return StatusUnavailable
	}
	s.setStatus(StatusConnected)
	s.setPhase(PhaseSubscribing)
	if err := writeJSON(syncCtx, conn, map[string]any{"id": 1, "type": "subscribe_events", "event_type": "state_changed"}); err != nil {
		return StatusUnavailable
	}
	buffer, bufferedBytes, status := s.awaitSubscription(syncCtx, conn)
	if status != StatusConnected {
		return status
	}
	s.setPhase(PhaseSyncing)
	if err := writeJSON(syncCtx, conn, map[string]any{"id": 2, "type": "get_states"}); err != nil {
		return StatusUnavailable
	}
	candidate, buffered, status := s.awaitSnapshot(syncCtx, conn, buffer, bufferedBytes)
	if status != StatusConnected {
		return status
	}
	for _, event := range buffered {
		if err := syncCtx.Err(); err != nil {
			return StatusUnavailable
		}
		if err := applyEvent(candidate, event); err != nil {
			return StatusUnavailable
		}
	}
	if syncCtx.Err() != nil || candidate.bytes > maxStatePayloadBytes {
		return StatusUnavailable
	}
	s.publish(candidate)
	liveCtx, stopLive := context.WithCancel(parent)
	defer stopLive()
	go func() {
		ticker := time.NewTicker(s.pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-liveCtx.Done():
				return
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(liveCtx, s.pongTimeout)
				err := conn.Ping(pingCtx)
				pingCancel()
				if err != nil {
					stopLive()
					return
				}
			}
		}
	}()
	for {
		kind, raw, err := conn.Read(liveCtx)
		if err != nil {
			return StatusUnavailable
		}
		if kind != websocket.MessageText {
			return StatusUnavailable
		}
		var envelope wsEnvelope
		if json.Unmarshal(raw, &envelope) != nil || envelope.Type != "event" || envelope.ID != 1 {
			return StatusUnavailable
		}
		var event stateEvent
		if json.Unmarshal(envelope.Event, &event) != nil || event.EventType != "state_changed" {
			return StatusUnavailable
		}
		if !s.applyLive(event) {
			return StatusUnavailable
		}
	}
}

func writeJSON(ctx context.Context, conn *websocket.Conn, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

type wsEnvelope struct {
	ID      int             `json:"id"`
	Type    string          `json:"type"`
	Success *bool           `json:"success"`
	Result  json.RawMessage `json:"result"`
	Error   struct {
		Code string `json:"code"`
	} `json:"error"`
	Event json.RawMessage `json:"event"`
}

func appendBuffered(events []stateEvent, size, rawSize int, event stateEvent) ([]stateEvent, int, bool) {
	if len(events) >= maxBufferedEvents || size+rawSize > maxBufferedEventBytes {
		return events, size, false
	}
	return append(events, event), size + rawSize, true
}

func (s *StateSession) awaitSubscription(ctx context.Context, conn *websocket.Conn) ([]stateEvent, int, Status) {
	var buffer []stateEvent
	bufferedBytes := 0
	for {
		kind, raw, err := conn.Read(ctx)
		if err != nil || kind != websocket.MessageText {
			return nil, 0, StatusUnavailable
		}
		var envelope wsEnvelope
		if json.Unmarshal(raw, &envelope) != nil {
			return nil, 0, StatusUnavailable
		}
		if envelope.Type == "event" && envelope.ID == 1 {
			var event stateEvent
			if json.Unmarshal(envelope.Event, &event) != nil || envelope.ID != 1 || event.EventType != "state_changed" || event.Data.EntityID == "" {
				return nil, 0, StatusUnavailable
			}
			var ok bool
			buffer, bufferedBytes, ok = appendBuffered(buffer, bufferedBytes, len(raw), event)
			if !ok {
				return nil, 0, StatusUnavailable
			}
			continue
		}
		if envelope.ID != 1 || envelope.Type != "result" {
			return nil, 0, StatusUnavailable
		}
		if envelope.Success == nil || !*envelope.Success {
			if deniedProtocolCode(envelope.Error.Code) {
				return nil, 0, StatusDenied
			}
			return nil, 0, StatusUnavailable
		}
		return buffer, bufferedBytes, StatusConnected
	}
}

func deniedProtocolCode(code string) bool {
	switch strings.ToLower(code) {
	case "unauthorized", "not_allowed", "auth_invalid", "forbidden":
		return true
	default:
		return false
	}
}

type stateEvent struct {
	EventType string `json:"event_type"`
	TimeFired string `json:"time_fired"`
	Data      struct {
		EntityID string          `json:"entity_id"`
		NewState json.RawMessage `json:"new_state"`
	} `json:"data"`
}

func (s *StateSession) awaitSnapshot(ctx context.Context, conn *websocket.Conn, buffer []stateEvent, bufferedBytes int) (*stateCandidate, []stateEvent, Status) {
	for {
		kind, raw, err := conn.Read(ctx)
		if err != nil || kind != websocket.MessageText {
			return nil, nil, StatusUnavailable
		}
		if ctx.Err() != nil {
			return nil, nil, StatusUnavailable
		}
		var envelope wsEnvelope
		if json.Unmarshal(raw, &envelope) != nil {
			return nil, nil, StatusUnavailable
		}
		if envelope.Type == "event" && envelope.ID == 1 {
			var event stateEvent
			if json.Unmarshal(envelope.Event, &event) != nil || event.EventType != "state_changed" || event.Data.EntityID == "" {
				return nil, nil, StatusUnavailable
			}
			var ok bool
			buffer, bufferedBytes, ok = appendBuffered(buffer, bufferedBytes, len(raw), event)
			if !ok {
				return nil, nil, StatusUnavailable
			}
			continue
		}
		if envelope.Type != "result" || envelope.ID != 2 || envelope.Success == nil || !*envelope.Success {
			if deniedProtocolCode(envelope.Error.Code) {
				return nil, nil, StatusDenied
			}
			return nil, nil, StatusUnavailable
		}
		result := strings.TrimSpace(string(envelope.Result))
		if len(result) == 0 || result[0] != '[' {
			return nil, nil, StatusUnavailable
		}
		var rawStates []json.RawMessage
		if json.Unmarshal(envelope.Result, &rawStates) != nil {
			return nil, nil, StatusUnavailable
		}
		candidate, err := newStateCandidate(rawStates)
		if err != nil {
			return nil, nil, StatusUnavailable
		}
		if ctx.Err() != nil {
			return nil, nil, StatusUnavailable
		}
		return candidate, buffer, StatusConnected
	}
}

func decodeEntityState(raw json.RawMessage) (EntityState, error) {
	var wire struct {
		EntityID    string         `json:"entity_id"`
		State       *string        `json:"state"`
		Attributes  map[string]any `json:"attributes"`
		LastChanged string         `json:"last_changed"`
		LastUpdated string         `json:"last_updated"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&wire); err != nil {
		return EntityState{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return EntityState{}, errors.New("trailing state data")
	}
	if wire.EntityID == "" || wire.State == nil || wire.Attributes == nil {
		return EntityState{}, errors.New("invalid state")
	}
	changed, err := parseTimestamp(wire.LastChanged)
	if err != nil {
		return EntityState{}, err
	}
	updated, err := parseTimestamp(wire.LastUpdated)
	if err != nil {
		return EntityState{}, err
	}
	state := EntityState{EntityID: wire.EntityID, State: *wire.State, Attributes: wire.Attributes, LastChanged: changed, LastUpdated: updated}
	return state, nil
}
func parseTimestamp(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, value)
	return t.UTC(), err
}

func applyEvent(candidate *stateCandidate, event stateEvent) error {
	if event.Data.EntityID == "" || event.TimeFired == "" {
		return errors.New("invalid event")
	}
	fired, err := parseTimestamp(event.TimeFired)
	if err != nil {
		return err
	}
	watermark, hasWatermark := candidate.watermarks[event.Data.EntityID]
	if hasWatermark && !watermark.timeFired.IsZero() {
		if fired.Before(watermark.timeFired) {
			return nil
		}
		if fired.Equal(watermark.timeFired) {
			if string(event.Data.NewState) == "null" {
				if _, deleted := candidate.tombstones[event.Data.EntityID]; deleted {
					return nil
				}
			} else {
				state, decodeErr := decodeEntityState(event.Data.NewState)
				if decodeErr == nil && state.EntityID == event.Data.EntityID {
					if old, exists := candidate.states[state.EntityID]; exists && sameState(old, state) {
						return nil
					}
				}
			}
			return errors.New("ambiguous event ordering")
		}
	}
	if string(event.Data.NewState) == "null" {
		if current, exists := candidate.states[event.Data.EntityID]; exists {
			if fired.Before(current.LastUpdated) {
				watermark.timeFired = fired
				candidate.watermarks[event.Data.EntityID] = watermark
				return nil
			}
			if fired.Equal(current.LastUpdated) {
				return errors.New("ambiguous removal ordering")
			}
		}
		if _, alreadyDeleted := candidate.tombstones[event.Data.EntityID]; !alreadyDeleted {
			if len(candidate.tombstones) >= maxStateTombstones {
				return errors.New("state tombstone count limit exceeded")
			}
			if candidate.tombstoneBytes+tombstonePayloadSize(event.Data.EntityID, fired) > maxStateTombstoneBytes {
				return errors.New("state tombstone byte limit exceeded")
			}
		}
		candidate.remove(event.Data.EntityID)
		watermark.timeFired = fired
		candidate.watermarks[event.Data.EntityID] = watermark
		if _, alreadyDeleted := candidate.tombstones[event.Data.EntityID]; !alreadyDeleted {
			candidate.tombstones[event.Data.EntityID] = fired
			candidate.tombstoneBytes += tombstonePayloadSize(event.Data.EntityID, fired)
		}
		return nil
	}
	state, err := decodeEntityState(event.Data.NewState)
	if err != nil || state.EntityID != event.Data.EntityID {
		return errors.New("invalid event state")
	}
	if watermark, ok := candidate.watermarks[state.EntityID]; ok {
		if state.LastUpdated.Before(watermark.lastUpdated) {
			watermark.timeFired = fired
			candidate.watermarks[state.EntityID] = watermark
			return nil
		}
		if state.LastUpdated.Equal(watermark.lastUpdated) {
			old, exists := candidate.states[state.EntityID]
			if !exists || !sameState(old, state) {
				return errors.New("ambiguous state ordering")
			}
		}
		if _, deleted := candidate.tombstones[state.EntityID]; deleted {
			if state.LastUpdated.Before(watermark.timeFired) {
				watermark.timeFired = fired
				candidate.watermarks[state.EntityID] = watermark
				return nil
			}
			if state.LastUpdated.Equal(watermark.timeFired) {
				return errors.New("ambiguous recreation ordering")
			}
		}
	}
	if err := candidate.set(state.EntityID, state); err != nil {
		return err
	}
	watermark = candidate.watermarks[state.EntityID]
	watermark.timeFired = fired
	candidate.watermarks[state.EntityID] = watermark
	return nil
}
func sameState(a, b EntityState) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func (s *StateSession) publish(candidate *stateCandidate) {
	s.mu.Lock()
	previousStatus := s.metadata.Status
	s.states = candidate.states
	s.stateBytes = candidate.bytes
	s.watermarks = candidate.watermarks
	s.tombstones = candidate.tombstones
	s.tombstoneBytes = candidate.tombstoneBytes
	s.metadata.Generation++
	s.metadata.Revision = 0
	s.metadata.EntityCount = len(candidate.states)
	s.metadata.Fresh = true
	now := time.Now().UTC()
	s.metadata.LastSuccessfulSync = &now
	s.metadata.Phase = PhaseReady
	s.metadata.Status = StatusConnected
	s.broadcastLocked(state.Event{Position: s.positionLocked(), Kind: state.Reset, Fresh: true, Snapshot: &StateSnapshot{Generation: s.metadata.Generation, Revision: 0, Fresh: true, States: s.states}})
	s.mu.Unlock()
	if s.logger != nil && previousStatus != StatusConnected {
		s.logger.Info("Home Assistant state session status changed", "status", StatusConnected)
	}
	s.signal()
}
func (s *StateSession) applyLive(event stateEvent) bool {
	s.mu.Lock()
	old, existed := s.states[event.Data.EntityID]
	candidate := &stateCandidate{states: s.states, watermarks: s.watermarks, tombstones: s.tombstones, tombstoneBytes: s.tombstoneBytes, bytes: s.stateBytes, maxBytes: maxStatePayloadBytes}
	if err := applyEvent(candidate, event); err != nil {
		s.staleLocked()
		s.mu.Unlock()
		return false
	}
	s.stateBytes = candidate.bytes
	s.tombstoneBytes = candidate.tombstoneBytes
	s.metadata.EntityCount = len(candidate.states)
	current, exists := s.states[event.Data.EntityID]
	if existed != exists || (exists && !sameState(old, current)) {
		s.metadata.Revision++
		change := state.Event{Position: s.positionLocked(), Fresh: s.metadata.Fresh, EntityID: event.Data.EntityID}
		switch {
		case !exists:
			change.Kind = state.Remove
		case !existed:
			change.Kind = state.Add
			change.Entity = &current
		default:
			change.Kind = state.Update
			change.Entity = &current
		}
		s.broadcastLocked(change)
	}
	s.mu.Unlock()
	s.signal()
	return true
}

func cloneWatermarks(values map[string]stateWatermark) map[string]stateWatermark {
	clone := make(map[string]stateWatermark, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func cloneTombstones(values map[string]time.Time) map[string]time.Time {
	clone := make(map[string]time.Time, len(values))
	for key, timestamp := range values {
		clone[key] = timestamp
	}
	return clone
}

func (s *StateSession) markDisconnected(status Status) {
	s.mu.Lock()
	previousStatus := s.metadata.Status
	s.metadata.Phase = PhaseDisconnected
	s.metadata.Status = status
	s.staleLocked()
	s.mu.Unlock()
	if s.logger != nil && previousStatus != status {
		s.logger.Info("Home Assistant state session status changed", "status", status)
	}
	s.signal()
}
