package supervisor

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/housefold/runtime/internal/audit"
	"github.com/housefold/runtime/internal/catalog"
	"github.com/housefold/runtime/internal/estate"
	"github.com/housefold/runtime/internal/packageverify"
)

const testAdmin = "0123456789abcdef0123456789abcdef"
const otherAdmin = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type testAuthorizer func(context.Context, string) error

func (a testAuthorizer) AuthorizeAdmin(ctx context.Context, id string) error { return a(ctx, id) }
func authorizedTestService(store *StatusStore) *Service {
	if store == nil {
		store = &StatusStore{}
	}
	store.SetManagement(nil, testAuthorizer(func(_ context.Context, id string) error {
		if id == testAdmin || id == otherAdmin {
			return nil
		}
		return errManagement
	}))
	return NewService(store)
}
func biosRequest(s *Service, method, path, user string, form url.Values) *httptest.ResponseRecorder {
	var body string
	if form != nil {
		body = form.Encode()
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = ingressPeer + ":8000"
	if user != "" {
		r.Header.Set("X-Remote-User-Id", user)
	}
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	return w
}
func TestBIOSAuthorizationFailClosedEverySurface(t *testing.T) {
	s := authorizedTestService(nil)
	for _, path := range []string{"/", "/review", "/manage", "/diagnostics"} {
		for _, user := range []string{"", "admin", strings.Repeat("b", 32)} {
			if w := biosRequest(s, "GET", path, user, nil); w.Code != 403 {
				t.Fatalf("%s user %q: %d", path, user, w.Code)
			}
		}
	}
	// An admin-looking header/identity from another peer is never accepted.
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.30.32.20:1234"
	r.Header.Set("X-Remote-User-Id", testAdmin)
	r.Header.Set("X-Forwarded-For", ingressPeer)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r.RemoteAddr = ingressPeer + ":1234"
	r.Header.Add("X-Remote-User-Id", otherAdmin)
	w = httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("duplicate identity accepted")
	}
	// Revocation or Core unavailability takes effect without Runtime restart.
	s.status.SetManagement(nil, testAuthorizer(func(context.Context, string) error { return errors.New("unavailable") }))
	if w := biosRequest(s, "GET", "/", testAdmin, nil); w.Code != 403 {
		t.Fatal("lookup failure accepted")
	}
	if w := biosRequest(NewService(nil), "GET", "/", testAdmin, nil); w.Code != 403 {
		t.Fatal("missing authorizer accepted")
	}
}

type managementFixture struct {
	mu               sync.Mutex
	calls            []string
	entered, release chan struct{}
	reviewErr        error
}

func (m *managementFixture) record(ctx context.Context, op string) error {
	m.mu.Lock()
	m.calls = append(m.calls, op)
	m.mu.Unlock()
	if m.entered != nil {
		close(m.entered)
		select {
		case <-m.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (m *managementFixture) CatalogStatus(time.Time) catalog.Snapshot {
	return catalog.Snapshot{Status: "unconfigured"}
}
func (m *managementFixture) ReviewInstall(id, v string, _ time.Time) (string, []packageverify.Manifest, error) {
	return strings.Repeat("a", 64), []packageverify.Manifest{{Identity: id, Version: v, Arch: "amd64", Dependencies: []packageverify.Dependency{{Identity: "dependency", Version: "1.0.0"}}, Capabilities: []string{"ha.observe"}, Resources: packageverify.Resources{MemoryKiB: 128}}}, m.reviewErr
}
func (m *managementFixture) Install(ctx context.Context, id, v, d string, _ time.Time) error {
	return m.record(ctx, "install:"+id+":"+v+":"+d)
}
func (m *managementFixture) RefreshCatalog(ctx context.Context, _ time.Time) error {
	return m.record(ctx, "refresh_catalog")
}
func (m *managementFixture) SetDesired(id string, b bool) error {
	op := "stop"
	if b {
		op = "start"
	}
	return m.record(context.Background(), op+":"+id)
}
func (m *managementFixture) Restart(id string) error {
	return m.record(context.Background(), "restart:"+id)
}
func (m *managementFixture) Remove(id string) error {
	return m.record(context.Background(), "remove:"+id)
}
func (m *managementFixture) Recover(id string, _ time.Time) error {
	return m.record(context.Background(), "recover:"+id)
}
func (m *managementFixture) Rollback(ctx context.Context, id string, _ time.Time) error {
	return m.record(ctx, "rollback:"+id)
}
func (m *managementFixture) ClearVolatile(ctx context.Context, id string) error {
	return m.record(ctx, "clear_volatile:"+id)
}
func (m *managementFixture) Cleanup() error { return m.record(context.Background(), "cleanup") }
func (m *managementFixture) FactoryReset(ctx context.Context, confirmation string) error {
	if confirmation != estate.FactoryResetConfirmation {
		return estate.ErrOperation
	}
	return m.record(ctx, "factory_reset")
}
func withManagement(m Management) *Service {
	s := authorizedTestService(nil)
	_, admin := s.status.managementOwner()
	s.status.SetManagement(m, admin)
	return s
}
func TestSingleUseApprovalIdentityExpiryAndBoundedness(t *testing.T) {
	s := authorizedTestService(nil)
	key, err := s.issue(testAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if s.consume(otherAdmin, key) {
		t.Fatal("approval crossed identities")
	}
	if !s.consume(testAdmin, key) || s.consume(testAdmin, key) {
		t.Fatal("approval replay")
	}
	key, _ = s.issue(testAdmin)
	s.mu.Lock()
	s.tickets[key] = approval{testAdmin, time.Now().Add(-time.Second)}
	s.mu.Unlock()
	if s.consume(testAdmin, key) {
		t.Fatal("expired approval")
	}
	for i := 0; i < 1000; i++ {
		if _, err = s.issue(testAdmin); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.tickets) != 256 {
		t.Fatal("unbounded approvals", len(s.tickets))
	}
}
func TestBIOSMutationsBoundedSingleWorkerAndNoReplay(t *testing.T) {
	m := &managementFixture{entered: make(chan struct{}), release: make(chan struct{})}
	s := withManagement(m)
	key, _ := s.issue(testAdmin)
	form := url.Values{"csrf": {key}, "operation": {"rollback"}, "module": {"synthetic"}}
	if w := biosRequest(s, "POST", "/manage", testAdmin, form); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	<-m.entered
	if w := biosRequest(s, "POST", "/manage", testAdmin, form); w.Code != 403 {
		t.Fatal("replay", w.Code)
	}
	key, _ = s.issue(testAdmin)
	form.Set("csrf", key)
	if w := biosRequest(s, "POST", "/manage", testAdmin, form); w.Code != 409 {
		t.Fatal("queued concurrent operation", w.Code)
	}
	if w := biosRequest(s, "GET", "/", testAdmin, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "running") {
		t.Fatal("operation blocked BIOS")
	}
	close(m.release)
	s.workers.Wait()
	if len(m.calls) != 1 || s.operation().State != "complete" {
		t.Fatal(m.calls, s.operation())
	}
}
func TestBIOSValidationResetAndReview(t *testing.T) {
	for _, op := range []string{"start", "stop", "restart", "remove", "recover", "rollback", "clear_volatile", "cleanup", "factory_reset", "refresh_catalog", "install"} {
		t.Run(op, func(t *testing.T) {
			m := &managementFixture{}
			s := withManagement(m)
			key, _ := s.issue(testAdmin)
			form := url.Values{"csrf": {key}, "operation": {op}}
			switch op {
			case "cleanup", "refresh_catalog":
			case "factory_reset":
				form.Set("confirmation", estate.FactoryResetConfirmation)
			default:
				form.Set("module", "synthetic")
			}
			if op == "install" {
				form.Set("version", "1.0.0")
				form.Set("digest", strings.Repeat("a", 64))
			}
			if w := biosRequest(s, "POST", "/manage", testAdmin, form); w.Code != 303 {
				t.Fatal(w.Code, w.Body.String())
			}
			s.workers.Wait()
			if len(m.calls) != 1 {
				t.Fatal(m.calls)
			}
		})
	}
	m := &managementFixture{}
	s := withManagement(m)
	key, _ := s.issue(testAdmin)
	for _, form := range []url.Values{
		{"csrf": {key}, "operation": {"factory_reset"}},
		{"csrf": {key}, "operation": {"start"}, "module": {"../../outside"}},
		{"csrf": {key}, "operation": {"start", "stop"}, "module": {"synthetic"}},
		{"csrf": {key}, "operation": {"start"}, "module": {"synthetic"}, "path": {"/tmp/binary"}},
		{"csrf": {key}, "operation": {"install"}, "module": {"synthetic"}, "version": {"1.0.0"}},
	} {
		if w := biosRequest(s, "POST", "/manage", testAdmin, form); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if len(m.calls) != 0 {
		t.Fatal("invalid operation mutated estate")
	}
	w := biosRequest(s, "GET", "/review?module=synthetic&version=1.0.0", testAdmin, nil)
	for _, want := range []string{"dependency", "ha.observe", "128", "Install reviewed official release", strings.Repeat("a", 64)} {
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Fatal(w.Code, w.Body.String(), want)
		}
	}
}
func TestRecoveryBIOSAndDiagnosticPrivacy(t *testing.T) {
	s := withManagement(&managementFixture{})
	s.status.SetRecoveryRequired()
	if w := biosRequest(s, "GET", "/", testAdmin, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "Recovery required") {
		t.Fatal(w.Code, w.Body.String())
	}
	w := biosRequest(s, "GET", "/diagnostics", testAdmin, nil)
	for _, bad := range []string{testAdmin, "csrf", "credentials", "SUPERVISOR_TOKEN", "entity_id", "StatePath"} {
		if strings.Contains(w.Body.String(), bad) {
			t.Fatal("diagnostic leaked", bad)
		}
	}
	if w.Code != 200 || w.Header().Get("Content-Disposition") == "" {
		t.Fatal(w.Code)
	}
	if w := biosRequest(s, "GET", "/healthz", "", nil); w.Code != 503 {
		t.Fatal("integrity health", w.Code)
	}
}

func (m *managementFixture) AuditSnapshot() estate.AuditSnapshot {
	return estate.AuditSnapshot{Status: "test"}
}
func (m *managementFixture) BeginAdmin(string, string, string, time.Time) (uint64, error) {
	return 1, nil
}
func (m *managementFixture) FinishAdmin(uint64, bool, bool) error { return nil }

type unauditableManagement struct{ managementFixture }

func (m *unauditableManagement) BeginAdmin(string, string, string, time.Time) (uint64, error) {
	return 0, errManagement
}
func TestUnauditableMutationDoesNotInvokeLifecycle(t *testing.T) {
	m := &unauditableManagement{}
	s := withManagement(m)
	key, _ := s.issue(testAdmin)
	if w := biosRequest(s, "POST", "/manage", testAdmin, url.Values{"csrf": {key}, "operation": {"restart"}, "module": {"synthetic"}}); w.Code != 303 {
		t.Fatal(w.Code)
	}
	s.workers.Wait()
	if len(m.calls) != 0 || s.operation().State != "failed" || !strings.Contains(s.operation().Result, "not attempted") {
		t.Fatal(m.calls, s.operation())
	}
}

type privateAuditFixture struct{ managementFixture }

func (m *privateAuditFixture) AuditSnapshot() estate.AuditSnapshot {
	return estate.AuditSnapshot{Status: "available", Entries: []audit.Entry{{User: testAdmin, Operation: "restart", Subject: "synthetic", Outcome: "completed"}}}
}

type privateEstateFixture struct{}

func (privateEstateFixture) Snapshot() estate.Snapshot {
	return estate.Snapshot{Phase: "running", Modules: []estate.ModuleStatus{{Identity: "synthetic", Version: "1.0.0", Error: "PRIVATE_RAW_ERROR /data/foreign", Outbound: []string{"private-household.local"}, Capabilities: []string{"PRIVATE_FUTURE_FIELD"}}}}
}
func TestDiagnosticProjectionExcludesActualAuditAndFutureSensitiveFields(t *testing.T) {
	s := withManagement(&privateAuditFixture{})
	s.status.SetEstate(privateEstateFixture{})
	w := biosRequest(s, "GET", "/diagnostics", testAdmin, nil)
	for _, bad := range []string{testAdmin, "private-household", "PRIVATE_", "/data/", "Outbound", "Capabilities", `"Error":`} {
		if strings.Contains(w.Body.String(), bad) {
			t.Fatal("export leaked", bad, w.Body.String())
		}
	}
	if !strings.Contains(w.Body.String(), `"audit_entries_retained":1`) || !strings.Contains(w.Body.String(), "synthetic") {
		t.Fatal("coarse diagnostics lost", w.Body.String())
	}
	w = biosRequest(s, "GET", "/", testAdmin, nil)
	if !strings.Contains(w.Body.String(), testAdmin) {
		t.Fatal("verified BIOS lacked attributed audit")
	}
}
