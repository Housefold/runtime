package supervisor

import (
	"encoding/json"
	"net/http"
	"runtime"

	"github.com/housefold/runtime/internal/estate"
	"github.com/housefold/runtime/internal/module"
)

type BuildInfo struct {
	Version      string `json:"version"`
	Source       string `json:"source"`
	Go           string `json:"go"`
	Architecture string `json:"architecture"`
}

func (s *StatusStore) SetBuild(version, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.build = BuildInfo{version, source, runtime.Version(), runtime.GOARCH}
}
func (s *StatusStore) Build() BuildInfo { s.mu.RLock(); defer s.mu.RUnlock(); return s.build }

type diagnosticModule struct {
	Identity, Version, RetainedVersion, Phase                                string
	Generation                                                               uint64
	Installed, Desired, ServiceHealthy, UIHealthy, Accepting, PressurePaused bool
	Usage                                                                    module.Usage
	Limits                                                                   module.ProcessLimits
}
type diagnosticEstate struct {
	Phase           string
	StorageDegraded bool
	Resources       estate.ResourceStatus
	Modules         []diagnosticModule
}

func (s *Service) diagnostics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="housefold-runtime-diagnostics.json"`)
	snap := s.status.Estate()
	state := diagnosticEstate{Phase: snap.Phase, StorageDegraded: snap.StorageDegraded, Resources: snap.Resources}
	for _, m := range snap.Modules {
		state.Modules = append(state.Modules, diagnosticModule{Identity: m.Identity, Version: m.Version, RetainedVersion: m.RetainedVersion, Phase: m.Phase, Generation: m.Generation, Installed: m.Installed, Desired: m.Desired, ServiceHealthy: m.ServiceHealthy, UIHealthy: m.UIHealthy, Accepting: m.Accepting, PressurePaused: m.PressurePaused, Usage: m.Usage, Limits: m.Limits})
	}
	logs := s.status.Logs()
	audit := s.auditSnapshot()
	// Only explicit coarse projections. No raw errors/paths, network declarations,
	// admin identities/form approvals, log contents, bindings or household values.
	_ = json.NewEncoder(w).Encode(struct {
		Schema         int              `json:"schema"`
		Build          BuildInfo        `json:"build"`
		Recovery       bool             `json:"recovery_required"`
		Goroutines     int              `json:"goroutines"`
		HA             HAStatus         `json:"ha"`
		Estate         diagnosticEstate `json:"estate"`
		Bridge         string           `json:"bridge_status"`
		Catalog        string           `json:"catalog_status"`
		LogsRetained   int              `json:"logs_retained"`
		LogsDropped    uint64           `json:"logs_dropped"`
		OutputFailures uint64           `json:"log_output_failures"`
		AuditStatus    string           `json:"audit_status"`
		AuditRetained  int              `json:"audit_entries_retained"`
	}{1, s.status.Build(), s.status.RecoveryRequired(), runtime.NumGoroutine(), s.status.HA(), state, s.status.Bridge().Status, s.catalogStatus().Status, len(logs.Entries), logs.Dropped, logs.OutputFailed, audit.Status, len(audit.Entries)})
}
