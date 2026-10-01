package estate

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/execution"
	"github.com/housefold/runtime/internal/module"
	"github.com/housefold/runtime/internal/packageverify"
	"github.com/housefold/runtime/internal/timeline"
)

var binaryMu sync.Mutex
var binaries = map[string][]byte{}

func fixtureBinary(t *testing.T, mode string) []byte {
	t.Helper()
	binaryMu.Lock()
	defer binaryMu.Unlock()
	if raw := binaries[mode]; raw != nil {
		return append([]byte(nil), raw...)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "reference")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w -X main.buildMode="+mode, "-o", path, "../../cmd/reference-module")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile synthetic reference: %v %s", err, out)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	binaries[mode] = raw
	return append([]byte(nil), raw...)
}
func signed(key ed25519.PrivateKey, data any) packageverify.Signed {
	raw, _ := json.Marshal(data)
	return packageverify.Signed{Data: raw, Signature: ed25519.Sign(key, raw)}
}
func bundle(t *testing.T, key ed25519.PrivateKey, id, version, mode string, deps ...packageverify.Dependency) (Bundle, []byte) {
	raw := fixtureBinary(t, mode)
	m := packageverify.Manifest{Schema: 1, Identity: id, Version: version, Arch: architecture(), RuntimeMajor: 1, ProtocolMajor: 1, ArtifactDigest: packageverify.Digest(raw), Dependencies: deps, AllowClean: true, HandoverRequired: true}
	manifest := signed(key, m)
	now := time.Now().UTC()
	c := packageverify.Catalog{Schema: 1, KeyID: "synthetic", Sequence: 1, Expires: now.Add(time.Hour), Entries: []packageverify.Entry{{Identity: id, Version: version, Arch: m.Arch, ArtifactDigest: m.ArtifactDigest, ManifestDigest: packageverify.Digest(manifest.Data)}}}
	return Bundle{Catalog: signed(key, c), Manifest: manifest, ArtifactSignature: ed25519.Sign(key, packageverify.ArtifactStatement(packageverify.Digest(manifest.Data), m.ArtifactDigest))}, raw
}
func fixtureEngine(t *testing.T) (*Engine, ed25519.PrivateKey) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	e := New(Config{Root: filepath.Join(t.TempDir(), "estate"), Authority: packageverify.Authority{KeyID: "synthetic", PublicKey: key.Public().(ed25519.PublicKey)}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := e.initialize(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	close(e.initialized)
	t.Cleanup(e.Close)
	return e, key
}
func stage(t *testing.T, e *Engine, b Bundle, raw []byte) {
	t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	if err := e.Stage(ctx, []Bundle{b}, [][]byte{raw}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}
func TestProductionLifecycleAndOfflineReboot(t *testing.T) {
	e, key := fixtureEngine(t)
	b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
	stage(t, e, b, raw)
	status := e.Snapshot().Modules[0]
	if !status.ServiceHealthy || status.UIHealthy || status.Phase != "ACTIVE" {
		t.Fatal(status)
	}
	old := e.currentUnit("synthetic")
	// Actual module execution, completion and persistent state precede handover.
	now := time.Now().UTC()
	if _, err := e.router.Admit("synthetic/tick", timeline.Occurrence{ID: "synthetic/work", Logical: now}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.router.Dispatch(old.ctx, old.process.Session); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := old.Export(ctx, false); err != nil {
		t.Fatal(err)
	} // ordered IPC barrier after completion
	if e.exec.Snapshot().Records["synthetic/work"].Phase != "completed" {
		t.Fatal("execution not durably completed")
	}
	b2, raw2 := bundle(t, key, "synthetic", "2.0.0", "normal")
	stage(t, e, b2, raw2)
	next := e.currentUnit("synthetic")
	if next.identity.Generation <= old.identity.Generation {
		t.Fatal("epoch reused")
	}
	retained, err := old.stores["persistent"].Read("counter")
	if err != nil || string(retained) != "1" {
		t.Fatal("lost retained state", string(retained), err)
	}
	transferred, err := next.stores["persistent"].Read("counter")
	if err != nil || string(transferred) != "1" {
		t.Fatal("handover omitted state", string(transferred), err)
	}
	if err = e.router.WithAuthority(old.identity, "", func() error { return nil }); err != module.ErrFenced {
		t.Fatal("old action authority", err)
	}
	if _, err = e.router.AdmitFrom(old.identity, "synthetic/tick", timeline.Occurrence{ID: "late", Logical: now}, now); err != module.ErrFenced {
		t.Fatal("draining trigger admitted", err)
	}
	if err = e.Rollback(ctx, "synthetic", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if e.currentUnit("synthetic").identity.Version != "1.0.0" {
		t.Fatal("rollback did not use retained package")
	}
	if err = e.SetDesired("synthetic", false); err != nil {
		t.Fatal(err)
	}
	e.Close()
	restarted := New(e.config)
	if err = restarted.initialize(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	if err = restarted.Reconcile(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if len(restarted.units) != 0 || restarted.Snapshot().Modules[0].Desired {
		t.Fatal("disabled module restarted")
	}
	if err = restarted.SetDesired("synthetic", true); err != nil {
		t.Fatal(err)
	}
	if err = restarted.Reconcile(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if restarted.currentUnit("synthetic") == nil {
		t.Fatal("selected module not restored offline")
	}
	if restarted.boot == e.boot {
		t.Fatal("boot identity reused")
	}
	removedIdentity := restarted.currentUnit("synthetic").identity
	if err = restarted.Remove("synthetic"); err != nil {
		t.Fatal(err)
	}
	if _, err = module.OpenState(restarted.stateRoot("persistent"), removedIdentity); err != nil {
		t.Fatal("removal discarded data", err)
	}
}
func TestFailedPreparationAndDependencyTransaction(t *testing.T) {
	e, key := fixtureEngine(t)
	old, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
	stage(t, e, old, raw)
	selected := e.currentUnit("synthetic")
	broken, raw := bundle(t, key, "synthetic", "2.0.0", "fail_ready")
	ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
	defer c()
	if err := e.Stage(ctx, []Bundle{broken}, [][]byte{raw}, time.Now().UTC()); err == nil {
		t.Fatal("failed candidate accepted")
	}
	if e.currentUnit("synthetic") != selected || e.inv.Modules["synthetic"].Current.declaration().Version != "1.0.0" {
		t.Fatal("failed preparation changed selection")
	}
	dep, depRaw := bundle(t, key, "dependency", "1.0.0", "normal")
	dependent, dependentRaw := bundle(t, key, "dependent", "1.0.0", "normal", packageverify.Dependency{Identity: "dependency", Version: "1.0.0"})
	if err := e.Stage(ctx, []Bundle{dependent}, [][]byte{dependentRaw}, time.Now().UTC()); err != ErrDependency {
		t.Fatal("missing required dependency", err)
	}
	if _, ok := e.inv.Modules["dependent"]; ok {
		t.Fatal("partial inventory commit")
	}
	if err := e.Stage(ctx, []Bundle{dep, dependent}, [][]byte{depRaw, dependentRaw}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := e.SetDesired("dependency", false); err != ErrDependency {
		t.Fatal("disabled required dependency", err)
	}
	if err := e.Remove("dependency"); err != ErrDependency {
		t.Fatal("removed required dependency", err)
	}
	optional, optionalRaw := bundle(t, key, "optional", "1.0.0", "normal", packageverify.Dependency{Identity: "absent", Version: "1.0.0", Optional: true})
	stage(t, e, optional, optionalRaw)
}
func TestMissingCorruptAndTamperedDurabilityIsPreserved(t *testing.T) {
	for _, fault := range []string{"missing", "corrupt", "artifact", "state"} {
		t.Run(fault, func(t *testing.T) {
			e, key := fixtureEngine(t)
			b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
			stage(t, e, b, raw)
			retainedState := e.currentUnit("synthetic").stores["persistent"].Reference()
			e.Close()
			path := filepath.Join(e.config.Root, "actions.json")
			switch fault {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(path, []byte("corrupt-preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			case "artifact":
				path = e.archivePath(b.declaration().ArtifactDigest)
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path+".tamper", []byte("tampered-preserve"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+".tamper", path); err != nil {
					t.Fatal(err)
				}
			case "state":
				path = filepath.Join(retainedState, "state.json")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			before, readErr := os.ReadFile(path)
			restarted := New(e.config)
			if err := restarted.initialize(time.Now().UTC()); err == nil {
				t.Fatal("invalid estate silently recovered")
			}
			restarted.Close()
			after, afterErr := os.ReadFile(path)
			if readErr != nil && afterErr == nil || string(before) != string(after) {
				t.Fatal("corruption was overwritten or missing state recreated")
			}
		})
	}
}
func TestSignedTraversalAndWrongELFRejectBeforeLaunch(t *testing.T) {
	e, key := fixtureEngine(t)
	b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
	var m packageverify.Manifest
	_ = json.Unmarshal(b.Manifest.Data, &m)
	m.ArtifactDigest = "../../credential"
	b.Manifest = signed(key, m)
	if _, err := e.verify(b, nil, time.Now().UTC()); err == nil {
		t.Fatal("traversal accepted")
	}
	b, raw = bundle(t, key, "synthetic", "1.0.0", "normal")
	raw[18] ^= 1
	if err := e.Stage(context.Background(), []Bundle{b}, [][]byte{raw}, time.Now().UTC()); err == nil {
		t.Fatal("tampered ELF accepted")
	}
	if len(e.units) != 0 || len(e.inv.Modules) != 0 {
		t.Fatal("invalid package launched or persisted")
	}
	if _, err := durable.NewFile(filepath.Join(e.config.Root, "inventory.json")).Load(); err != nil {
		t.Fatal(err)
	}
}
func TestBoundedCrashQuarantineSurvivesReboot(t *testing.T) {
	e, key := fixtureEngine(t)
	b, raw := bundle(t, key, "synthetic", "1.0.0", "exit_after_activation")
	stage(t, e, b, raw)
	now := time.Now().UTC()
	for attempt := 0; attempt < 4; attempt++ {
		u := e.currentUnit("synthetic")
		if u == nil {
			t.Fatal("restart missing before budget exhausted")
		}
		ctx, c := context.WithTimeout(context.Background(), 3*time.Second)
		select {
		case <-u.readDone:
		case <-ctx.Done():
			t.Fatal("synthetic crash did not occur")
		}
		c()
		if err := e.Reconcile(now); err != nil {
			t.Fatal(err)
		}
		if attempt < 3 {
			now = now.Add(10 * time.Second)
			if err := e.Reconcile(now); err != nil {
				t.Fatal(err)
			}
		}
	}
	data := e.router.Snapshot()
	active := data.Generations[data.Modules["synthetic"].Active]
	if active.Phase != module.Quarantined || active.Version != "1.0.0" {
		t.Fatal(active)
	}
	e.Close()
	restarted := New(e.config)
	if err := restarted.initialize(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	if restarted.router.Snapshot().Generations[active.Number].Phase != module.Quarantined {
		t.Fatal("quarantine reset on reboot")
	}
}

func TestRequiredDisabledDependencyPreparesInActivationTransaction(t *testing.T) {
	e, key := fixtureEngine(t)
	b, raw := bundle(t, key, "dependency", "1.0.0", "normal")
	stage(t, e, b, raw)
	old := e.currentUnit("dependency")
	if err := e.SetDesired("dependency", false); err != nil {
		t.Fatal(err)
	}
	dependent, dependentRaw := bundle(t, key, "dependent", "1.0.0", "normal", packageverify.Dependency{Identity: "dependency", Version: "1.0.0"})
	stage(t, e, dependent, dependentRaw)
	if !e.router.Accepting("dependent") || !e.router.Accepting("dependency") {
		t.Fatal("dependency not selected atomically")
	}
	if e.currentUnit("dependency").identity.Generation == old.identity.Generation {
		t.Fatal("stopped epoch reused")
	}
	if e.router.Snapshot().Modules["dependency"].Failures != 0 {
		t.Fatal("intentional stop counted as crash")
	}
	if err := e.SetDesired("dependent", false); err != nil {
		t.Fatal(err)
	}
	if err := e.SetDesired("dependency", false); err != nil {
		t.Fatal(err)
	}
	broken, brokenRaw := bundle(t, key, "dependent", "2.0.0", "fail_ready", packageverify.Dependency{Identity: "dependency", Version: "1.0.0"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.Stage(ctx, []Bundle{broken}, [][]byte{brokenRaw}, time.Now().UTC()); err == nil {
		t.Fatal("failed readiness accepted")
	}
	if e.inv.Modules["dependency"].Desired || e.router.Accepting("dependency") {
		t.Fatal("failed transaction enabled dependency")
	}
}

func TestRestartPreservesPreviousVersionAndState(t *testing.T) {
	e, key := fixtureEngine(t)
	b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
	stage(t, e, b, raw)
	b, raw = bundle(t, key, "synthetic", "2.0.0", "normal")
	stage(t, e, b, raw)
	old := e.currentUnit("synthetic")
	if err := old.stores["persistent"].Write("retained", []byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	if err := e.Restart("synthetic"); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	current := e.currentUnit("synthetic")
	if current == nil || current.identity.Generation == old.identity.Generation {
		t.Fatal("restart epoch")
	}
	value, err := current.stores["persistent"].Read("retained")
	if err != nil || string(value) != "synthetic" {
		t.Fatal("restart lost state", err)
	}
	sel := e.router.Snapshot().Modules["synthetic"]
	if sel.Failures != 0 || e.router.Snapshot().Generations[sel.Previous].Version != "1.0.0" || e.inv.Modules["synthetic"].Previous.declaration().Version != "1.0.0" {
		t.Fatal("restart overwrote rollback", sel)
	}
}

func TestProductionScheduleAdmissionAndCancellation(t *testing.T) {
	for _, mode := range []string{"schedule", "hold_execution"} {
		t.Run(mode, func(t *testing.T) {
			e, key := fixtureEngine(t)
			b, raw := bundle(t, key, "synthetic", "1.0.0", mode)
			stage(t, e, b, raw)
			u := e.currentUnit("synthetic")
			now := time.Now().UTC()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if mode == "schedule" {
				registered, ok := e.timeline.Snapshot().Schedules["synthetic/tick"]
				if !ok {
					t.Fatal("IPC schedule not registered")
				}
				if err := e.Reconcile(registered.Start.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if len(e.timeline.Snapshot().Pending) != 0 || len(e.exec.Snapshot().Records) != 1 {
					t.Fatal("schedule not durably admitted/acknowledged")
				}
				if err := e.Reconcile(registered.Start.Add(2 * time.Second)); err != nil {
					t.Fatal(err)
				}
				if _, err := u.Export(ctx, false); err != nil {
					t.Fatal(err)
				}
				for _, r := range e.exec.Snapshot().Records {
					if r.Phase != execution.Completed {
						t.Fatal(r)
					}
				}
			} else {
				if _, err := e.router.Admit("synthetic/tick", timeline.Occurrence{ID: "old", Logical: now}, now); err != nil {
					t.Fatal(err)
				}
				if _, err := e.router.Dispatch(ctx, u.process.Session); err != nil {
					t.Fatal(err)
				}
				if _, err := u.Export(ctx, false); err != nil {
					t.Fatal(err)
				}
				if _, err := e.router.Admit("synthetic/tick", timeline.Occurrence{ID: "new", Logical: now}, now); err != nil {
					t.Fatal(err)
				}
				if err := e.Reconcile(now); err != nil {
					t.Fatal(err)
				}
				if _, err := u.Export(ctx, false); err != nil {
					t.Fatal(err)
				}
				if e.exec.Snapshot().Records["old"].Phase != execution.Canceled {
					t.Fatal("cancellation not acknowledged", e.exec.Snapshot().Records)
				}
			}
		})
	}
}

type failingStore struct {
	durable.Store
	err error
}

func (s failingStore) Save([]byte) error { return s.err }

func TestEstateWriteFailureSeparatesPressureFromUncertainRecovery(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "disk_full", true: "uncertain"}[uncertain], func(t *testing.T) {
			e, key := fixtureEngine(t)
			b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
			stage(t, e, b, raw)
			fault := error(syscall.ENOSPC)
			if uncertain {
				fault = durable.ErrUncertain
			}
			e.store = ownedStore{e, failingStore{e.file("inventory"), fault}}
			if err := e.SetDesired("synthetic", false); !errors.Is(err, fault) {
				t.Fatal(err)
			}
			snapshot := e.Snapshot()
			if uncertain {
				if snapshot.Phase != "recovery_required" {
					t.Fatal(snapshot)
				}
			} else {
				if snapshot.Phase != "running" || !snapshot.StorageDegraded || !snapshot.Modules[0].Desired {
					t.Fatal(snapshot)
				}
				if err := e.SetDesired("synthetic", false); !errors.Is(err, ErrStorage) {
					t.Fatal("writes not fenced", err)
				}
			}
		})
	}
}

func TestConcurrentSnapshotsDuringInitializationAndLifecycle(t *testing.T) {
	e, key := fixtureEngine(t)
	b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			_ = e.Snapshot()
		}
	}()
	stage(t, e, b, raw)
	if err := e.SetDesired("synthetic", false); err != nil {
		t.Fatal(err)
	}
	if err := e.SetDesired("synthetic", true); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
}

func TestInterruptedSameBytesUpdateDoesNotInventActivation(t *testing.T) {
	e, key := fixtureEngine(t)
	b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
	stage(t, e, b, raw)
	next, nextRaw := bundle(t, key, "synthetic", "2.0.0", "normal")
	if b.declaration().ArtifactDigest != next.declaration().ArtifactDigest {
		t.Fatal("fixture must use identical executables")
	}
	next.AcceptedAt = time.Now().UTC()
	if _, err := e.verify(next, nextRaw, next.AcceptedAt); err != nil {
		t.Fatal(err)
	}
	d := cloneInventory(e.inv)
	row := d.Modules["synthetic"]
	row.Pending = &next
	d.Modules["synthetic"] = row
	if err := e.save(d); err != nil {
		t.Fatal(err)
	}
	e.Close()
	restarted := New(e.config)
	if err := restarted.initialize(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	row = restarted.inv.Modules["synthetic"]
	if row.Current.declaration().Version != "1.0.0" || row.Pending == nil || row.UpdateError == "" {
		t.Fatal("interrupted update promoted by digest alone", row)
	}
}
