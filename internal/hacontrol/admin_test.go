package hacontrol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

func TestAdminLookupAgainstAuthenticatedCoreRevocationAndFailures(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	var mu sync.Mutex
	row := map[string]any{"id": id, "is_active": true, "is_owner": false, "system_generated": false, "group_ids": []string{"system-admin"}, "name": "PRIVATE_NAME", "credentials": []string{"PRIVATE_CREDENTIAL"}}
	success, duplicate := true, false
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
		if auth["access_token"] != "SYNTHETIC_ADMIN_ADAPTER" {
			t.Error("adapter token missing")
			return
		}
		_ = writeJSON(ctx, conn, map[string]any{"type": "auth_ok"})
		for {
			_, raw, err = conn.Read(ctx)
			if err != nil {
				return
			}
			var cmd struct {
				ID   uint64 `json:"id"`
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &cmd)
			if cmd.Type != "config/auth/list" {
				t.Error("unexpected admin command")
				return
			}
			mu.Lock()
			rows := []any{row}
			if duplicate {
				rows = append(rows, row)
			}
			err = writeJSON(ctx, conn, map[string]any{"id": cmd.ID, "type": "result", "success": success, "result": rows})
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}))
	defer server.Close()
	n := NewNative("SYNTHETIC_ADMIN_ADAPTER", nil)
	n.adminCore = newCore("SYNTHETIC_ADMIN_ADAPTER", "ws"+strings.TrimPrefix(server.URL, "http"), localHTTP())
	defer n.adminCore.Close()
	if err := n.AuthorizeAdmin(context.Background(), id); err != nil {
		t.Fatal("admin denied", err)
	}
	for _, tc := range []struct {
		name, key string
		value     any
	}{
		{"revoked", "group_ids", []string{"system-users"}},
		{"inactive", "is_active", false},
		{"system user", "system_generated", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			old := row[tc.key]
			row[tc.key] = tc.value
			mu.Unlock()
			if n.AuthorizeAdmin(context.Background(), id) == nil {
				t.Fatal("denied role accepted")
			}
			mu.Lock()
			row[tc.key] = old
			mu.Unlock()
		})
	}
	mu.Lock()
	row["is_owner"] = true
	row["group_ids"] = []string{}
	mu.Unlock()
	if n.AuthorizeAdmin(context.Background(), id) != nil {
		t.Fatal("owner denied")
	}
	mu.Lock()
	success = false
	mu.Unlock()
	if n.AuthorizeAdmin(context.Background(), id) == nil {
		t.Fatal("unverifiable accepted")
	}
	mu.Lock()
	success = true
	duplicate = true
	mu.Unlock()
	if n.AuthorizeAdmin(context.Background(), id) == nil {
		t.Fatal("duplicate identity accepted")
	}
	if n.AuthorizeAdmin(context.Background(), "admin") == nil {
		t.Fatal("malformed ID")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n.AuthorizeAdmin(ctx, id) == nil {
		t.Fatal("cancel ignored")
	}
}
