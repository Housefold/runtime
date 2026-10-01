package hacontrol

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/housefold/runtime/internal/action"
	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/execution"
	"github.com/housefold/runtime/internal/module"
)

func nativeFixture(t *testing.T, handler http.HandlerFunc) *Native {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	n := NewNative("SYNTHETIC_SUPERVISOR", nil)
	n.base = server.URL + "/api/"
	t.Cleanup(n.core.Close)
	t.Cleanup(func() { n.http.CloseIdleConnections() })
	return n
}
func nativeRequest() action.Request {
	return action.Request{ID: "synthetic-action", Domain: "light", Service: "turn_on", Data: json.RawMessage(`{"entity_id":"light.synthetic_private"}`)}
}
func TestNativeExplicitAndUnknownOutcomesNeverRetry(t *testing.T) {
	for _, row := range []struct {
		status int
		want   action.Outcome
	}{{200, action.Accepted}, {400, action.Rejected}, {401, action.Rejected}, {403, action.Rejected}, {404, action.Rejected}, {408, action.Unknown}, {500, action.Unknown}, {307, action.Unknown}} {
		t.Run(http.StatusText(row.status), func(t *testing.T) {
			var calls, target atomic.Int32
			n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/target" {
					target.Add(1)
					w.WriteHeader(200)
					return
				}
				calls.Add(1)
				if r.Header.Get("Authorization") != "Bearer SYNTHETIC_SUPERVISOR" {
					t.Error("local auth missing")
				}
				if r.URL.Path != "/api/services/light/turn_on" {
					t.Error("wrong HA boundary")
				}
				w.Header().Set("Location", "/target")
				w.WriteHeader(row.status)
				_, _ = w.Write([]byte(`{"household_value":"must-not-return"}`))
			})
			outcome, err := n.Call(context.Background(), nativeRequest())
			if outcome != row.want || (err == nil) != (row.want == action.Accepted) || calls.Load() != 1 || target.Load() != 0 {
				t.Fatal(outcome, err, calls.Load(), target.Load())
			}
			if err != nil && (strings.Contains(err.Error(), "SYNTHETIC_SUPERVISOR") || strings.Contains(err.Error(), "household_value")) {
				t.Fatal("private error")
			}
		})
	}
	var calls atomic.Int32
	n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	outcome, err := n.Call(context.Background(), nativeRequest())
	if outcome != action.Unknown || err == nil || calls.Load() != 1 {
		t.Fatal("ambiguous disconnect retried", outcome, err, calls.Load())
	}
}
func TestNativeNotSentIsProvenBeforeSend(t *testing.T) {
	var calls atomic.Int32
	n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if outcome, _ := n.Call(ctx, nativeRequest()); outcome != action.NotSent {
		t.Fatal(outcome)
	}
	request := nativeRequest()
	request.Domain = "../supervisor"
	if outcome, _ := n.Call(context.Background(), request); outcome != action.NotSent {
		t.Fatal(outcome)
	}
	request = nativeRequest()
	request.Data = json.RawMessage(`[]`)
	if outcome, _ := n.Call(context.Background(), request); outcome != action.NotSent {
		t.Fatal(outcome)
	}
	n.token = ""
	if outcome, _ := n.Call(context.Background(), nativeRequest()); outcome != action.NotSent || calls.Load() != 0 {
		t.Fatal(outcome, calls.Load())
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()
	n.token = "SYNTHETIC"
	n.base = address + "/"
	if outcome, _ := n.Call(context.Background(), nativeRequest()); outcome != action.NotSent {
		t.Fatal("preconnection error", outcome)
	}
}
func TestCoreOwnsAuthenticationIDsAndCommandConnection(t *testing.T) {
	var connections, commands atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		connections.Add(1)
		ctx := r.Context()
		_ = writeJSON(ctx, conn, map[string]any{"type": "auth_required"})
		_, raw, err := conn.Read(ctx)
		var auth struct {
			Token string `json:"access_token"`
		}
		if err != nil {
			return
		}
		_ = json.Unmarshal(raw, &auth)
		if auth.Token != "SYNTHETIC" {
			t.Error("wrong command auth")
			return
		}
		_ = writeJSON(ctx, conn, map[string]any{"type": "auth_ok"})
		var previous uint64
		for {
			_, raw, err = conn.Read(ctx)
			if err != nil {
				return
			}
			var command map[string]json.RawMessage
			_ = json.Unmarshal(raw, &command)
			var id uint64
			_ = json.Unmarshal(command["id"], &id)
			if id <= previous {
				t.Error("reused wire ID")
			}
			previous = id
			if _, ok := command["body"]; ok {
				t.Error("unflattened command")
			}
			commands.Add(1)
			if string(command["mode"]) == `"hang"` {
				_, _, _ = conn.Read(ctx)
				return
			}
			if string(command["mode"]) == `"bad_id"` {
				id++
			}
			if err = writeJSON(ctx, conn, map[string]any{"id": id, "type": "result", "success": true, "result": command["value"]}); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	core := newCore("SYNTHETIC", "ws"+strings.TrimPrefix(server.URL, "http"), localHTTP())
	defer core.Close()
	for _, id := range []uint64{999, 1} {
		request, _ := json.Marshal(map[string]any{"id": id, "type": "synthetic/test", "body": map[string]any{"value": "fixture"}})
		raw, err := core.Exchange(context.Background(), request, 4096)
		var reply struct {
			ID uint64 `json:"id"`
		}
		_ = json.Unmarshal(raw, &reply)
		if err != nil || reply.ID != id {
			t.Fatal(string(raw), err)
		}
	}
	if connections.Load() != 1 || commands.Load() != 2 {
		t.Fatal("private connection not reused", connections.Load(), commands.Load())
	}
	bad := []byte(`{"id":1,"type":"test","body":{"id":7}}`)
	if _, err := core.Exchange(context.Background(), bad, 4096); err == nil {
		t.Fatal("caller forged wire ownership")
	}
	if _, err := core.Exchange(context.Background(), []byte(`{"id":1,"type":"test","mode":"bad_id"}`), 4096); err == nil {
		t.Fatal("wrong reply ID accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := core.Exchange(ctx, []byte(`{"id":1,"type":"test","mode":"hang"}`), 4096); err == nil {
		t.Fatal("timeout ignored")
	}
	if _, err := core.Exchange(context.Background(), []byte(`{"id":1,"type":"test","value":true}`), 4096); err != nil {
		t.Fatal("fresh command after timeout", err)
	}
	core.gate <- struct{}{}
	if _, err := core.Exchange(context.Background(), []byte(`{"id":1,"type":"test"}`), 4096); err == nil {
		t.Fatal("unbounded command admission")
	}
	<-core.gate
}
func TestOperationalSignalsHaveNoControlsOrHouseholdPayload(t *testing.T) {
	bodies := make(chan string, 32)
	n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		bodies <- r.URL.Path + ":" + string(raw)
		w.WriteHeader(200)
	})
	n.SetSignals(func() OperationalSnapshot {
		return OperationalSnapshot{Runtime: "recovery_required", Catalog: "unconfigured", Modules: []ModuleSignal{{Identity: "synthetic", Version: "1.0.0", Generation: 2, Installed: true, Desired: true, Phase: "QUARANTINED"}}}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); n.Run(ctx) }()
	sawRuntime, sawModule, sawNotification := false, false, false
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for !sawRuntime || !sawModule || !sawNotification {
		select {
		case body := <-bodies:
			if strings.Contains(body, "SYNTHETIC_SUPERVISOR") || strings.Contains(body, "light.synthetic_private") || strings.Contains(body, "button.") {
				t.Fatal("private data or admin control in signals", body)
			}
			sawRuntime = sawRuntime || strings.HasPrefix(body, "/api/states/sensor.housefold_runtime:")
			sawModule = sawModule || strings.HasPrefix(body, "/api/states/"+entitySignalID("synthetic")+":")
			sawNotification = sawNotification || strings.HasPrefix(body, "/api/services/persistent_notification/create:")
		case <-deadline.C:
			t.Fatal("missing runtime/module/notification signals")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("native workers failed to join")
	}
	// Attributed action events use only identifiers/outcome, never request Data.
	n2 := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	id := module.Identity{Module: "synthetic", Version: "1.0.0", Generation: 2, Boot: "boot"}
	if outcome, err := n2.CallAttributed(context.Background(), id, nativeRequest()); outcome != action.Accepted || err != nil {
		t.Fatal(outcome, err)
	}
	signal := <-n2.events
	if signal.Module != id.Module || signal.Generation != 2 || signal.Request != "synthetic-action" || signal.Outcome != action.Accepted {
		t.Fatal(signal)
	}
	for i := 0; i < 65; i++ {
		n2.events <- ActionSignal{}
		if len(n2.events) == 64 {
			break
		}
	}
	_, _ = n2.CallAttributed(context.Background(), id, nativeRequest())
	if n2.droppedEvents.Load() != 1 {
		t.Fatal("event pressure not bounded/visible")
	}
}

func TestNativeGatewayUnknownIsDurableAndStaleAuthorityNeverCallsHA(t *testing.T) {
	var calls atomic.Int32
	n := nativeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	root := t.TempDir()
	manager, err := execution.Open(durable.NewFile(filepath.Join(root, "executions")))
	if err != nil {
		t.Fatal(err)
	}
	router, err := module.OpenRouter(durable.NewFile(filepath.Join(root, "router")), manager, "synthetic-boot")
	if err != nil {
		t.Fatal(err)
	}
	id, err := router.Prepare("synthetic", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err = router.Ready(id, true, nil); err != nil {
		t.Fatal(err)
	}
	if err = router.Cutover(id, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "actions")
	gateway, err := action.Open(durable.NewFile(path), router, n)
	if err != nil {
		t.Fatal(err)
	}
	request := nativeRequest()
	record, err := gateway.Submit(context.Background(), id, request)
	if record.Outcome != action.Unknown || err == nil {
		t.Fatal(record, err)
	}
	signal := <-n.events
	if signal.Module != id.Module || signal.Generation != id.Generation {
		t.Fatal("missing action attribution", signal)
	}
	recovered, err := action.Open(durable.NewFile(path), router, n)
	if err != nil {
		t.Fatal(err)
	}
	record, err = recovered.Submit(context.Background(), id, request)
	if err != nil || record.Outcome != action.Unknown || calls.Load() != 1 {
		t.Fatal("unknown blindly retried", record, err, calls.Load())
	}
	next, err := router.Prepare("synthetic", "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	_ = router.Ready(next, true, nil)
	if err = router.Cutover(next, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	request.ID = "fresh-after-cutover"
	if record, err = recovered.Submit(context.Background(), id, request); err == nil || record.Outcome != action.NotSent || calls.Load() != 1 {
		t.Fatal("stale authority reached native HA", record, err, calls.Load())
	}
}
