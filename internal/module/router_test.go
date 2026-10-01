package module

import (
	"context"
	"fmt"
	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/execution"
	"github.com/housefold/runtime/internal/timeline"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func routerFixture(t *testing.T) (*Router, *execution.Manager, *durable.File, time.Time, Identity) {
	t.Helper()
	dir := t.TempDir()
	m, err := execution.Open(durable.NewFile(filepath.Join(dir, "exec")))
	if err != nil {
		t.Fatal(err)
	}
	m.Configure(execution.Definition{ID: "automation", Module: "synthetic", Mode: execution.Parallel, Concurrency: 32, Queue: 64, TTL: time.Hour})
	store := durable.NewFile(filepath.Join(dir, "router"))
	r, err := OpenRouter(store, m, "boot")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	id, err := r.Prepare("synthetic", "1")
	if err != nil {
		t.Fatal(err)
	}
	r.Ready(id, true, nil)
	if err = r.Cutover(id, now); err != nil {
		t.Fatal(err)
	}
	return r, m, store, now, id
}
func TestReadinessAndFailure(t *testing.T) {
	r, _, _, now, v1 := routerFixture(t)
	v2, _ := r.Prepare("synthetic", "2")
	if r.Cutover(v2, now) != ErrFenced {
		t.Fatal("unready cutover")
	}
	o := timeline.Occurrence{ID: "before", Logical: now}
	record, err := r.Admit("automation", o, now)
	if err != nil || record.Generation != v1.Generation {
		t.Fatal(record, err)
	}
	if err = r.FailCandidate(v2); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().Modules["synthetic"].Active != v1.Generation {
		t.Fatal("lost active")
	}
}
func TestCutoverRaceExactlyOnce(t *testing.T) {
	r, m, store, now, v1 := routerFixture(t)
	v2, _ := r.Prepare("synthetic", "2")
	r.Ready(v2, true, nil)
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 101)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := r.Admit("automation", timeline.Occurrence{ID: fmt.Sprint(i), Logical: now}, now)
			errs <- err
		}(i)
	}
	wg.Add(1)
	go func() { defer wg.Done(); <-start; errs <- r.Cutover(v2, now) }()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	data := m.Snapshot()
	if len(data.Records) != 100 {
		t.Fatal(len(data.Records))
	}
	for _, record := range data.Records {
		if record.Generation != v1.Generation && record.Generation != v2.Generation {
			t.Fatal(record)
		}
	}
	for i := 0; i < 100; i++ {
		record, err := r.Admit("automation", timeline.Occurrence{ID: fmt.Sprint(i), Logical: now}, now)
		if err != nil || record.Generation != data.Records[record.ID].Generation {
			t.Fatal(record, err)
		}
	}
	if r.WithAuthority(v1, "", func() error { return nil }) != ErrFenced {
		t.Fatal("old admits new work")
	}
	if err := r.Drain(now.Add(DrainBudget)); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().Generations[v1.Generation].Phase != Retired {
		t.Fatal("unbounded drain")
	}
	reopened, err := OpenRouter(store, m, "new-boot")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.WithAuthority(v2, "", func() error { return nil }) != ErrFenced {
		t.Fatal("stale boot")
	}
	if _, err = reopened.Admit("automation", timeline.Occurrence{ID: "new", Logical: now}, now); err != ErrFenced {
		t.Fatal("recovery bypassed readiness")
	}
}

type childRecorder struct{ stopped bool }

func (c *childRecorder) Stop(context.Context) error { c.stopped = true; return nil }
func TestDrainStopsOwnedChild(t *testing.T) {
	r, m, _, now, v1 := routerFixture(t)
	child := &childRecorder{}
	r.Ready(v1, true, child)
	work, _ := r.Admit("automation", timeline.Occurrence{ID: "old", Logical: now}, now)
	v2, _ := r.Prepare("synthetic", "2")
	r.Ready(v2, true, nil)
	r.Cutover(v2, now)
	if err := r.WithAuthority(v1, work.ID, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	r.Drain(now)
	if child.stopped {
		t.Fatal("premature stop")
	}
	m.Finish(work.ID, false, now)
	if err := r.Drain(now); err != nil || !child.stopped {
		t.Fatal(err)
	}
}
