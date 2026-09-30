package ha

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestStateSessionMissingTokenMakesNoRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	s := newStateSession("ws"+strings.TrimPrefix(server.URL, "http"), func(context.Context, time.Duration) bool { return false })
	s.Run(context.Background(), " \t")
	if requests.Load() != 0 || s.Metadata().Status != StatusUnavailable || s.Metadata().Fresh {
		t.Fatal("missing token attempted transport or became ready")
	}
}
func TestRESTSuccessCannotOverrideDeniedStateSession(t *testing.T) {
	var restRequests atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { restRequests.Add(1); w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/websocket", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) })
	server := httptest.NewServer(mux)
	defer server.Close()
	s := newStateSession("ws"+strings.TrimPrefix(server.URL, "http")+"/websocket", func(context.Context, time.Duration) bool { return false })
	s.Run(context.Background(), "synthetic")
	if s.Metadata().Status != StatusDenied || s.Metadata().Fresh || restRequests.Load() != 0 {
		t.Fatal("REST affected operational readiness")
	}
}
