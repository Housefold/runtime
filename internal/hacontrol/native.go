package hacontrol

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/housefold/runtime/internal/action"
	"github.com/housefold/runtime/internal/bridge"
	"github.com/housefold/runtime/internal/discovery"
	"github.com/housefold/runtime/internal/ha"
	"github.com/housefold/runtime/internal/module"
)

const defaultAPIURL = "http://supervisor/core/api/"

var nativeToken = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
var ErrNative = errors.New("local Home Assistant unavailable")

func localHTTP() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // Local HA credentials must never enter an environment proxy.
	transport.MaxConnsPerHost = 16
	transport.MaxIdleConns = 16
	transport.MaxIdleConnsPerHost = 16
	return &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type ModuleSignal struct {
	Identity       string               `json:"module"`
	Version        string               `json:"version"`
	Generation     uint64               `json:"generation"`
	Boot           string               `json:"boot"`
	Phase          string               `json:"phase"`
	Desired        bool                 `json:"desired"`
	ServiceHealthy bool                 `json:"service_healthy"`
	UIHealthy      bool                 `json:"ui_healthy"`
	Installed      bool                 `json:"installed"`
	Usage          module.Usage         `json:"usage"`
	Limits         module.ProcessLimits `json:"limits"`
	PressurePaused bool                 `json:"pressure_paused"`
}
type OperationalSnapshot struct {
	Runtime         string         `json:"runtime"`
	StorageDegraded bool           `json:"storage_degraded"`
	Catalog         string         `json:"catalog"`
	Bridge          string         `json:"bridge,omitempty"`
	Modules         []ModuleSignal `json:"modules"`
}
type ActionSignal struct {
	Module, Version    string
	Boot               string
	Generation         uint64
	Request, Execution string
	Outcome            action.Outcome
}
type Native struct {
	mu             sync.Mutex
	token, base    string
	http           *http.Client
	core           *Core
	source         *ha.StateSession
	discovered     discovery.Snapshot
	discoveryFresh bool
	discoverySink  func(discovery.Snapshot) error
	signals        func() OperationalSnapshot
	events         chan ActionSignal
	droppedEvents  atomic.Uint64
	bridgeCore     *Core
	bridgeClient   *bridge.Client
	bridgeSelected bool
	bridgeError    bool
}

func NewNative(token string, source *ha.StateSession) *Native {
	n := &Native{token: token, base: defaultAPIURL, http: localHTTP(), core: NewCore(token), source: source, events: make(chan ActionSignal, 64), bridgeCore: NewCore(token)}
	n.bridgeClient = bridge.New(n.bridgeCore)
	return n
}
func (n *Native) SetDiscoverySink(sink func(discovery.Snapshot) error) {
	n.mu.Lock()
	n.discoverySink = sink
	n.mu.Unlock()
}
func (n *Native) SetSignals(source func() OperationalSnapshot) {
	n.mu.Lock()
	n.signals = source
	n.mu.Unlock()
}
func (n *Native) Call(ctx context.Context, request action.Request) (action.Outcome, error) {
	return n.call(ctx, request)
}
func (n *Native) CallAttributed(ctx context.Context, id module.Identity, request action.Request) (action.Outcome, error) {
	outcome, err := n.call(ctx, request)
	signal := ActionSignal{Module: id.Module, Version: id.Version, Boot: id.Boot, Generation: id.Generation, Request: request.ID, Execution: request.Work, Outcome: outcome}
	select {
	case n.events <- signal:
	default:
		n.droppedEvents.Add(1)
	} // Observability must never block action completion.
	return outcome, err
}
func (n *Native) call(ctx context.Context, request action.Request) (action.Outcome, error) {
	if n.token == "" || ctx.Err() != nil || !nativeToken.MatchString(request.Domain) || !nativeToken.MatchString(request.Service) || !action.ValidRequest(request) {
		return action.NotSent, ErrNative
	}
	outcome, _, err := n.request(ctx, http.MethodPost, "services/"+request.Domain+"/"+request.Service, request.Data, 4096)
	if outcome == action.Rejected {
		return outcome, nil
	} // Explicit HA refusal is a known outcome.
	return outcome, err
}
func (n *Native) request(ctx context.Context, method, path string, raw []byte, limit int64) (action.Outcome, []byte, error) {
	if n.token == "" || ctx.Err() != nil {
		return action.NotSent, nil, ErrNative
	}
	ctx, cancel := context.WithTimeout(ctx, action.RequestTimeout)
	defer cancel()
	connected := atomic.Bool{}
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { connected.Store(true) }})
	request, err := http.NewRequestWithContext(ctx, method, n.base+path, bytes.NewReader(raw))
	if err != nil {
		return action.NotSent, nil, ErrNative
	}
	request.Header.Set("Authorization", "Bearer "+n.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := n.http.Do(request)
	if err != nil {
		if !connected.Load() {
			return action.NotSent, nil, ErrNative
		}
		return action.Unknown, nil, ErrNative
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 400, 401, 403, 404, 405, 422, 429:
		return action.Rejected, nil, ErrNative
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return action.Unknown, nil, ErrNative
	}
	if method != http.MethodGet {
		// Explicit successful HA response means accepted; household result payload is
		// unnecessary and is never returned/logged. Drain only a bounded prefix.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, limit))
		return action.Accepted, nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return action.Unknown, nil, ErrNative
	}
	return action.Accepted, body, nil
}
func (n *Native) DiscoverySnapshot() (discovery.Snapshot, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	copy, err := discovery.Normalize(n.discovered)
	return copy, n.discoveryFresh && err == nil
}
func (n *Native) Run(ctx context.Context) {
	defer n.core.Close()
	defer n.bridgeCore.Close()
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); n.discoveryLoop(workCtx) }()
	n.signalLoop(workCtx)
	cancel()
	<-done
	if transport, ok := n.http.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}
func (n *Native) discoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var next time.Time
	var generation uint64
	wasFresh := true
	for {
		now := time.Now()
		fresh := n.source == nil || n.source.Metadata().Fresh
		if n.source != nil {
			g := n.source.Metadata().Generation
			if g != generation {
				generation = g
				n.bridgeClient.Invalidate()
				next = time.Time{}
			}
		}
		if !fresh {
			if wasFresh {
				n.bridgeClient.Invalidate()
				n.bridgeCore.Close()
				n.mu.Lock()
				n.bridgeSelected = false
				n.discoveryFresh = false
				n.mu.Unlock()
			}
		} else {
			result := n.bridgeClient.Snapshot()
			if !now.Before(result.NextProbe) {
				n.probeBridge(ctx, now)
				next = time.Time{} // Enrichment changes/failure publish native fallback immediately.
			}
			if !now.Before(next) {
				_ = n.CollectDiscovery(ctx)
				next = now.Add(30 * time.Second)
			}
		}
		wasFresh = fresh
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func entitySignalID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "sensor.housefold_module_" + hex.EncodeToString(sum[:])
}
func (n *Native) signalLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	previous := map[string]string{}
	notifications := map[string]bool{}
	wasFresh := false
	var lastGeneration uint64
	publish := func() {
		n.mu.Lock()
		source := n.signals
		n.mu.Unlock()
		if source == nil {
			return
		}
		fresh := n.source == nil || n.source.Metadata().Fresh
		if n.source != nil {
			generation := n.source.Metadata().Generation
			if generation != lastGeneration {
				wasFresh = false
				lastGeneration = generation
				notifications = map[string]bool{}
			}
		}
		if !fresh {
			previous = map[string]string{}
			wasFresh = false
			return
		}
		if !wasFresh {
			previous = map[string]string{}
		}
		wasFresh = true
		snapshot := source()
		snapshot.Bridge = n.BridgeSnapshot().Status
		rows := map[string]any{"sensor.housefold_runtime": map[string]any{"state": snapshot.Runtime, "attributes": map[string]any{"friendly_name": "Housefold Runtime", "module_count": len(snapshot.Modules), "storage_degraded": snapshot.StorageDegraded, "catalog": snapshot.Catalog, "bridge": snapshot.Bridge, "action_event_drops": n.droppedEvents.Load()}}}
		issues := map[string]bool{"housefold_runtime_recovery": snapshot.Runtime == "recovery_required", "housefold_storage": snapshot.StorageDegraded}
		for _, m := range snapshot.Modules {
			if len(rows) >= 33 {
				break
			}
			phase := m.Phase
			if !m.Installed {
				phase = "removed"
			} else if !m.Desired {
				phase = "stopped"
			}
			rows[entitySignalID(m.Identity)] = map[string]any{"state": phase, "attributes": m}
			issues["housefold_quarantine_"+entitySignalID(m.Identity)] = m.Phase == "QUARANTINED"
		}
		cycle, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		ids := make([]string, 0, len(rows))
		for id := range rows {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		// Runtime's own status is sent first, then bounded module identities.
		ids = append([]string{"sensor.housefold_runtime"}, ids...)
		for _, id := range ids {
			row := rows[id]
			raw, _ := json.Marshal(row)
			fingerprint := string(raw)
			if previous[id] == fingerprint {
				continue
			}
			outcome, _, err := n.request(cycle, http.MethodPost, "states/"+id, raw, 4096)
			if err != nil || outcome != action.Accepted {
				return
			}
			previous[id] = fingerprint
			event, _ := json.Marshal(map[string]any{"schema": 1, "entity_id": id, "status": row})
			_, _, _ = n.request(cycle, http.MethodPost, "events/housefold_status", event, 4096)
		}
		for id, active := range issues {
			if notifications[id] == active {
				continue
			}
			service := "dismiss"
			body := map[string]any{"notification_id": id}
			if active {
				service = "create"
				body["title"] = "Housefold Runtime needs attention"
				body["message"] = "Open the Housefold Runtime admin panel for diagnostics and recovery. Existing module data is retained."
			}
			raw, _ := json.Marshal(body)
			outcome, _, err := n.request(cycle, http.MethodPost, "services/persistent_notification/"+service, raw, 4096)
			if err == nil && outcome == action.Accepted {
				notifications[id] = active
			}
		}
		// Remove bounded tracking for inventory identities no longer present.
		for id := range notifications {
			if _, ok := issues[id]; !ok {
				delete(notifications, id)
			}
		}
	}
	publish()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			publish()
		case signal := <-n.events:
			body, _ := json.Marshal(map[string]any{"schema": 1, "module": signal.Module, "version": signal.Version, "boot": signal.Boot, "generation": signal.Generation, "request": signal.Request, "execution": signal.Execution, "outcome": signal.Outcome})
			_, _, _ = n.request(ctx, http.MethodPost, "events/housefold_action", body, 4096)
		}
	}
}
