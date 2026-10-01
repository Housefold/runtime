package estate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/housefold/runtime/internal/module"
	"github.com/housefold/runtime/internal/packageverify"
)

func healthyHost(string) (HostResources, error) {
	return HostResources{TotalKiB: 8 * 1024 * 1024, AvailableKiB: 4 * 1024 * 1024}, nil
}
func amend(t *testing.T, key ed25519.PrivateKey, b Bundle, change func(*packageverify.Manifest)) Bundle {
	t.Helper()
	m := b.declaration()
	change(&m)
	b.Manifest = signed(key, m)
	c := packageverify.Catalog{Schema: 1, KeyID: "synthetic", Sequence: 1, Expires: time.Now().Add(time.Hour), Entries: []packageverify.Entry{{Identity: m.Identity, Version: m.Version, Arch: m.Arch, ArtifactDigest: m.ArtifactDigest, ManifestDigest: packageverify.Digest(b.Manifest.Data)}}}
	b.Catalog = signed(key, c)
	b.ArtifactSignature = ed25519.Sign(key, packageverify.ArtifactStatement(packageverify.Digest(b.Manifest.Data), m.ArtifactDigest))
	return b
}
func TestProductionLimitsAndSustainedViolationUseCrashBudget(t *testing.T) {
	e, key := fixtureEngine(t)
	launcher := filepath.Join(t.TempDir(), "launcher")
	cmd := exec.Command("go", "build", "-o", launcher, "../../cmd/module-launcher")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	e.config.Trampoline = launcher
	b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
	stage(t, e, b, raw)
	var high bool
	e.config.SampleUsage = func(*module.Process) (module.Usage, error) {
		r := uint64(1)
		if high {
			r = 128*1024 + 1
		}
		return module.Usage{RSSKiB: r, Threads: 4, FDs: 6, Processes: 1}, nil
	}
	now := time.Now()
	high = true
	for i := 0; i < 2; i++ {
		if err := e.Reconcile(now); err != nil {
			t.Fatal(err)
		}
	}
	if e.router.Snapshot().Modules["synthetic"].Failures != 0 {
		t.Fatal("transient burst consumed crash budget")
	}
	high = false
	_ = e.Reconcile(now)
	high = true
	for i := 0; i < 3; i++ {
		if err := e.Reconcile(now); err != nil {
			t.Fatal(err)
		}
	}
	if e.router.Snapshot().Modules["synthetic"].Failures != 1 {
		t.Fatal("sustained violation did not enter crash lifecycle")
	}
	s := e.Snapshot().Modules[0]
	if s.Limits.FDs != 64 || s.Limits.Priority != "normal" {
		t.Fatal(s)
	}
}
func TestPressureEvictionPriorityAndControlledRecovery(t *testing.T) {
	e, key := fixtureEngine(t)
	for _, id := range []string{"essential", "background", "normal"} {
		b, raw := bundle(t, key, id, "1.0.0", "normal")
		b = amend(t, key, b, func(m *packageverify.Manifest) { m.Priority = id })
		stage(t, e, b, raw)
	}
	old := e.Snapshot().Modules
	e.config.SampleResources = func(string) (HostResources, error) { h, _ := healthyHost(""); h.AvailableKiB = 1; return h, nil }
	now := time.Now()
	if err := e.Reconcile(now); err != nil {
		t.Fatal(err)
	}
	if !e.inv.Modules["background"].PressurePaused || e.inv.Modules["normal"].PressurePaused || e.inv.Modules["essential"].PressurePaused {
		t.Fatal("wrong pressure victim", e.inv)
	}
	if e.router.Accepting("background") || !e.router.Accepting("essential") {
		t.Fatal("authority survived pressure stop")
	}
	for _, id := range []string{"normal", "essential"} {
		if err := e.Reconcile(now); err != nil {
			t.Fatal(err)
		}
		if !e.inv.Modules[id].PressurePaused {
			t.Fatal("priority order", id)
		}
	}
	for _, sel := range e.router.Snapshot().Modules {
		if sel.Failures != 0 {
			t.Fatal("host pressure consumed crash budget")
		}
	}
	e.config.SampleResources = healthyHost
	for i := 0; i < 4; i++ {
		if err := e.Reconcile(now); err != nil {
			t.Fatal(err)
		}
	}
	if e.router.Accepting("essential") {
		t.Fatal("recovered without hysteresis")
	}
	if err := e.Reconcile(now); err != nil {
		t.Fatal(err)
	}
	if !e.router.Accepting("essential") || e.router.Accepting("normal") {
		t.Fatal("uncontrolled recovery")
	}
	if e.Snapshot().Modules[1].Generation == old[1].Generation {
		t.Fatal("stale authority resumed")
	}
}
func TestStoragePressurePreservesRetainedAndFailsClosed(t *testing.T) {
	e, key := fixtureEngine(t)
	b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
	stage(t, e, b, raw)
	first := e.currentUnit("synthetic").identity
	b, raw = bundle(t, key, "synthetic", "2.0.0", "normal")
	stage(t, e, b, raw)
	before, err := os.ReadFile(filepath.Join(e.stateRoot("persistent"), hashID("synthetic"), "1", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	unused := e.archivePath(packageverify.Digest([]byte("orphan")))
	if err = os.WriteFile(unused, []byte("orphan"), 0500); err != nil {
		t.Fatal(err)
	}
	e.config.FreeSpace = func(string) (uint64, error) { return MinFreeBytes - 1, nil }
	if err = e.checkSpace(1); !errors.Is(err, ErrStorage) {
		t.Fatal("unsafe write admitted", err)
	}
	if err = e.collectStorage(true); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(unused); !os.IsNotExist(err) {
		t.Fatal("orphan artifact retained", err)
	}
	after, _ := os.ReadFile(filepath.Join(e.stateRoot("persistent"), hashID(first.Module), "1", "state.json"))
	if string(before) != string(after) {
		t.Fatal("retained state discarded")
	}
	e.config.FreeSpace = func(string) (uint64, error) { return ResumeFreeBytes + 1, nil }
	e.config.SampleResources = healthyHost
	if err = e.resourceTick(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = e.checkSpace(0); err != nil {
		t.Fatal("storage did not recover", err)
	}
	target := filepath.Join(t.TempDir(), "sentinel")
	if err = os.WriteFile(target, []byte("must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, filepath.Join(e.stateRoot("cache"), hashID("synthetic"), "1", "attack")); err != nil {
		t.Fatal(err)
	}
	if err = e.collectStorage(true); err == nil {
		t.Fatal("linked tree accepted")
	}
	if raw, _ := os.ReadFile(target); string(raw) != "must survive" {
		t.Fatal("traversed link")
	}
}
func TestResourceRequestsRefusedBeforeInventoryCommit(t *testing.T) {
	e, key := fixtureEngine(t)
	for i, id := range []string{"one", "two"} {
		b, raw := bundle(t, key, id, "1.0.0", "normal")
		b = amend(t, key, b, func(m *packageverify.Manifest) { m.Requests.CPUPercent = 60; m.Resources.CPUPercent = 100 })
		err := e.Stage(context.Background(), []Bundle{b}, [][]byte{raw}, time.Now())
		if i == 0 && err != nil {
			t.Fatal(err)
		}
		if i == 1 && !errors.Is(err, ErrResources) {
			t.Fatal(err)
		}
	}
	if len(e.inv.Modules) != 1 || !e.router.Accepting("one") {
		t.Fatal("rejected admission changed estate")
	}
}

func hashID(id string) string { sum := sha256.Sum256([]byte(id)); return hex.EncodeToString(sum[:]) }
