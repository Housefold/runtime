package supervisor

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunServesHealthyHealthCheck(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	service := NewService(nil)
	go func() { done <- service.Run(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("service did not stop")
		}
	})

	client := &http.Client{Timeout: time.Second}
	var response *http.Response
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		response, err = client.Get("http://" + listener.Addr().String() + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["status"] != "healthy" {
		t.Fatalf("unexpected health body: %#v", body)
	}
}

func TestHealthRouteRejectsOtherMethodsAndPaths(t *testing.T) {
	service := NewService(nil)
	for _, tc := range []struct {
		method string
		path   string
		want   int
	}{
		{method: http.MethodPost, path: "/healthz", want: http.StatusMethodNotAllowed},
		{method: http.MethodHead, path: "/", want: http.StatusMethodNotAllowed},
		{method: http.MethodGet, path: "/control", want: http.StatusNotFound},
	} {
		request := httptest.NewRequest(tc.method, tc.path, nil)
		request.RemoteAddr = ingressPeer + ":4567"
		response := httptest.NewRecorder()
		service.handler().ServeHTTP(response, request)
		if response.Code != tc.want {
			t.Errorf("%s %s status = %d, want %d", tc.method, tc.path, response.Code, tc.want)
		}
	}
}

func TestStoppingHealthIsUnavailable(t *testing.T) {
	service := NewService(nil)
	service.stopping.Store(true)
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	service.handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if response.Body.String() != "{\"status\":\"unavailable\"}\n" {
		t.Fatalf("unexpected health response: %q", response.Body.String())
	}
}

func TestRunReturnsListenerStartupFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := NewService(nil).Run(context.Background(), listener); err == nil {
		t.Fatal("Run returned nil for a closed listener")
	}
}

func TestCancellationClosesListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- NewService(nil).Run(ctx, listener) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if _, err := net.DialTimeout("tcp", address, 100*time.Millisecond); err == nil {
		t.Fatal("listener accepted a connection after shutdown")
	}
}

func TestStatusPageRequiresIngressPeerAndOnlyShowsCoarseStatus(t *testing.T) {
	checkedAt := time.Date(2026, time.September, 27, 14, 0, 0, 0, time.FixedZone("test", 3600))
	store := &StatusStore{}
	store.UpdateHA("ready", "connected", "fresh", 7, 42, checkedAt)
	service := NewService(store)

	for _, tc := range []struct {
		name       string
		remoteAddr string
		forwarded  string
		method     string
		path       string
		want       int
	}{
		{name: "ingress peer", remoteAddr: "172.30.32.2:4567", method: http.MethodGet, path: "/", want: http.StatusOK},
		{name: "nearby non-ingress peer", remoteAddr: "172.30.32.20:4567", method: http.MethodGet, path: "/", want: http.StatusForbidden},
		{name: "ipv6 peer", remoteAddr: "[::1]:4567", method: http.MethodGet, path: "/", want: http.StatusForbidden},
		{name: "forwarded identity is ignored", remoteAddr: "172.30.32.20:4567", forwarded: "172.30.32.2", method: http.MethodGet, path: "/", want: http.StatusForbidden},
		{name: "ingress post rejected", remoteAddr: "172.30.32.2:4567", method: http.MethodPost, path: "/", want: http.StatusMethodNotAllowed},
		{name: "unknown path rejected", remoteAddr: "172.30.32.2:4567", method: http.MethodGet, path: "/status", want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(tc.method, tc.path, nil)
			request.RemoteAddr = tc.remoteAddr
			if tc.forwarded != "" {
				request.Header.Set("X-Forwarded-For", tc.forwarded)
				request.Header.Set("X-Remote-User", "admin")
			}
			response := httptest.NewRecorder()
			service.handler().ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%q", response.Code, tc.want, response.Body.String())
			}
			if tc.want == http.StatusOK {
				body := response.Body.String()
				for _, want := range []string{"Connection phase", "ready", "WebSocket status", "connected", "State freshness", "fresh", "Generation", "7", "Entity count", "42", "2026-09-27T13:00:00Z", "process health is independent", "remains stale"} {
					if !strings.Contains(body, want) {
						t.Errorf("page missing %q", want)
					}
				}
				for _, forbidden := range []string{"X-Remote-User", "token", "entity_id", "must-not-appear", "living_room", "secret_attribute"} {
					if strings.Contains(body, forbidden) {
						t.Errorf("page contains forbidden data %q", forbidden)
					}
				}
				if response.Header().Get("Cache-Control") != "no-store" {
					t.Errorf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
				}
			}
		})
	}
}

func TestStatusPageHandlesUncheckedAndUnknownHAStatus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		phase     string
		freshness string
		want      string
	}{
		{name: "not checked", want: "Not yet synchronized"},
		{name: "unknown phase and freshness are normalized", phase: `<script>alert("x")</script>`, freshness: `<img src=x>`, want: "disconnected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &StatusStore{}
			if tc.phase != "" {
				store.UpdateHA(tc.phase, "unavailable", tc.freshness, 0, 0, time.Time{})
			}
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.RemoteAddr = "172.30.32.2:4567"
			response := httptest.NewRecorder()
			NewService(store).handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			if !strings.Contains(response.Body.String(), tc.want) {
				t.Fatalf("page does not contain %q: %s", tc.want, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "<script>") {
				t.Fatal("untrusted status was rendered as markup")
			}
		})
	}
}

func TestStatusPageRendersEveryHAConnectionResult(t *testing.T) {
	for _, phase := range []string{"disconnected", "connecting", "authenticating", "subscribing", "syncing", "ready"} {
		t.Run(phase, func(t *testing.T) {
			store := &StatusStore{}
			store.UpdateHA(phase, "connected", "synchronizing", 0, 0, time.Date(2026, time.September, 27, 14, 0, 0, 0, time.UTC))
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.RemoteAddr = ingressPeer + ":4567"
			response := httptest.NewRecorder()
			NewService(store).handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			if !strings.Contains(response.Body.String(), phase) || !strings.Contains(response.Body.String(), "2026-09-27T14:00:00Z") {
				t.Fatalf("page did not render phase and timestamp: %s", response.Body.String())
			}
		})
	}
}

func TestHealthRouteRemainsAvailableToSupervisorPeer(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.RemoteAddr = "172.30.32.1:4567"
	response := httptest.NewRecorder()
	NewService(nil).handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.Code)
	}
}
