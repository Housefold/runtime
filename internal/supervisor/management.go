package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"html/template"
	"net/http"
	"regexp"
	"time"

	"github.com/housefold/runtime/internal/catalog"
	"github.com/housefold/runtime/internal/estate"
	"github.com/housefold/runtime/internal/packageverify"
)

type AdminAuthorizer interface {
	AuthorizeAdmin(context.Context, string) error
}

// Management deliberately has no arbitrary path, executable or HA service API.
type Management interface {
	AuditSnapshot() estate.AuditSnapshot
	BeginAdmin(string, string, string, time.Time) (uint64, error)
	FinishAdmin(uint64, bool, bool) error
	CatalogStatus(time.Time) catalog.Snapshot
	ReviewInstall(string, string, time.Time) (string, []packageverify.Manifest, error)
	Install(context.Context, string, string, string, time.Time) error
	RefreshCatalog(context.Context, time.Time) error
	SetDesired(string, bool) error
	Restart(string) error
	Remove(string) error
	Recover(string, time.Time) error
	Rollback(context.Context, string, time.Time) error
	ClearVolatile(context.Context, string) error
	Cleanup() error
	FactoryReset(context.Context, string) error
}

func (s *StatusStore) SetManagement(owner Management, admin AdminAuthorizer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.management, s.admin = owner, admin
}
func (s *StatusStore) managementOwner() (Management, AdminAuthorizer) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.management, s.admin
}
func (s *Service) authorize(r *http.Request) (string, error) {
	values := r.Header.Values("X-Remote-User-Id")
	if len(values) != 1 || !haUserID.MatchString(values[0]) {
		return "", errManagement
	}
	_, admin := s.status.managementOwner()
	if admin == nil {
		return "", errManagement
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if admin.AuthorizeAdmin(ctx, values[0]) != nil {
		return "", errManagement
	}
	return values[0], nil
}

var haUserID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var moduleID = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)
var versionID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+_-]{0,63}$`)
var digestID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var errManagement = errors.New("management unavailable")

type approval struct {
	User    string
	Expires time.Time
}
type operationStatus struct{ Operation, Subject, State, Result string }

func (s *Service) issue(user string) (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	key := hex.EncodeToString(raw[:])
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.tickets {
		if !now.Before(v.Expires) {
			delete(s.tickets, k)
		}
	}
	// Finite approvals, including adversarial page refresh. Evict earliest expiry.
	if len(s.tickets) >= 256 {
		var oldest string
		var expiry time.Time
		for k, v := range s.tickets {
			if oldest == "" || v.Expires.Before(expiry) {
				oldest, expiry = k, v.Expires
			}
		}
		delete(s.tickets, oldest)
	}
	s.tickets[key] = approval{user, now.Add(5 * time.Minute)}
	return key, nil
}
func (s *Service) consume(user, key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.tickets[key]
	if !ok || a.User != user || !time.Now().Before(a.Expires) {
		return false
	}
	delete(s.tickets, key)
	return true
}
func (s *Service) catalogStatus() catalog.Snapshot {
	owner, _ := s.status.managementOwner()
	if owner == nil {
		return catalog.Snapshot{Status: "unavailable"}
	}
	return owner.CatalogStatus(time.Now())
}
func (s *Service) operation() operationStatus { s.mu.Lock(); defer s.mu.Unlock(); return s.job }
func (s *Service) manage(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		http.Error(w, "form required", 415)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil || len(r.URL.RawQuery) != 0 {
		http.Error(w, "invalid form", 400)
		return
	}
	for k, v := range r.PostForm {
		switch k {
		case "csrf", "operation", "module", "version", "digest", "confirmation":
		default:
			http.Error(w, "invalid field", 400)
			return
		}
		if len(v) != 1 || len(v[0]) > 128 {
			http.Error(w, "invalid field", 400)
			return
		}
	}
	op, id, version, digest := r.PostForm.Get("operation"), r.PostForm.Get("module"), r.PostForm.Get("version"), r.PostForm.Get("digest")
	switch op {
	case "start", "stop", "restart", "remove", "recover", "rollback", "clear_volatile":
		if !moduleID.MatchString(id) || version != "" || digest != "" {
			http.Error(w, "invalid module operation", 400)
			return
		}
	case "install":
		if !moduleID.MatchString(id) || !versionID.MatchString(version) || !digestID.MatchString(digest) {
			http.Error(w, "review required", 400)
			return
		}
	case "cleanup", "factory_reset", "refresh_catalog":
		if id != "" || version != "" || digest != "" {
			http.Error(w, "invalid operation", 400)
			return
		}
	default:
		http.Error(w, "invalid operation", 400)
		return
	}
	confirmation := r.PostForm.Get("confirmation")
	if (op == "factory_reset" && confirmation != "DELETE ALL HOUSEFOLD DATA") || (op != "factory_reset" && confirmation != "") {
		http.Error(w, "explicit reset confirmation required", 400)
		return
	}
	if !s.consume(user, r.PostForm.Get("csrf")) {
		http.Error(w, "form approval expired or already used; reload BIOS", 403)
		return
	}
	owner, _ := s.status.managementOwner()
	if owner == nil {
		http.Error(w, "estate unavailable", 503)
		return
	}
	s.mu.Lock()
	if s.working || s.workCtx.Err() != nil || s.stopping.Load() {
		s.mu.Unlock()
		http.Error(w, "management busy; refresh BIOS", 409)
		return
	}
	s.working = true
	s.job = operationStatus{Operation: op, Subject: id, State: "running"}
	ctx, cancel := context.WithTimeout(s.workCtx, 30*time.Second)
	s.workers.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.workers.Done()
		defer cancel()
		now := time.Now()
		sequence, err := owner.BeginAdmin(user, op, id, now)
		if err != nil {
			s.mu.Lock()
			s.job.State, s.job.Result = "failed", "Administrative audit unavailable; operation was not attempted."
			s.working = false
			s.mu.Unlock()
			return
		}
		switch op {
		case "start":
			err = owner.SetDesired(id, true)
		case "stop":
			err = owner.SetDesired(id, false)
		case "restart":
			err = owner.Restart(id)
		case "remove":
			err = owner.Remove(id)
		case "recover":
			err = owner.Recover(id, now)
		case "rollback":
			err = owner.Rollback(ctx, id, now)
		case "clear_volatile":
			err = owner.ClearVolatile(ctx, id)
		case "cleanup":
			err = owner.Cleanup()
		case "factory_reset":
			err = owner.FactoryReset(ctx, confirmation)
		case "refresh_catalog":
			err = owner.RefreshCatalog(ctx, now)
		case "install":
			err = owner.Install(ctx, id, version, digest, now)
		}
		resetComplete := op == "factory_reset" && errors.Is(err, estate.ErrRestartRequired)
		auditErr := owner.FinishAdmin(sequence, err == nil || resetComplete, resetComplete)
		state, result := "complete", "Operation completed. Refresh status."
		if err != nil {
			state, result = "failed", operationError(err)
		}
		if errors.Is(err, estate.ErrRestartRequired) {
			state, result = "complete", "Reset complete. Restart Runtime using Home Assistant App controls."
		}
		if auditErr != nil {
			state, result = "audit_incomplete", "Operation outcome could not be audited. Inspect current status before retrying."
		}
		s.mu.Lock()
		s.job.State, s.job.Result = state, result
		s.working = false
		s.mu.Unlock()
	}()
	// The request never owns the long-running worker or queues another operation.
	w.Header().Set("Location", "./")
	w.WriteHeader(http.StatusSeeOther)
}
func operationError(err error) string {
	switch {
	case errors.Is(err, estate.ErrRecovery):
		return "Recovery required. Durable data was preserved."
	case errors.Is(err, estate.ErrStorage):
		return "Storage writes are fenced. Check storage pressure."
	case errors.Is(err, estate.ErrDependency), errors.Is(err, catalog.ErrDependency):
		return "Required dependency unavailable or still enabled. Review dependencies."
	case errors.Is(err, catalog.ErrReview):
		return "Catalog changed. Review installation again."
	case errors.Is(err, catalog.ErrUnavailable):
		return "Official catalog unavailable. Existing modules are unaffected."
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "Operation interrupted. Inspect current status before retrying."
	default:
		return "Operation failed. Inspect diagnostics and Home Assistant App logs."
	}
}

var reviewPage = template.Must(template.New("review").Parse(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Housefold install review</title><body><h1>Review official install / update</h1><p>Catalog digest: {{.Digest}}</p>{{range .Manifests}}<article><h2>{{.Identity}} {{.Version}}</h2><p>Architecture: {{.Arch}} · Runtime {{.RuntimeMajor}} · IPC {{.ProtocolMajor}}</p><p>Capabilities: {{range .Capabilities}}{{.}} {{end}}</p><p>Resources: {{.Resources}} · requests: {{.Requests}} · priority: {{.Priority}}</p><p>Dependencies: {{range .Dependencies}}{{.Identity}} {{.Version}} (optional: {{.Optional}}) {{end}}</p><p>Outbound networking: {{.Outbound}}</p></article>{{end}}<p>Required dependencies are installed/enabled transactionally. Preparation failure preserves active modules. Removed module data is retained.</p><form action="./manage" method="post"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="operation" value="install"><input type="hidden" name="module" value="{{.ID}}"><input type="hidden" name="version" value="{{.Version}}"><input type="hidden" name="digest" value="{{.Digest}}"><button>Install reviewed official release</button></form><a href="./">Back to BIOS</a></body></html>`))

func (s *Service) review(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", 405)
		return
	}
	query := r.URL.Query()
	if len(query) != 2 || len(query["module"]) != 1 || len(query["version"]) != 1 || !moduleID.MatchString(query.Get("module")) || !versionID.MatchString(query.Get("version")) {
		http.Error(w, "invalid review", 400)
		return
	}
	owner, _ := s.status.managementOwner()
	if owner == nil {
		http.Error(w, "estate unavailable", 503)
		return
	}
	digest, manifests, err := owner.ReviewInstall(query.Get("module"), query.Get("version"), time.Now())
	if err != nil {
		http.Error(w, operationError(err), 409)
		return
	}
	csrf, err := s.issue(user)
	if err != nil {
		http.Error(w, "management unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = reviewPage.Execute(w, struct {
		CSRF, Digest, ID, Version string
		Manifests                 []packageverify.Manifest
	}{csrf, digest, query.Get("module"), query.Get("version"), manifests})
}
