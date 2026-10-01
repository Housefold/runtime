package hacontrol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/housefold/runtime/internal/bridge"
	"github.com/housefold/runtime/internal/discovery"
	"github.com/housefold/runtime/internal/ha"
)

func TestProductionBridgeNegotiatesEnrichmentAndKeepsNativeAuthority(t *testing.T) {
	var mode atomic.Int32
	var ordered, hello, fetch atomic.Int32
	sources := ha.NewSources()
	sourceFixture(t, sources, false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		_ = writeJSON(ctx, conn, map[string]any{"type": "auth_required"})
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var auth map[string]string
		_ = json.Unmarshal(raw, &auth)
		if auth["access_token"] != "SYNTHETIC" {
			t.Error("Bridge did not use private local auth")
			return
		}
		_ = writeJSON(ctx, conn, map[string]any{"type": "auth_ok"})
		for {
			_, raw, err = conn.Read(ctx)
			if err != nil {
				return
			}
			var command map[string]json.RawMessage
			_ = json.Unmarshal(raw, &command)
			var kind string
			var id uint64
			_ = json.Unmarshal(command["type"], &kind)
			_ = json.Unmarshal(command["id"], &id)
			if strings.Contains(kind, "state") {
				ordered.Add(1)
				t.Error("unproven ordered state invoked")
			}
			if _, ok := command["body"]; ok {
				t.Error("Bridge private envelope leaked to Core")
			}
			result := any(nil)
			switch kind {
			case "housefold/hello":
				hello.Add(1)
				if mode.Load() == 0 {
					_ = writeJSON(ctx, conn, map[string]any{"type": "result", "id": id, "success": false, "error": map[string]string{"code": "unknown_command"}})
					continue
				}
				major := 1
				if mode.Load() == 2 {
					major = 2
				}
				result = bridge.Hello{Protocol: bridge.Version{Major: major}, BridgeVersion: "1.0.0", CoreVersion: "2026.10.0", Capabilities: []bridge.Capability{{Name: "discovery", Version: bridge.Version{Major: 1}, Required: []string{"bounded_chunks"}, Limits: bridge.Limits{Frame: bridge.MaxFrame, Chunks: 2, Total: bridge.MaxTotal}}, {Name: "ordered_state", Version: bridge.Version{Major: 1}, Required: []string{"snapshot_barrier", "contiguous_sequence"}, Limits: bridge.Limits{Frame: bridge.MaxFrame, Chunks: 2, Total: bridge.MaxTotal}}}}
			case "housefold/discovery":
				fetch.Add(1)
				if mode.Load() == 3 {
					_ = writeJSON(ctx, conn, map[string]any{"type": "result", "id": id, "success": false, "error": map[string]string{"code": "unauthorized"}})
					continue
				}
				if _, ok := command["cursor"]; !ok {
					t.Error("flat Bridge cursor missing")
				}
				result = bridge.Page{Schema: 1, Epoch: "synthetic-epoch", Index: 0, Entities: bridge.Section[bridge.RegistryEntity]{Status: discovery.Available, Rows: []bridge.RegistryEntity{{EntityID: "light.synthetic", RegistryID: "synthetic-id", Name: "Enriched", AreaID: "synthetic-area", Status: discovery.Available, Attributes: []discovery.Field{{Name: "brightness", Type: "integer"}}}}}, Devices: bridge.Section[bridge.RegistryObject]{Status: discovery.Unsupported}, Areas: bridge.Section[bridge.RegistryObject]{Status: discovery.Redacted}, Services: bridge.Section[discovery.Service]{Status: discovery.Missing}}
			default:
				t.Error("unexpected Bridge command", kind)
				return
			}
			_ = writeJSON(ctx, conn, map[string]any{"type": "result", "id": id, "success": true, "result": result})
		}
	}))
	defer server.Close()
	n := NewNative("SYNTHETIC", sources.View())
	n.bridgeCore.Close()
	n.bridgeCore = newCore("SYNTHETIC", "ws"+strings.TrimPrefix(server.URL, "http"), localHTTP())
	n.bridgeClient = bridge.New(n.bridgeCore)
	defer n.bridgeCore.Close()
	defer n.core.Close()
	defer n.http.CloseIdleConnections()
	now := time.Now()
	n.probeBridge(context.Background(), now)
	if n.BridgeSnapshot().Status != "absent" || fetch.Load() != 0 {
		t.Fatal(n.BridgeSnapshot())
	}
	mode.Store(1)
	n.probeBridge(context.Background(), now.Add(time.Second))
	if hello.Load() != 1 {
		t.Fatal("Bridge backoff bypassed")
	}
	n.probeBridge(context.Background(), now.Add(31*time.Second))
	status := n.BridgeSnapshot()
	if status.Status != "available" || !status.OrderedAdvertised || status.OrderedEnabled || status.StateSource != "native" || status.Limitation == "" {
		t.Fatal(status)
	}
	native := discovery.Snapshot{Schema: 1, RegistryStatus: discovery.Unsupported, EntityRegistryStatus: discovery.Unsupported, DeviceRegistryStatus: discovery.Unsupported, AreaRegistryStatus: discovery.Unsupported, ServicesStatus: discovery.Unsupported, Entities: []discovery.Entity{{Identity: discovery.NativeIdentity("light.synthetic", ""), EntityID: "light.synthetic", Domain: "light", Name: "Native", Status: discovery.Available, Attributes: []discovery.Field{{Name: "generic", Type: "unknown"}}}, {Identity: discovery.NativeIdentity("unknown_domain.synthetic", ""), EntityID: "unknown_domain.synthetic", Domain: "unknown_domain", Name: "Generic", Status: discovery.Available}}}
	enriched, used, err := n.enrichDiscovery(native)
	if err != nil || !used || len(enriched.Entities) != 2 {
		t.Fatal(enriched, used, err)
	}
	for _, e := range enriched.Entities {
		if e.EntityID == "light.synthetic" && (e.Name != "Enriched" || e.Identity.Weak || len(e.Attributes) != 2) {
			t.Fatal("enrichment lost native generic facts", e)
		}
	}
	n.mu.Lock()
	n.discovered = enriched
	n.discoveryFresh = true
	n.bridgeSelected = used
	n.mu.Unlock()
	if n.BridgeSnapshot().Status != "active" || sources.Selected() != ha.NativeSource {
		t.Fatal("state ownership changed", n.BridgeSnapshot())
	}
	conflicting, _ := discovery.Normalize(native)
	for i := range conflicting.Entities {
		if conflicting.Entities[i].EntityID == "light.synthetic" {
			conflicting.Entities[i].Identity = discovery.NativeIdentity("light.synthetic", "conflicting-registry")
		}
	}
	original, _ := json.Marshal(conflicting)
	_, used, err = n.enrichDiscovery(conflicting)
	after, _ := json.Marshal(conflicting)
	if err == nil || used || string(original) != string(after) {
		t.Fatal("strong identity conflict mutated native facts", err)
	}
	mode.Store(3)
	n.probeBridge(context.Background(), now.Add(62*time.Second))
	if n.BridgeSnapshot().Status != "temporarily_unavailable" {
		t.Fatal(n.BridgeSnapshot())
	}
	fallback, used, err := n.enrichDiscovery(native)
	if err != nil || used || fallback.Entities[0].Name != "Native" {
		t.Fatal("native fallback unavailable", fallback, used, err)
	}
	mode.Store(2)
	n.probeBridge(context.Background(), now.Add(93*time.Second))
	if n.BridgeSnapshot().Status != "incompatible" {
		t.Fatal(n.BridgeSnapshot())
	}
	mode.Store(1)
	n.probeBridge(context.Background(), now.Add(124*time.Second))
	if n.BridgeSnapshot().Status != "available" || ordered.Load() != 0 || hello.Load() != 5 {
		t.Fatal(n.BridgeSnapshot(), ordered.Load(), hello.Load())
	}
	if sources.Selected() != ha.NativeSource {
		t.Fatal("Bridge mutated native state")
	}
}
func TestBridgeSafeVersionAndOrderedStateGate(t *testing.T) {
	// Untrusted optional metadata is bounded; production state selection stays gated.
	n := NewNative("", nil)
	defer n.core.Close()
	defer n.bridgeCore.Close()
	if safeBridgeVersion("secret\ninvalid") != "unknown" {
		t.Fatal("unsafe version metadata exposed")
	}
	if orderedBridgeEnabled {
		t.Fatal("unproven ordered state activated")
	}
}
