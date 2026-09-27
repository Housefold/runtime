package ha

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestMonitorReconnectsWhenHARecovers(t *testing.T) {
	var available atomic.Bool
	var restRequests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		restRequests.Add(1)
		if !available.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/websocket", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"auth_required"}`)); err != nil {
			return
		}
		_, message, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var auth struct {
			Type  string `json:"type"`
			Token string `json:"access_token"`
		}
		if err := json.Unmarshal(message, &auth); err != nil || auth.Type != "auth" || auth.Token != testToken {
			return
		}
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"auth_ok"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/websocket"
	probe := func(ctx context.Context) Result {
		return probeAt(ctx, testToken, server.URL+"/api/", wsURL)
	}
	var logOutput bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logOutput, nil))
	var delays []time.Duration
	waitCount := 0
	monitor(context.Background(), probe, func(_ context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		waitCount++
		if waitCount == 1 {
			available.Store(true)
			return true
		}
		return false
	}, logger)

	if got := restRequests.Load(); got != 2 {
		t.Fatalf("REST probe count = %d, want 2", got)
	}
	if len(delays) != 2 || delays[0] != time.Second || delays[1] != connectedPoll {
		t.Fatalf("wait delays = %v, want [%s %s]", delays, time.Second, connectedPoll)
	}
	lines := strings.Split(strings.TrimSpace(logOutput.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"status":"unavailable"`) || !strings.Contains(lines[1], `"status":"connected"`) {
		t.Fatalf("unexpected connection transition log: %s", logOutput.String())
	}
}
