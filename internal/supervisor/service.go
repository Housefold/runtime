package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/housefold/runtime/internal/catalog"
	"github.com/housefold/runtime/internal/diagnostics"
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
	mu         sync.RWMutex
	status     HAStatus
	recovery   bool
	estate     interface{ Snapshot() estate.Snapshot }
	bridge     interface{ BridgeSnapshot() hacontrol.BridgeInfo }
	management Management
	admin      AdminAuthorizer
	build      BuildInfo
	logs       interface{ Snapshot() diagnostics.Snapshot }
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

// Service serves Runtime health and the HA-admin ingress BIOS.
type Service struct {
	stopping   atomic.Bool
	status     *StatusStore
	mu         sync.Mutex
	tickets    map[string]approval
	job        operationStatus
	working    bool
	workCtx    context.Context
	workCancel context.CancelFunc
	workers    sync.WaitGroup
}

func NewService(status *StatusStore) *Service {
	if status == nil {
		status = &StatusStore{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{status: status, tickets: map[string]approval{}, workCtx: ctx, workCancel: cancel}
}

// Run serves on listener until ctx is canceled or the server fails. The caller
// owns listener creation so tests and embedding code can select an address.
func (s *Service) Run(ctx context.Context, listener net.Listener) error {
	s.mu.Lock()
	s.workCancel()
	s.workCtx, s.workCancel = context.WithCancel(ctx)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.workCancel(); s.mu.Unlock(); s.workers.Wait() }()
	server := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		MaxHeaderBytes:    16 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
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
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'self'; frame-ancestors 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.URL.Path == "/healthz" {
			s.serveHealth(w, r)
			return
		}
		switch r.URL.Path {
		case "/", "/manage", "/review", "/diagnostics":
		default:
			http.NotFound(w, r)
			return
		}
		if !isIngressPeer(r.RemoteAddr) {
			http.Error(w, "forbidden", 403)
			return
		}
		id, err := s.authorize(r)
		if err != nil {
			http.Error(w, "HA administrator authorization unavailable or denied", 403)
			return
		}
		if s.stopping.Load() {
			http.Error(w, "runtime is stopping", 503)
			return
		}
		switch r.URL.Path {
		case "/manage":
			s.manage(w, r, id)
		case "/review":
			s.review(w, r, id)
		case "/diagnostics":
			s.diagnostics(w, r)
		case "/":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", "GET")
				http.Error(w, "method not allowed", 405)
				return
			}
			csrf, err := s.issue(id)
			if err != nil {
				http.Error(w, "management unavailable", 503)
				return
			}
			s.serveStatus(w, csrf)
		}
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
	CSRF           string
	Catalog        catalog.Snapshot
	Job            operationStatus
	Logs           diagnostics.Snapshot
	Audit          estate.AuditSnapshot
}

var statusPage = template.Must(template.New("status").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="light dark">
  <title>Housefold Runtime BIOS</title>
  <style>
    body { font: 1rem/1.5 ui-monospace, monospace; margin: 2rem auto; max-width: 72rem; padding: 0 1rem; }
    h1 { line-height: 1.2; }
    section { border: 1px solid; border-radius: .5rem; margin: 1rem 0; padding: 1rem; }
    dt { font-weight: 650; }
    dd { margin: 0 0 .75rem; }
  </style>
</head>
<body>
  <main>
    <h1>Housefold Runtime BIOS</h1>
 <p><a href="./">Refresh status</a> · <a href="./diagnostics">Download diagnostics</a> · <a href="/hassio/dashboard" target="_top" rel="noreferrer">Home Assistant App lifecycle, update, backup and live logs</a></p>
 {{if .Job.Operation}}<p>Last operation: {{.Job.Operation}} · {{.Job.Subject}} · {{.Job.State}} · {{.Job.Result}}</p>{{end}}
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
    {{range .Estate.Modules}}<article><p>{{.Identity}} {{.Version}} · {{.Phase}} · enabled: {{.Desired}} · service: {{.ServiceHealthy}} · UI: {{.UIHealthy}} · RSS KiB: {{.Usage.RSSKiB}} / {{.Limits.MemoryKiB}} · CPU: {{.Usage.CPUPercent}} / {{.Limits.CPUPercent}}% · threads: {{.Usage.Threads}} / {{.Limits.Threads}} · FDs: {{.Usage.FDs}} / {{.Limits.FDs}} · priority: {{.Limits.Priority}} · {{.Error}}</p><p>Retained version: {{.RetainedVersion}} · dependencies: {{range .Dependencies}}{{.Identity}} {{.Version}} (optional: {{.Optional}}) {{end}} · capabilities: {{range .Capabilities}}{{.}} {{end}} · outbound: {{range .Outbound}}{{.}} {{end}}</p>
 <form action="./manage" method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}"><input type="hidden" name="module" value="{{.Identity}}">
 {{if .Installed}}<button name="operation" value="start">Start / enable on Runtime boot</button><button name="operation" value="stop">Stop / disable</button><button name="operation" value="restart">Restart</button><button name="operation" value="recover">Recover quarantine</button><button name="operation" value="rollback">Roll back to retained version</button><button name="operation" value="remove">Remove (preserve data)</button>{{end}}
 <button name="operation" value="clear_volatile">Clear cache and temporary data</button></form></article>{{else}}<p>No installed modules.</p>{{end}}
    </section>
    <section><h2>Official catalog</h2><p>Status: {{.Catalog.Status}} · sequence: {{.Catalog.Sequence}}</p><p>Only verified official releases can be installed. An unavailable catalog does not affect existing modules.</p>
 {{range .Catalog.Items}}<p>{{.Manifest.Identity}} {{.Manifest.Version}} · {{.Manifest.Arch}} · compatible: {{.Compatible}}</p>{{if .Compatible}}<form action="./review" method="get"><input type="hidden" name="module" value="{{.Manifest.Identity}}"><input type="hidden" name="version" value="{{.Manifest.Version}}"><button>Review install / update and dependencies</button></form>{{end}}{{end}}
 <form action="./manage" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><button name="operation" value="refresh_catalog">Refresh official catalog</button></form></section>
 <section><h2>Storage and recovery</h2><p>Cleanup preserves all active and retained state. Factory reset removes all Runtime and module data, then requires an App restart.</p>
 <form action="./manage" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><button name="operation" value="cleanup">Safe storage cleanup</button></form>
 <form action="./manage" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><label>Type DELETE ALL HOUSEFOLD DATA to confirm <input name="confirmation" autocomplete="off" maxlength="64"></label><button name="operation" value="factory_reset">Factory reset all Housefold data</button></form></section>
 <section><h2>Structured App logs</h2><p>Dropped: {{.Logs.Dropped}} · output failures: {{.Logs.OutputFailed}}. Full live stream uses normal HA App logs.</p><pre>{{range .Logs.Entries}}{{.At}} {{.Level}} {{.Message}} {{.Module}} {{.Version}} generation={{.Generation}} {{.Code}} {{.Phase}} {{.Status}}
{{end}}</pre></section>
 <section><h2>Administrative audit</h2><p>Status: {{.Audit.Status}}. Explicit factory reset deletes this journal with all Runtime data.</p><table><thead><tr><th>Sequence / time</th><th>HA user ID</th><th>Operation</th><th>Subject</th><th>Outcome</th></tr></thead><tbody>{{range .Audit.Entries}}<tr><td>{{.Sequence}} / {{.At}}</td><td>{{.User}}</td><td>{{.Operation}}</td><td>{{.Subject}}</td><td>{{.Outcome}}</td></tr>{{end}}</tbody></table></section>
 <p>For app recovery, use Home Assistant Supervisor controls and logs. If Home Assistant is unavailable, use the HAOS host console and <code>ha apps</code> commands.</p>
  </main>
</body>
</html>`))

func (s *Service) serveStatus(w http.ResponseWriter, csrf string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'self'; frame-ancestors 'self'")
	current := HAStatus{}
	if s.status != nil {
		current = s.status.HA()
	}
	page := statusPageData{CSRF: csrf, Logs: s.status.Logs(), Audit: s.auditSnapshot(), Catalog: s.catalogStatus(), Job: s.operation(), Bridge: s.status.Bridge(), Estate: s.status.Estate(), Runtime: "Healthy", Phase: "disconnected", Connection: "unavailable", Freshness: "none", LastSuccessful: "Not yet synchronized"}
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

func (s *StatusStore) SetLogs(logs interface{ Snapshot() diagnostics.Snapshot }) {
	s.mu.Lock()
	s.logs = logs
	s.mu.Unlock()
}
func (s *StatusStore) Logs() diagnostics.Snapshot {
	s.mu.RLock()
	logs := s.logs
	s.mu.RUnlock()
	if logs == nil {
		return diagnostics.Snapshot{}
	}
	return logs.Snapshot()
}
func (s *Service) auditSnapshot() estate.AuditSnapshot {
	owner, _ := s.status.managementOwner()
	if owner == nil {
		return estate.AuditSnapshot{Status: "unavailable"}
	}
	return owner.AuditSnapshot()
}
