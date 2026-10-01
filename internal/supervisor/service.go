package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/housefold/runtime/internal/estate"
	"github.com/housefold/runtime/internal/hacontrol"
	"html/template"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	shutdownTimeout = 8 * time.Second
	ingressPeer     = "172.30.32.2"
)

// HAStatus contains coarse session/cache metadata only. Entity data is never
// copied into this store or rendered by the local status page.
type HAStatus struct {
	Phase          string
	Connection     string
	Freshness      string
	Generation     uint64
	EntityCount    int
	LastSuccessful time.Time
}

// StatusStore keeps the latest session metadata in memory for the local status
// page. No persistence or household state is involved.
type StatusStore struct {
	mu       sync.RWMutex
	status   HAStatus
	recovery bool
	estate   interface{ Snapshot() estate.Snapshot }
	bridge   interface{ BridgeSnapshot() hacontrol.BridgeInfo }
}

func (s *StatusStore) SetBridge(b interface{ BridgeSnapshot() hacontrol.BridgeInfo }) {
	s.mu.Lock()
	s.bridge = b
	s.mu.Unlock()
}
func (s *StatusStore) Bridge() hacontrol.BridgeInfo {
	s.mu.RLock()
	b := s.bridge
	s.mu.RUnlock()
	if b == nil {
		return hacontrol.BridgeInfo{Status: "unavailable", StateSource: "native"}
	}
	return b.BridgeSnapshot()
}
func (s *StatusStore) SetEstate(owner interface{ Snapshot() estate.Snapshot }) {
	s.mu.Lock()
	s.estate = owner
	s.mu.Unlock()
}
func (s *StatusStore) Estate() estate.Snapshot {
	s.mu.RLock()
	owner := s.estate
	s.mu.RUnlock()
	if owner == nil {
		return estate.Snapshot{Phase: "unavailable"}
	}
	return owner.Snapshot()
}
func (s *StatusStore) SetRecoveryRequired()   { s.mu.Lock(); s.recovery = true; s.mu.Unlock() }
func (s *StatusStore) RecoveryRequired() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.recovery }

func (s *StatusStore) UpdateHA(phase, connection, freshness string, generation uint64, entityCount int, lastSuccessful time.Time) {
	s.mu.Lock()
	s.status = HAStatus{
		Phase:          safeHAPhase(phase),
		Connection:     safeHAConnection(connection),
		Freshness:      safeFreshness(freshness),
		Generation:     generation,
		EntityCount:    max(0, entityCount),
		LastSuccessful: lastSuccessful.UTC(),
	}
	s.mu.Unlock()
}

func (s *StatusStore) HA() HAStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// Service serves process health and an ingress-only read-only status page.
type Service struct {
	stopping atomic.Bool
	status   *StatusStore
}

func NewService(status *StatusStore) *Service {
	if status == nil {
		status = &StatusStore{}
	}
	return &Service{status: status}
}

// Run serves on listener until ctx is canceled or the server fails. The caller
// owns listener creation so tests and embedding code can select an address.
func (s *Service) Run(ctx context.Context, listener net.Listener) error {
	server := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 2 * time.Second,
		WriteTimeout:      2 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(&boundedListener{Listener: listener, slots: make(chan struct{}, MaxConnections)})
	}()

	select {
	case err := <-serveDone:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		s.stopping.Store(true)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return err
		}
		err := <-serveDone
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Service) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.URL.Path == "/healthz" {
			s.serveHealth(w, r)
			return
		}
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if !isIngressPeer(r.RemoteAddr) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if s.stopping.Load() {
			http.Error(w, "runtime is stopping", http.StatusServiceUnavailable)
			return
		}
		s.serveStatus(w)
	})
}

func (s *Service) serveHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status := "healthy"
	code := http.StatusOK
	if s.status.RecoveryRequired() {
		status = "recovery_required"
		code = http.StatusServiceUnavailable
	}
	if s.stopping.Load() {
		status = "unavailable"
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(struct {
		Status string `json:"status"`
	}{Status: status})
}

type statusPageData struct {
	Runtime        string
	Estate         estate.Snapshot
	Bridge         hacontrol.BridgeInfo
	Phase          string
	Connection     string
	Freshness      string
	Generation     uint64
	EntityCount    int
	LastSuccessful string
}

var statusPage = template.Must(template.New("status").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="light dark">
  <title>Housefold Runtime status</title>
  <style>
    body { font: 1rem/1.5 system-ui, sans-serif; margin: 2rem auto; max-width: 48rem; padding: 0 1rem; }
    h1 { line-height: 1.2; }
    section { border: 1px solid; border-radius: .5rem; margin: 1rem 0; padding: 1rem; }
    dt { font-weight: 650; }
    dd { margin: 0 0 .75rem; }
  </style>
</head>
<body>
  <main>
    <h1>Housefold Runtime</h1>
    <section aria-labelledby="runtime-heading">
      <h2 id="runtime-heading">Runtime</h2>
      <p>{{.Runtime}}</p>
    </section>
    <section aria-labelledby="ha-heading">
      <h2 id="ha-heading">Home Assistant</h2>
      <dl>
		<dt>Connection phase</dt><dd>{{.Phase}}</dd>
		<dt>WebSocket status</dt><dd>{{.Connection}}</dd>
		<dt>State freshness</dt><dd>{{.Freshness}}</dd>
		<dt>Generation</dt><dd>{{.Generation}}</dd>
		<dt>Entity count</dt><dd>{{.EntityCount}}</dd>
		<dt>Last successful sync</dt><dd>{{.LastSuccessful}}</dd>
      </dl>
	  <p>Runtime process health is independent of Home Assistant availability. Cached state remains stale until a complete new generation is synchronized.</p>
    </section>
    <section aria-labelledby="bridge-heading"><h2 id="bridge-heading">Optional Bridge</h2><p>{{.Bridge.Status}} · Bridge {{.Bridge.BridgeVersion}} · Core {{.Bridge.CoreVersion}}</p><p>{{.Bridge.Guidance}}</p><p>State source: {{.Bridge.StateSource}}. {{.Bridge.Limitation}}</p></section>
    <section aria-labelledby="modules-heading"><h2 id="modules-heading">Modules</h2>
    <p>Estate: {{.Estate.Phase}}. Pressure: {{.Estate.Resources.Pressure}}. Runtime RSS KiB: {{.Estate.Resources.Host.Runtime.RSSKiB}}. Managed RSS KiB: {{.Estate.Resources.Managed.RSSKiB}}. Storage bytes: {{.Estate.Resources.StorageBytes}}. Free bytes: {{.Estate.Resources.FreeBytes}}. Storage writes degraded: {{.Estate.StorageDegraded}}</p>
    {{range .Estate.Modules}}<p>{{.Identity}} {{.Version}} · {{.Phase}} · enabled: {{.Desired}} · service: {{.ServiceHealthy}} · UI: {{.UIHealthy}} · RSS KiB: {{.Usage.RSSKiB}} / {{.Limits.MemoryKiB}} · CPU: {{.Usage.CPUPercent}} / {{.Limits.CPUPercent}}% · threads: {{.Usage.Threads}} / {{.Limits.Threads}} · FDs: {{.Usage.FDs}} / {{.Limits.FDs}} · priority: {{.Limits.Priority}} · {{.Error}}</p>{{else}}<p>No installed modules.</p>{{end}}
    </section>
    <p>For app recovery, use Home Assistant Supervisor controls and logs. If Home Assistant is unavailable, use the HAOS host console and <code>ha apps</code> commands.</p>
  </main>
</body>
</html>`))

func (s *Service) serveStatus(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'self'")
	current := HAStatus{}
	if s.status != nil {
		current = s.status.HA()
	}
	page := statusPageData{Bridge: s.status.Bridge(), Estate: s.status.Estate(), Runtime: "Healthy", Phase: "disconnected", Connection: "unavailable", Freshness: "none", LastSuccessful: "Not yet synchronized"}
	if s.status.RecoveryRequired() {
		page.Runtime = "Recovery required: private storage unavailable"
	}
	if current.Phase != "" {
		page.Phase = current.Phase
		page.Connection = current.Connection
		page.Freshness = current.Freshness
		page.Generation = current.Generation
		page.EntityCount = current.EntityCount
		if !current.LastSuccessful.IsZero() {
			page.LastSuccessful = current.LastSuccessful.Format(time.RFC3339)
		}
	}
	_ = statusPage.Execute(w, page)
}

func safeHAPhase(status string) string {
	switch strings.ToLower(status) {
	case "disconnected", "connecting", "authenticating", "subscribing", "syncing", "ready":
		return status
	default:
		return "disconnected"
	}
}

func safeHAConnection(status string) string {
	switch status {
	case "connected", "denied", "unavailable":
		return status
	default:
		return "unavailable"
	}
}

func safeFreshness(value string) string {
	switch value {
	case "none", "synchronizing", "fresh", "stale":
		return value
	default:
		return "none"
	}
}

func isIngressPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = strings.Trim(remoteAddr, "[]")
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.Equal(net.ParseIP(ingressPeer))
}
