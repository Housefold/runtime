package ha

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestCandidateReconcilesStateEvents(t *testing.T) {
	base := json.RawMessage(`{"entity_id":"light.a","state":"off","attributes":{"level":1},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:00Z"}`)
	c, err := newStateCandidate([]json.RawMessage{base})
	if err != nil {
		t.Fatal(err)
	}
	updated := stateEvent{EventType: "state_changed", TimeFired: "2026-01-01T00:00:02Z"}
	updated.Data.EntityID = "light.a"
	updated.Data.NewState = json.RawMessage(`{"entity_id":"light.a","state":"off","attributes":{"level":2},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:01Z"}`)
	if err := applyEvent(c, updated); err != nil {
		t.Fatal(err)
	}
	if c.states["light.a"].Attributes["level"] != json.Number("2") {
		t.Fatalf("attribute update not applied: %#v", c.states["light.a"].Attributes)
	}
	olderRemoval := updated
	olderRemoval.TimeFired = "2026-01-01T00:00:01.500Z"
	olderRemoval.Data.NewState = json.RawMessage("null")
	if err := applyEvent(c, olderRemoval); err != nil {
		t.Fatal(err)
	}
	if _, exists := c.states["light.a"]; !exists {
		t.Fatal("older removal deleted a state changed by a newer event")
	}
	old := updated
	old.TimeFired = "2026-01-01T00:00:01Z"
	old.Data.NewState = json.RawMessage(`{"entity_id":"light.a","state":"on","attributes":{},"last_changed":"2025-12-31T23:59:00Z","last_updated":"2025-12-31T23:59:00Z"}`)
	if err := applyEvent(c, old); err != nil {
		t.Fatal(err)
	}
	if c.states["light.a"].State != "off" {
		t.Fatal("older event overwrote newer state")
	}
	added := updated
	added.Data.EntityID = "sensor.b"
	added.Data.NewState = json.RawMessage(`{"entity_id":"sensor.b","state":"1","attributes":{},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:01Z"}`)
	if err := applyEvent(c, added); err != nil {
		t.Fatal(err)
	}
	if len(c.states) != 2 {
		t.Fatalf("got %d entities, want 2", len(c.states))
	}
	removed := updated
	removed.TimeFired = "2026-01-01T00:00:04Z"
	removed.Data.EntityID = "light.a"
	removed.Data.NewState = json.RawMessage("null")
	if err := applyEvent(c, removed); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.states["light.a"]; ok || len(c.states) != 1 {
		t.Fatal("null new_state did not remove entity")
	}
}

func TestStateSessionMetadataAndSnapshotIsolation(t *testing.T) {
	s := newStateSession("ws://unused", waitContext)
	candidate, err := newStateCandidate([]json.RawMessage{json.RawMessage(`{"entity_id":"sensor.a","state":"1","attributes":{"nested":{"value":1}},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:00Z"}`)})
	if err != nil {
		t.Fatal(err)
	}
	s.publish(candidate)
	metadata := s.Metadata()
	if metadata.Phase != PhaseReady || metadata.Status != StatusConnected || !metadata.Fresh || metadata.Generation != 1 || metadata.EntityCount != 1 || metadata.LastSuccessfulSync == nil {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
	snapshot := s.Snapshot()
	snapshot.States["sensor.a"].Attributes["nested"].(map[string]any)["value"] = "changed"
	if s.Snapshot().States["sensor.a"].Attributes["nested"].(map[string]any)["value"] != json.Number("1") {
		t.Fatal("snapshot shares nested state memory")
	}
	s.markDisconnected(StatusUnavailable)
	metadata = s.Metadata()
	if metadata.Fresh || metadata.Phase != PhaseDisconnected || metadata.Generation != 1 || metadata.EntityCount != 1 {
		t.Fatalf("disconnect did not retain stale generation: %+v", metadata)
	}
}

func TestStateSessionLogsOnlyCoarseTransitions(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	s := newStateSession("ws://unused", waitContext, logger)
	s.setPhase(PhaseConnecting)
	s.markDisconnected(StatusDenied)
	logs := output.String()
	for _, safe := range []string{"Connecting", "denied"} {
		if !strings.Contains(logs, safe) {
			t.Fatalf("logs do not include coarse transition %q: %s", safe, logs)
		}
	}
	for _, secret := range []string{"test-token", "sensor.secret", "secret_attribute", "last_updated"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("logs contain sensitive field %q: %s", secret, logs)
		}
	}
}

func TestEqualTimestampConflictingStateFailsClosed(t *testing.T) {
	c, err := newStateCandidate([]json.RawMessage{json.RawMessage(`{"entity_id":"sensor.a","state":"1","attributes":{},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:00Z"}`)})
	if err != nil {
		t.Fatal(err)
	}
	e := stateEvent{EventType: "state_changed", TimeFired: time.Now().UTC().Format(time.RFC3339Nano)}
	e.Data.EntityID = "sensor.a"
	e.Data.NewState = json.RawMessage(`{"entity_id":"sensor.a","state":"2","attributes":{},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:00Z"}`)
	if applyEvent(c, e) == nil {
		t.Fatal("conflicting equal-timestamp event should fail closed")
	}
}

func TestMalformedStateAndSnapshotOverflowFailClosed(t *testing.T) {
	t.Run("missing state field", func(t *testing.T) {
		_, err := decodeEntityState(json.RawMessage(`{"entity_id":"sensor.a","attributes":{},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:00Z"}`))
		if err == nil {
			t.Fatal("missing state field was accepted")
		}
	})
	t.Run("snapshot payload exceeds limit", func(t *testing.T) {
		snapshot := []json.RawMessage{json.RawMessage(`{"entity_id":"sensor.a","state":"1","attributes":{},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:00Z"}`)}
		if _, err := newStateCandidateWithLimit(snapshot, 8); err == nil {
			t.Fatal("oversized snapshot was accepted")
		}
	})
}

func TestStateSessionSubscribesBuffersAndPublishesAtomically(t *testing.T) {
	snapshotRequested := make(chan struct{})
	allowSnapshot := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"auth_required"}`))
		_, auth, err := conn.Read(ctx)
		if err != nil || !strings.Contains(string(auth), `"type":"auth"`) {
			return
		}
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"auth_ok"}`))
		_, subscribe, err := conn.Read(ctx)
		if err != nil || !strings.Contains(string(subscribe), `"subscribe_events"`) {
			return
		}
		// Core may emit an event after listener registration and before its ack.
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"id":1,"type":"event","event":{"event_type":"state_changed","time_fired":"2026-01-01T00:00:02Z","data":{"entity_id":"sensor.a","new_state":{"entity_id":"sensor.a","state":"2","attributes":{"v":2},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:02Z"}}}}`))
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"id":1,"type":"result","success":true}`))
		_, states, err := conn.Read(ctx)
		if err != nil || !strings.Contains(string(states), `"get_states"`) {
			return
		}
		close(snapshotRequested)
		select {
		case <-allowSnapshot:
		case <-ctx.Done():
			return
		}
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"id":2,"type":"result","success":true,"result":[{"entity_id":"sensor.a","state":"1","attributes":{"v":1},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:01Z"}]}`))
		<-ctx.Done()
	}))
	defer server.Close()
	s := newStateSession("ws"+strings.TrimPrefix(server.URL, "http"), waitContext)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx, "test-token"); close(done) }()
	select {
	case <-snapshotRequested:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("session did not request the state snapshot")
	}
	metadata := s.Metadata()
	if metadata.Phase != PhaseSyncing || metadata.Status != StatusConnected || metadata.Fresh {
		cancel()
		t.Fatalf("connection and state readiness were not separated while syncing: %+v", metadata)
	}
	close(allowSnapshot)
	deadline := time.After(3 * time.Second)
	for {
		if m := s.Metadata(); m.Phase == PhaseReady && m.Fresh {
			if m.Generation != 1 || m.EntityCount != 1 {
				t.Fatalf("unexpected ready metadata: %+v", m)
			}
			if got := s.Snapshot().States["sensor.a"].State; got != "2" {
				t.Fatalf("buffered event was not reconciled: %q", got)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("session did not become ready")
		case <-s.Changes():
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session did not stop promptly")
	}
	if m := s.Metadata(); m.Fresh || m.Generation != 1 {
		t.Fatalf("disconnect did not stale retained generation: %+v", m)
	}
}

func serveAuthenticated(t *testing.T, conn *websocket.Conn, ctx context.Context) bool {
	t.Helper()
	write := func(text string) bool { return conn.Write(ctx, websocket.MessageText, []byte(text)) == nil }
	readContains := func(fragment string) bool {
		_, data, err := conn.Read(ctx)
		return err == nil && strings.Contains(string(data), fragment)
	}
	if !write(`{"type":"auth_required"}`) || !readContains(`"type":"auth"`) || !write(`{"type":"auth_ok"}`) || !readContains(`"subscribe_events"`) || !write(`{"id":1,"type":"result","success":true}`) || !readContains(`"get_states"`) {
		return false
	}
	return true
}

func TestStateSessionRejectsNullSnapshotResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		if serveAuthenticated(t, conn, r.Context()) {
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"id":2,"type":"result","success":true,"result":null}`))
		}
	}))
	defer server.Close()
	s := newStateSession("ws"+strings.TrimPrefix(server.URL, "http"), waitContext)
	if got := s.runConnection(context.Background(), "token"); got != StatusUnavailable {
		t.Fatalf("got %q, want unavailable", got)
	}
	if s.Metadata().Generation != 0 || s.Metadata().Fresh {
		t.Fatalf("null snapshot result published a generation: %+v", s.Metadata())
	}
}

func TestStateSessionDeniedAuthAndSyncTimeout(t *testing.T) {
	t.Run("unavailable HA", func(t *testing.T) {
		s := newStateSession("ws://127.0.0.1:1", waitContext)
		if got := s.runConnection(context.Background(), "token"); got != StatusUnavailable {
			t.Fatalf("got %q, want unavailable", got)
		}
		if s.Metadata().Generation != 0 || s.Metadata().Fresh {
			t.Fatalf("unavailable HA published a generation: %+v", s.Metadata())
		}
	})
	t.Run("denied auth", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"auth_required"}`))
			_, _, _ = conn.Read(r.Context())
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"auth_invalid"}`))
		}))
		defer server.Close()
		s := newStateSession("ws"+strings.TrimPrefix(server.URL, "http"), waitContext)
		if got := s.runConnection(context.Background(), "token"); got != StatusDenied {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("sync timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			if serveAuthenticated(t, conn, r.Context()) {
				<-r.Context().Done()
			}
		}))
		defer server.Close()
		s := newStateSession("ws"+strings.TrimPrefix(server.URL, "http"), waitContext)
		s.syncTimeout = 20 * time.Millisecond
		started := time.Now()
		if got := s.runConnection(context.Background(), "token"); got != StatusUnavailable {
			t.Fatalf("got %q", got)
		}
		s.markDisconnected(StatusUnavailable)
		if time.Since(started) > time.Second {
			t.Fatal("sync timeout was not bounded")
		}
		if s.Metadata().Generation != 0 {
			t.Fatal("timed out candidate was published")
		}
	})
}

func TestStateSessionReconnectPublishesNewGeneration(t *testing.T) {
	var connections int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		mu.Lock()
		connections++
		number := connections
		mu.Unlock()
		if !serveAuthenticated(t, conn, r.Context()) {
			return
		}
		if number == 1 {
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"id":2,"type":"result","success":true,"result":[]}`))
			return
		}
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"id":2,"type":"result","success":true,"result":[{"entity_id":"sensor.new","state":"ok","attributes":{},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:00Z"}]}`))
		<-r.Context().Done()
	}))
	defer server.Close()
	s := newStateSession("ws"+strings.TrimPrefix(server.URL, "http"), func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx, "token"); close(done) }()
	deadline := time.After(3 * time.Second)
	for s.Metadata().Generation < 2 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("did not publish second generation")
		case <-s.Changes():
		}
	}
	if got := s.Snapshot().States["sensor.new"].State; got != "ok" {
		t.Fatalf("second generation state = %q", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session did not cancel")
	}
}

func TestRunUsesBoundedRetrySchedule(t *testing.T) {
	var delays []time.Duration
	s := newStateSession("ws://127.0.0.1:1", func(_ context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		return len(delays) < 7
	})
	s.Run(context.Background(), "token")
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	if len(delays) != len(want) {
		t.Fatalf("retry count = %d, want %d (%v)", len(delays), len(want), delays)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("retry delay %d = %s, want %s; all=%v", i, delays[i], want[i], delays)
		}
	}
}

func TestStateSessionMalformedFrameAndPingTimeoutDisconnect(t *testing.T) {
	t.Run("malformed frame", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			if serveAuthenticated(t, conn, r.Context()) {
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{broken`))
			}
		}))
		defer server.Close()
		s := newStateSession("ws"+strings.TrimPrefix(server.URL, "http"), waitContext)
		if got := s.runConnection(context.Background(), "token"); got != StatusUnavailable {
			t.Fatalf("got %q", got)
		}
		if s.Metadata().Generation != 0 {
			t.Fatal("malformed candidate was published")
		}
	})
	t.Run("ping timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			if !serveAuthenticated(t, conn, r.Context()) {
				return
			}
			_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"id":2,"type":"result","success":true,"result":[]}`))
			<-r.Context().Done() // Deliberately do not read the client's ping.
		}))
		defer server.Close()
		s := newStateSession("ws"+strings.TrimPrefix(server.URL, "http"), waitContext)
		s.pingInterval = 5 * time.Millisecond
		s.pongTimeout = 20 * time.Millisecond
		started := time.Now()
		if got := s.runConnection(context.Background(), "token"); got != StatusUnavailable {
			t.Fatalf("got %q", got)
		}
		if time.Since(started) > time.Second {
			t.Fatal("ping failure did not close the session promptly")
		}
		s.markDisconnected(StatusUnavailable)
		if s.Metadata().Fresh {
			t.Fatal("ping loss left generation fresh")
		}
	})
}

func TestBufferedEventOverflowAndRemovalRecreateOrdering(t *testing.T) {
	event := stateEvent{EventType: "state_changed"}
	if _, _, ok := appendBuffered(make([]stateEvent, 0, maxBufferedEvents), maxBufferedEventBytes, 1, event); ok {
		t.Fatal("byte overflow was accepted")
	}
	full := make([]stateEvent, maxBufferedEvents)
	if _, _, ok := appendBuffered(full, 0, 1, event); ok {
		t.Fatal("event count overflow was accepted")
	}
	c, err := newStateCandidate([]json.RawMessage{json.RawMessage(`{"entity_id":"sensor.a","state":"1","attributes":{},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:01Z"}`)})
	if err != nil {
		t.Fatal(err)
	}
	remove := stateEvent{EventType: "state_changed", TimeFired: "2026-01-01T00:00:02Z"}
	remove.Data.EntityID = "sensor.a"
	remove.Data.NewState = json.RawMessage("null")
	if err := applyEvent(c, remove); err != nil {
		t.Fatal(err)
	}
	stale := remove
	stale.TimeFired = "2026-01-01T00:00:01Z"
	stale.Data.NewState = json.RawMessage(`{"entity_id":"sensor.a","state":"old","attributes":{},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:01Z"}`)
	if err := applyEvent(c, stale); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.states["sensor.a"]; ok {
		t.Fatal("stale event recreated removed entity")
	}
	newer := stale
	newer.TimeFired = "2026-01-01T00:00:04Z"
	newer.Data.NewState = json.RawMessage(`{"entity_id":"sensor.a","state":"new","attributes":{},"last_changed":"2026-01-01T00:00:03Z","last_updated":"2026-01-01T00:00:03Z"}`)
	if err := applyEvent(c, newer); err != nil {
		t.Fatal(err)
	}
	if c.states["sensor.a"].State != "new" {
		t.Fatal("newer event did not recreate entity")
	}
}

func TestLiveRemovalWatermarkSurvivesEventPublication(t *testing.T) {
	s := newStateSession("ws://unused", waitContext)
	candidate, err := newStateCandidate([]json.RawMessage{json.RawMessage(`{"entity_id":"sensor.a","state":"1","attributes":{},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:01Z"}`)})
	if err != nil {
		t.Fatal(err)
	}
	s.publish(candidate)

	remove := stateEvent{EventType: "state_changed", TimeFired: "2026-01-01T00:00:02Z"}
	remove.Data.EntityID = "sensor.a"
	remove.Data.NewState = json.RawMessage("null")
	if !s.applyLive(remove) {
		t.Fatal("live removal failed")
	}
	stale := remove
	stale.TimeFired = "2026-01-01T00:00:01Z"
	stale.Data.NewState = json.RawMessage(`{"entity_id":"sensor.a","state":"old","attributes":{},"last_changed":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:01Z"}`)
	if !s.applyLive(stale) {
		t.Fatal("stale live event should be ignored without failing the session")
	}
	if _, exists := s.Snapshot().States["sensor.a"]; exists {
		t.Fatal("stale live event recreated removed entity")
	}
	newer := stale
	newer.TimeFired = "2026-01-01T00:00:03Z"
	newer.Data.NewState = json.RawMessage(`{"entity_id":"sensor.a","state":"new","attributes":{},"last_changed":"2026-01-01T00:00:03Z","last_updated":"2026-01-01T00:00:03Z"}`)
	if !s.applyLive(newer) {
		t.Fatal("newer live state failed")
	}
	if got := s.Snapshot().States["sensor.a"].State; got != "new" {
		t.Fatalf("newer live event did not recreate entity: state=%q", got)
	}
}

func TestDistinctRemovalTombstonesAreBounded(t *testing.T) {
	candidate, err := newStateCandidate(nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxStateTombstones; i++ {
		event := stateEvent{EventType: "state_changed", TimeFired: time.Date(2026, 1, 1, 0, 0, 0, i, time.UTC).Format(time.RFC3339Nano)}
		event.Data.EntityID = fmt.Sprintf("sensor.removed_%d", i)
		event.Data.NewState = json.RawMessage("null")
		if err := applyEvent(candidate, event); err != nil {
			t.Fatalf("removal %d: %v", i, err)
		}
	}
	tooMany := stateEvent{EventType: "state_changed", TimeFired: "2026-01-01T00:00:01Z"}
	tooMany.Data.EntityID = "sensor.overflow"
	tooMany.Data.NewState = json.RawMessage("null")
	if err := applyEvent(candidate, tooMany); err == nil {
		t.Fatal("tombstone count overflow was accepted")
	}
	byBytes, err := newStateCandidate(nil)
	if err != nil {
		t.Fatal(err)
	}
	byBytes.tombstoneBytes = maxStateTombstoneBytes
	tooLarge := tooMany
	tooLarge.Data.EntityID = "sensor.byte_overflow"
	if err := applyEvent(byBytes, tooLarge); err == nil {
		t.Fatal("tombstone byte overflow was accepted")
	}
}
