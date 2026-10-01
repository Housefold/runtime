package hacontrol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/housefold/runtime/internal/discovery"
	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/ha"
	"github.com/housefold/runtime/internal/state"
)

func sourceFixture(t *testing.T, source *ha.Sources, renamed bool) {
	t.Helper()
	id := "light.synthetic"
	if renamed {
		id = "light.renamed"
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entities := []state.Entity{{EntityID: id, State: "SYNTHETIC_VALUE_NOT_DISCOVERY", Attributes: map[string]any{"friendly_name": "Synthetic Light", "brightness": json.Number("120")}, LastChanged: now, LastUpdated: now}, {EntityID: "unknown_domain.synthetic", State: "PRIVATE_SYNTHETIC", Attributes: map[string]any{"custom": []any{"SYNTHETIC_VALUE"}}, LastChanged: now, LastUpdated: now}}
	w, err := source.Begin(ha.NativeSource, "epoch", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Stage(entities); err != nil {
		t.Fatal(err)
	}
	if err = w.Complete("epoch", 10); err != nil {
		t.Fatal(err)
	}
	if err = w.Commit(); err != nil {
		t.Fatal(err)
	}
}
func TestNativeDiscoveryStableRenameWeakUnknownAndBindings(t *testing.T) {
	sources := ha.NewSources()
	sourceFixture(t, sources, false)
	var rename, deny, overflow atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/services" {
			if r.Header.Get("Authorization") != "Bearer SYNTHETIC" {
				t.Error("services auth")
			}
			_, _ = w.Write([]byte(`[{"domain":"unknown_domain","services":{"custom":{"fields":{"payload":{"required":true,"selector":{"object":{}}}}}}}]`))
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		_ = writeJSON(ctx, conn, map[string]any{"type": "auth_required"})
		_, _, err = conn.Read(ctx)
		if err != nil {
			return
		}
		_ = writeJSON(ctx, conn, map[string]any{"type": "auth_ok"})
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var command struct {
				ID   uint64 `json:"id"`
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &command)
			if deny.Load() {
				_ = writeJSON(ctx, conn, map[string]any{"id": command.ID, "type": "result", "success": false, "error": map[string]string{"code": "unauthorized"}})
				continue
			}
			var rows any
			switch command.Type {
			case "config/entity_registry/list":
				id := "light.synthetic"
				if rename.Load() {
					id = "light.renamed"
				}
				rows = []registryEntity{{ID: "stable-fixture", EntityID: id, DeviceID: "device-fixture"}}
				if overflow.Load() {
					rows = make([]registryEntity, discovery.MaxEntities+1)
				}
			case "config/device_registry/list":
				rows = []registryDevice{{ID: "device-fixture", Name: "Synthetic Device", AreaID: "area-fixture"}}
			case "config/area_registry/list":
				rows = []registryArea{{ID: "area-fixture", Name: "Synthetic Area"}}
			default:
				t.Error("unexpected native discovery command", command.Type)
				return
			}
			_ = writeJSON(ctx, conn, map[string]any{"id": command.ID, "type": "result", "success": true, "result": rows})
		}
	}))
	defer server.Close()
	n := NewNative("SYNTHETIC", sources.View())
	n.base = server.URL + "/api/"
	n.core = newCore("SYNTHETIC", "ws"+strings.TrimPrefix(server.URL, "http"), localHTTP())
	defer n.core.Close()
	defer n.http.CloseIdleConnections()
	generator, err := discovery.OpenGenerator(durable.NewFile(filepath.Join(t.TempDir(), "bindings")))
	if err != nil {
		t.Fatal(err)
	}
	var generated []byte
	n.SetDiscoverySink(func(s discovery.Snapshot) error { source, err := generator.Generate(s); generated = source; return err })
	if err = n.CollectDiscovery(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, fresh := n.DiscoverySnapshot()
	if !fresh || snapshot.EntityRegistryStatus != discovery.Available || len(snapshot.Entities) != 2 || len(snapshot.Services) != 1 {
		t.Fatal(snapshot, fresh)
	}
	var stable string
	for _, entity := range snapshot.Entities {
		if entity.EntityID == "light.synthetic" {
			if entity.Identity.Weak || entity.Area != "area-fixture" {
				t.Fatal(entity)
			}
			stable = entity.Identity.Key
		} else if !entity.Identity.Weak || entity.Domain != "unknown_domain" {
			t.Fatal("generic unknown lost", entity)
		}
	}
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), "SYNTHETIC_VALUE") || strings.Contains(string(generated), "PRIVATE_SYNTHETIC") || !strings.Contains(string(generated), "GenericEntity") {
		t.Fatal("household values or generic access contract")
	}
	rename.Store(true)
	sourceFixture(t, sources, true)
	if err = n.CollectDiscovery(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = n.DiscoverySnapshot()
	for _, entity := range snapshot.Entities {
		if entity.EntityID == "light.renamed" && entity.Identity.Key != stable {
			t.Fatal("rename lost stable identity")
		}
	}
	if !strings.Contains(string(generated), "var SyntheticLight=") && !strings.Contains(string(generated), "var SyntheticLight =") {
		t.Fatal("binding symbol not stable", string(generated))
	}
	overflow.Store(true)
	if err = n.CollectDiscovery(context.Background()); err == nil {
		t.Fatal("discovery bounds ignored")
	}
	snapshot, fresh = n.DiscoverySnapshot()
	if fresh || len(snapshot.Entities) != 2 {
		t.Fatal("overflow discarded last facts")
	}
	overflow.Store(false)
	deny.Store(true)
	if err = n.CollectDiscovery(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, fresh = n.DiscoverySnapshot()
	if !fresh || snapshot.EntityRegistryStatus != discovery.Redacted || snapshot.DeviceRegistryStatus != discovery.Redacted {
		t.Fatal("redaction silently successful empty", snapshot)
	}
	for _, entity := range snapshot.Entities {
		if !entity.Identity.Weak {
			t.Fatal("fabricated stable identity without registry")
		}
	}
}
