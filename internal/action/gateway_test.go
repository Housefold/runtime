package action

import (
	"context"
	"errors"
	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/execution"
	"github.com/housefold/runtime/internal/module"
	"github.com/housefold/runtime/internal/timeline"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type fakeTransport struct {
	calls atomic.Int32
	call  func(context.Context) (Outcome, error)
}

func (f *fakeTransport) Call(ctx context.Context, r Request) (Outcome, error) {
	f.calls.Add(1)
	return f.call(ctx)
}
func fixture(t *testing.T, f Transport) (*Gateway, *module.Router, *execution.Manager, *durable.File, module.Identity, time.Time) {
	dir := t.TempDir()
	m, _ := execution.Open(durable.NewFile(filepath.Join(dir, "exec")))
	m.Configure(execution.Definition{ID: "automation", Module: "synthetic", Mode: execution.Parallel, Concurrency: 2, Queue: 2, TTL: time.Hour})
	r, err := module.OpenRouter(durable.NewFile(filepath.Join(dir, "router")), m, "boot")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.Prepare("synthetic", "1")
	r.Ready(id, true, nil)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.Cutover(id, now)
	store := durable.NewFile(filepath.Join(dir, "actions"))
	g, err := Open(store, r, f)
	if err != nil {
		t.Fatal(err)
	}
	return g, r, m, store, id, now
}
func req(id string) Request {
	return Request{ID: id, Domain: "synthetic", Service: "test", Data: []byte(`{"unknown":true}`)}
}
func TestOutcomesRecoveryAndEvidence(t *testing.T) {
	for _, out := range []Outcome{NotSent, Accepted, Rejected, Unknown} {
		f := &fakeTransport{call: func(context.Context) (Outcome, error) { return out, nil }}
		g, r, _, store, id, _ := fixture(t, f)
		record, err := g.Submit(context.Background(), id, req("action"))
		if err != nil || record.Outcome != out {
			t.Fatal(record, err)
		}
		restored, err := Open(store, r, f)
		if err != nil {
			t.Fatal(err)
		}
		record, err = restored.Submit(context.Background(), id, req("action"))
		if err != nil || record.Outcome != out || f.calls.Load() != 1 {
			t.Fatal(record, err, f.calls.Load())
		}
		if err = restored.Observe(id.Module, "action", Matches); err != nil {
			t.Fatal(err)
		}
		record, _ = restored.Submit(context.Background(), id, req("action"))
		if record.Outcome != out || record.Observation != Matches {
			t.Fatal("observation overwrote outcome")
		}
	}
}
func TestCutoverAndUnknownDedupRace(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := &fakeTransport{call: func(ctx context.Context) (Outcome, error) {
		close(entered)
		<-release
		return Unknown, context.DeadlineExceeded
	}}
	g, r, _, store, v1, now := fixture(t, f)
	work, _ := r.Admit("automation", timeline.Occurrence{ID: "work", Logical: now}, now)
	done := make(chan Record, 1)
	go func() { record, _ := g.Submit(context.Background(), v1, req("first")); done <- record }()
	<-entered
	v2, _ := r.Prepare(v1.Module, "2")
	r.Ready(v2, true, nil)
	r.Cutover(v2, now)
	if record, err := g.Submit(context.Background(), v1, req("fenced")); err != module.ErrFenced || record.Outcome != NotSent {
		t.Fatal(record, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record, err := g.Submit(context.Background(), v1, req("first"))
			if err != nil || record.Outcome != Unknown {
				t.Error(record, err)
			}
		}()
	}
	wg.Wait()
	restored, err := Open(store, r, f)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := restored.Submit(context.Background(), v1, req("first"))
	if record.Outcome != Unknown || f.calls.Load() != 1 {
		t.Fatal("uncertainty resent")
	}
	close(release)
	if record = <-done; record.Outcome != Unknown {
		t.Fatal(record)
	}
	f.call = func(context.Context) (Outcome, error) { return Accepted, nil }
	request := req("drain")
	request.Work = work.ID
	if record, err = g.Submit(context.Background(), v1, request); err != nil || record.Outcome != Accepted {
		t.Fatal(record, err)
	}
}
func TestCancellationTimeoutAndInput(t *testing.T) {
	f := &fakeTransport{call: func(ctx context.Context) (Outcome, error) { <-ctx.Done(); return Unknown, ctx.Err() }}
	g, _, _, _, id, _ := fixture(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	record, err := g.Submit(ctx, id, req("canceled"))
	if !errors.Is(err, context.Canceled) || record.Outcome != NotSent || f.calls.Load() != 0 {
		t.Fatal(record, err)
	}
	bad := req("bad")
	bad.Data = []byte(`[]`)
	if _, err = g.Submit(context.Background(), id, bad); err == nil {
		t.Fatal("bad input")
	}
	entered := make(chan struct{})
	f.call = func(ctx context.Context) (Outcome, error) { close(entered); <-ctx.Done(); return Unknown, ctx.Err() }
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan Record, 1)
	go func() { record, _ := g.Submit(ctx, id, req("timeout")); done <- record }()
	<-entered
	cancel()
	if record = <-done; record.Outcome != Unknown {
		t.Fatal(record)
	}
}

type faultStore struct {
	durable.Store
	saves  int
	failAt int
}

func (f *faultStore) Save(raw []byte) error {
	f.saves++
	if f.saves == f.failAt {
		return syscall.ENOSPC
	}
	return f.Store.Save(raw)
}
func TestDurableFailuresDoNotRetry(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		f := &fakeTransport{call: func(context.Context) (Outcome, error) { return Accepted, nil }}
		g, r, _, store, id, _ := fixture(t, f)
		fault := &faultStore{Store: store, failAt: failAt}
		g.store = fault
		record, err := g.Submit(context.Background(), id, req("fault"))
		if !errors.Is(err, syscall.ENOSPC) {
			t.Fatal(err)
		}
		if failAt == 1 {
			if record.Outcome != NotSent || f.calls.Load() != 0 {
				t.Fatal(record)
			}
		} else {
			if record.Outcome != Unknown || f.calls.Load() != 1 {
				t.Fatal(record)
			}
			restored, err := Open(store, r, f)
			if err != nil {
				t.Fatal(err)
			}
			record, err = restored.Submit(context.Background(), id, req("fault"))
			if err != nil || record.Outcome != Unknown || f.calls.Load() != 1 {
				t.Fatal("failed response commit resent", record, err)
			}
		}
	}
}
