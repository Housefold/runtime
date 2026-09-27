package ha

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const testToken = "test-secret-token"

func TestProbeRESTSuccessAndWebSocketSuccess(t *testing.T) {
	server := newProbeServer(t, http.StatusOK, "auth_ok")
	result := probeServer(t, server)
	if result != (Result{Status: StatusConnected}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestProbeRESTDenial(t *testing.T) {
	server := newProbeServer(t, http.StatusUnauthorized, "")
	result := probeServer(t, server)
	if result != (Result{Status: StatusDenied, ErrorCategory: "rest_authentication_rejected"}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestProbeRESTUnavailable(t *testing.T) {
	server := newProbeServer(t, http.StatusServiceUnavailable, "")
	result := probeServer(t, server)
	if result != (Result{Status: StatusUnavailable, ErrorCategory: "rest_http_error"}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestProbeRESTTransportUnavailable(t *testing.T) {
	result := probeAt(context.Background(), testToken, "http://127.0.0.1:1/api/", "ws://127.0.0.1:1/websocket")
	if result != (Result{Status: StatusUnavailable, ErrorCategory: "rest_transport_error"}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestProbeMissingTokenDoesNotMakeRESTRequest(t *testing.T) {
	var restHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		restHits.Add(1)
	}))
	t.Cleanup(server.Close)
	result := probeAt(context.Background(), " \t", server.URL+"/api/", "ws://127.0.0.1:1/websocket")
	if result != (Result{Status: StatusUnavailable, ErrorCategory: "token_unavailable"}) {
		t.Fatalf("result = %#v", result)
	}
	if hits := restHits.Load(); hits != 0 {
		t.Fatalf("REST request count = %d, want 0", hits)
	}
}

func TestProbeWebSocketAuthenticationDenied(t *testing.T) {
	server := newProbeServer(t, http.StatusOK, "auth_invalid")
	result := probeServer(t, server)
	if result != (Result{Status: StatusDenied, ErrorCategory: "websocket_authentication_rejected"}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestProbeWebSocketUnavailable(t *testing.T) {
	restServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(restServer.Close)
	result := probeAt(context.Background(), testToken, restServer.URL+"/api/", "ws://127.0.0.1:1/websocket")
	if result != (Result{Status: StatusUnavailable, ErrorCategory: "websocket_transport_error"}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestProbeWebSocketRedirectIsNotFollowed(t *testing.T) {
	var redirectedHits atomic.Int32
	redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedHits.Add(1)
		_, _ = w.Write([]byte(r.Header.Get("Authorization") + r.URL.RawQuery))
	}))
	t.Cleanup(redirected.Close)
	var target *url.URL
	var err error
	if target, err = url.Parse(redirected.URL); err != nil {
		t.Fatal(err)
	}
	target.Path = "/websocket"
	var sourceHits atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceHits.Add(1)
		http.Redirect(w, r, target.String(), http.StatusTemporaryRedirect)
	}))
	t.Cleanup(source.Close)
	rest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(rest.Close)
	wsURL := "ws" + strings.TrimPrefix(source.URL, "http") + "/websocket"
	result := probeAt(context.Background(), testToken, rest.URL+"/api/", wsURL)
	if result != (Result{Status: StatusUnavailable, ErrorCategory: "websocket_transport_error"}) {
		t.Fatalf("result = %#v", result)
	}
	if sourceHits.Load() != 1 {
		t.Fatalf("source handshake count = %d, want 1", sourceHits.Load())
	}
	if hits := redirectedHits.Load(); hits != 0 {
		t.Fatalf("redirect target received %d requests", hits)
	}
}

func TestProbeNeverReturnsSecretsOrEndpointDetails(t *testing.T) {
	server := newProbeServer(t, http.StatusOK, "auth_invalid")
	result := probeServer(t, server)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{testToken, server.URL, "127.0.0.1", "authorization"} {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(forbidden)) {
			t.Fatalf("result leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestProbeRedirectIsNotFollowed(t *testing.T) {
	var redirected atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("REST request did not carry the configured token")
		}
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/elsewhere", func(http.ResponseWriter, *http.Request) {
		redirected.Store(true)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	result := probeAt(context.Background(), testToken, server.URL+"/api/", "ws://127.0.0.1:1/websocket")
	if result != (Result{Status: StatusUnavailable, ErrorCategory: "rest_http_error"}) {
		t.Fatalf("result = %#v", result)
	}
	if redirected.Load() {
		t.Fatal("client followed REST redirect")
	}
}

func TestProbeTimeoutCoversWebSocketAfterREST(t *testing.T) {
	websocketStarted := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
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
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
		close(websocketStarted)
		time.Sleep(100 * time.Millisecond)
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"type":"auth_ok"}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/websocket"
	startedAt := time.Now()
	result := probeAtWithTimeout(context.Background(), testToken, server.URL+"/api/", wsURL, 30*time.Millisecond)
	if result != (Result{Status: StatusUnavailable, ErrorCategory: "websocket_transport_error"}) {
		t.Fatalf("result = %#v", result)
	}
	select {
	case <-websocketStarted:
	case <-time.After(time.Second):
		t.Fatal("WebSocket phase did not start")
	}
	if elapsed := time.Since(startedAt); elapsed > 300*time.Millisecond {
		t.Fatalf("probe exceeded its shared timeout: %s", elapsed)
	}
}

func newProbeServer(t *testing.T, restStatus int, wsAuthResult string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+testToken {
			http.Error(w, "sensitive response detail", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(restStatus)
		_, _ = w.Write([]byte("sensitive response detail"))
	})
	if wsAuthResult != "" {
		mux.HandleFunc("/websocket", func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.CloseNow()
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"auth_required"}`)); err != nil {
				t.Errorf("write auth_required: %v", err)
				return
			}
			_, message, err := conn.Read(ctx)
			if err != nil {
				t.Errorf("read auth message: %v", err)
				return
			}
			var auth struct {
				Type  string `json:"type"`
				Token string `json:"access_token"`
			}
			if err := json.Unmarshal(message, &auth); err != nil {
				t.Errorf("decode auth message: %v", err)
				return
			}
			if auth.Type != "auth" || auth.Token != testToken {
				t.Errorf("unexpected auth envelope")
				return
			}
			payload, _ := json.Marshal(map[string]string{"type": wsAuthResult})
			if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
				t.Errorf("write auth result: %v", err)
				return
			}
			_, message, err = conn.Read(ctx)
			if err == nil {
				t.Errorf("received follow-up WebSocket message: %s", message)
				return
			}
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestProbeDoesNotWaitForWebSocketCloseAcknowledgement(t *testing.T) {
	peerHoldingOpen := make(chan struct{})
	releasePeer := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releasePeer) }) }
	t.Cleanup(release)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
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
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
		if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"auth_ok"}`)); err != nil {
			return
		}
		// Do not read or acknowledge a WebSocket close until after the client
		// should already have returned from the one-shot health probe.
		close(peerHoldingOpen)
		<-releasePeer
		_, _, _ = conn.Read(ctx)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/websocket"
	resultCh := make(chan Result, 1)
	go func() { resultCh <- probeAt(context.Background(), testToken, server.URL+"/api/", wsURL) }()
	select {
	case <-peerHoldingOpen:
	case <-time.After(5 * time.Second):
		t.Fatal("peer did not reach the unacknowledged close state")
	}
	var result Result
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		release()
		t.Fatal("probe waited for peer close acknowledgement")
	}
	release()
	if result != (Result{Status: StatusConnected}) {
		t.Fatalf("result = %#v", result)
	}
}

func probeServer(t *testing.T, server *httptest.Server) Result {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/websocket"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return probeAt(ctx, testToken, server.URL+"/api/", wsURL)
}
