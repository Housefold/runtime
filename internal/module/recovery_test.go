package module

import (
	"github.com/housefold/runtime/internal/timeline"
	"strings"
	"testing"
	"time"
)

func TestRetentionAndManualRollback(t *testing.T) {
	r, _, _, now, v1 := routerFixture(t)
	root := t.TempDir()
	a, _ := AllocateState(root, v1)
	a.Write("state", []byte("v1")) // Initial fixture already active: references are normally bound before readiness.
	r.mu.Lock()
	d := routerCopy(r.data)
	g := d.Generations[v1.Generation]
	g.ArtifactDigest = strings.Repeat("a", 64)
	g.StatePath = a.path
	d.Generations[g.Number] = g
	r.commit(d)
	r.mu.Unlock()
	v2, _ := r.Prepare("synthetic", "2")
	b, _ := AllocateState(root, v2)
	b.Write("state", []byte("v2"))
	if err := r.BindReferences(v2, strings.Repeat("b", 64), b); err != nil {
		t.Fatal(err)
	}
	r.Ready(v2, true, nil)
	r.Cutover(v2, now)
	r.Drain(now)
	if old, err := r.Collectable("synthetic"); err != nil || len(old) != 0 {
		t.Fatal(old, err)
	}
	v3, _ := r.Prepare("synthetic", "3")
	r.Ready(v3, true, nil)
	r.Cutover(v3, now)
	r.Drain(now)
	old, err := r.Collectable("synthetic")
	if err != nil || len(old) != 1 || old[0].Number != v1.Generation {
		t.Fatal(old, err)
	}
	rollback, err := r.PrepareRollback("synthetic", root)
	if err != nil || rollback.Version != "2" || rollback.Generation <= v3.Generation {
		t.Fatal(rollback, err)
	}
	copy, _ := AllocateState(root, rollback)
	copy.Write("state", []byte("rollback"))
	raw, _ := b.Read("state")
	if string(raw) != "v2" {
		t.Fatal("rollback shared write ownership")
	}
	if r.Cutover(rollback, now) != ErrFenced {
		t.Fatal("rollback skipped readiness")
	}
	r.Ready(rollback, true, nil)
	if err = r.Cutover(rollback, now); err != nil {
		t.Fatal(err)
	}
	if r.WithAuthority(v2, "", func() error { return nil }) != ErrFenced {
		t.Fatal("resurrected old authority")
	}
}
func TestCrashBudgetAndOperatorRecovery(t *testing.T) {
	r, _, store, now, id := routerFixture(t)
	root := t.TempDir()
	for i := 1; i <= MaxRestarts+1; i++ {
		if err := r.Crash(id, now); err != nil {
			t.Fatal(err)
		}
		r.Crash(id, now)
		if r.Snapshot().Modules[id.Module].Failures != i {
			t.Fatal("duplicate crash consumed budget")
		}
		if _, err := r.Admit("automation", timeline.Occurrence{ID: "new", Logical: now}, now); err != ErrFenced {
			t.Fatal(err)
		}
		if i > MaxRestarts {
			break
		}
		if _, err := r.RestartCandidate(id.Module, root, now); err != ErrFenced {
			t.Fatal("backoff ignored")
		}
		now = now.Add(time.Second * time.Duration(1<<uint(i-1)))
		next, err := r.RestartCandidate(id.Module, root, now)
		if err != nil || next.Version != "1" {
			t.Fatal(next, err)
		}
		r.Ready(next, true, nil)
		if err = r.Cutover(next, now); err != nil {
			t.Fatal(err)
		}
		r.Drain(now)
		id = next
	}
	if r.Snapshot().Generations[id.Generation].Phase != Quarantined {
		t.Fatal("not quarantined")
	}
	if _, err := r.RestartCandidate(id.Module, root, now.Add(time.Hour)); err != ErrFenced {
		t.Fatal("quarantine restarted")
	}
	other, err := r.Prepare("other", "1")
	if err != nil {
		t.Fatal("unrelated module blocked", err)
	}
	r.Ready(other, true, nil)
	r.Cutover(other, now)
	restored, err := OpenRouter(store, r.exec, "new-boot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restored.RestartCandidate(id.Module, root, now.Add(time.Hour)); err != ErrFenced {
		t.Fatal("restart cleared quarantine")
	}
	if err = restored.OperatorRecover(id.Module, now); err != nil {
		t.Fatal(err)
	}
	next, err := restored.RestartCandidate(id.Module, root, now)
	if err != nil || next.Generation <= id.Generation {
		t.Fatal(next, err)
	}
}
func TestFailedRestartCandidatesExhaust(t *testing.T) {
	r, _, _, now, id := routerFixture(t)
	r.Crash(id, now)
	root := t.TempDir()
	for i := 1; i <= MaxRestarts; i++ {
		now = r.Snapshot().Modules[id.Module].NextRestart
		candidate, err := r.RestartCandidate(id.Module, root, now)
		if err != nil {
			t.Fatal(err)
		}
		if err = r.FailCandidateAt(candidate, now); err != nil {
			t.Fatal(err)
		}
	}
	if r.Snapshot().Generations[id.Generation].Phase != Quarantined {
		t.Fatal("failed candidates retry forever")
	}
	if r.Ready(id, true, nil) != ErrFenced {
		t.Fatal("crashed authority revived")
	}
}
