package estate

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/housefold/runtime/internal/action"
	"github.com/housefold/runtime/internal/execution"
	"github.com/housefold/runtime/internal/timeline"
)

type uncertainHA struct{ calls int }

func (h *uncertainHA) Call(context.Context, action.Request) (action.Outcome, error) {
	h.calls++
	return action.Unknown, errors.New("synthetic lost response")
}
func cloneTree(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Mkdir(to, 0700); err != nil {
		t.Fatal(err)
	}
	rows, err := readEntries(from, 256)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		src, dst := filepath.Join(from, row.Name()), filepath.Join(to, row.Name())
		info, err := row.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.IsDir() {
			cloneTree(t, src, dst)
			continue
		}
		in, err := os.Open(src)
		if err != nil {
			t.Fatal(err)
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(out, in)
		in.Close()
		out.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestColdBackupRestoresSelectedEstateAndPreservesUnknownActions(t *testing.T) {
	e, key := fixtureEngine(t)
	b, raw := bundle(t, key, "synthetic", "1.0.0", "hold_execution")
	stage(t, e, b, raw)
	identity := e.currentUnit("synthetic").identity
	now := time.Now().UTC()
	_, err := e.router.Admit("synthetic/tick", timeline.Occurrence{ID: "synthetic/running", Logical: now}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.router.Dispatch(e.ctx, e.currentUnit("synthetic").process.Session); err != nil {
		t.Fatal(err)
	}
	transport := &uncertainHA{}
	e.actions, err = action.Open(e.owned("actions"), e.router, transport)
	if err != nil {
		t.Fatal(err)
	}
	request := action.Request{ID: "synthetic-unknown", Domain: "light", Service: "turn_on", Data: []byte(`{}`)}
	record, err := e.actions.Submit(context.Background(), identity, request)
	if record.Outcome != action.Unknown || err == nil {
		t.Fatal(record, err)
	}
	b, raw = bundle(t, key, "disabled", "1.0.0", "normal")
	stage(t, e, b, raw)
	if err = e.SetDesired("disabled", false); err != nil {
		t.Fatal(err)
	}
	if err = e.currentUnit("synthetic").stores["persistent"].Write("retained", []byte("synthetic-data")); err != nil {
		t.Fatal(err)
	}
	e.Close()
	if _, err = e.file("backup").Load(); err != nil {
		t.Fatal("cold checkpoint missing", err)
	}
	// Supervisor restores to the same private /data/housefold path on a new
	// host. Move the old test estate aside, then restore only the captured bytes.
	restore := e.config.Root
	archive := restore + "-old"
	if err = os.Rename(restore, archive); err != nil {
		t.Fatal(err)
	}
	cloneTree(t, archive, restore)
	cfg := e.config
	cfg.Actions = transport
	recovered := New(cfg)
	t.Cleanup(recovered.Close)
	if err = recovered.initialize(now.Add(48 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if r := recovered.exec.Snapshot().Records["synthetic/running"]; r.Phase != execution.Interrupted {
		t.Fatal("old running work resumed", r)
	}
	if err = recovered.Reconcile(now.Add(48 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if recovered.currentUnit("disabled") != nil || recovered.currentUnit("synthetic") == nil {
		t.Fatal("desired intent lost")
	}
	if recovered.boot == identity.Boot || recovered.currentUnit("synthetic").identity.Generation == identity.Generation {
		t.Fatal("stale authority restored")
	}
	got, err := recovered.currentUnit("synthetic").stores["persistent"].Read("retained")
	if err != nil || string(got) != "synthetic-data" {
		t.Fatal(string(got), err)
	}
	record, err = recovered.actions.Submit(context.Background(), identity, request)
	if err != nil || record.Outcome != action.Unknown || transport.calls != 1 {
		t.Fatal("unknown action replayed", record, err, transport.calls)
	}
}
func TestColdRestoreRejectsPartialOrTamperedSnapshotBeforeWrites(t *testing.T) {
	for _, kind := range []string{"missing", "changed", "extra", "linked"} {
		t.Run(kind, func(t *testing.T) {
			e, key := fixtureEngine(t)
			b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
			stage(t, e, b, raw)
			e.Close()
			path := filepath.Join(e.config.Root, "timeline.json")
			before, _ := os.ReadFile(path)
			switch kind {
			case "missing":
				os.Remove(path)
			case "changed":
				os.WriteFile(path, []byte("corrupt"), 0600)
			case "extra":
				os.WriteFile(filepath.Join(e.config.Root, "unexpected"), []byte("extra"), 0600)
			case "linked":
				os.Symlink(path, filepath.Join(e.config.Root, "linked"))
			}
			clone := New(e.config)
			defer clone.Close()
			if err := clone.initialize(time.Now()); err == nil {
				t.Fatal("unsafe restore accepted")
			}
			after, _ := os.ReadFile(path)
			if kind == "changed" && string(after) != "corrupt" {
				t.Fatal("corruption overwritten")
			}
			if kind == "extra" || kind == "linked" {
				if string(before) != string(after) {
					t.Fatal("rejected restore modified durable state")
				}
			}
			if len(clone.units) != 0 {
				t.Fatal("work admitted before restore verification")
			}
		})
	}
}
func TestTargetedCleanupAndExplicitFactoryReset(t *testing.T) {
	e, key := fixtureEngine(t)
	b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
	stage(t, e, b, raw)
	old := e.currentUnit("synthetic")
	if err := old.stores["persistent"].Write("keep", []byte("durable")); err != nil {
		t.Fatal(err)
	}
	_ = old.stores["cache"].Write("drop", []byte("volatile"))
	if err := e.ClearVolatile(context.Background(), "synthetic"); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(time.Now()); err != nil {
		t.Fatal(err)
	}
	next := e.currentUnit("synthetic")
	if next == nil || next.identity.Generation == old.identity.Generation {
		t.Fatal("reset resumed stale child")
	}
	if got, err := next.stores["persistent"].Read("keep"); err != nil || string(got) != "durable" {
		t.Fatal(string(got), err)
	}
	if got, err := next.stores["cache"].Read("drop"); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if err := e.FactoryReset(context.Background(), "wrong"); !errors.Is(err, ErrOperation) || !e.router.Accepting("synthetic") {
		t.Fatal("unconfirmed reset acted", err)
	}
	if err := e.FactoryReset(context.Background(), FactoryResetConfirmation); !errors.Is(err, ErrRestartRequired) {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(e.config.Root)
	if len(entries) != 0 {
		t.Fatal("reset retained owned bytes", entries)
	}
	fresh := New(e.config)
	defer fresh.Close()
	if err := fresh.initialize(time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(fresh.inv.Modules) != 0 || len(fresh.router.Snapshot().Generations) != 0 || len(fresh.exec.Snapshot().Records) != 0 {
		t.Fatal("not fresh install")
	}
}
func TestInterruptedResetRequiresExplicitResumeAndCorruptStateCanReset(t *testing.T) {
	e, key := fixtureEngine(t)
	b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
	stage(t, e, b, raw)
	e.Close()
	if err := os.WriteFile(filepath.Join(e.config.Root, "inventory.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.file("reset-intent").Save([]byte(`{"Version":1,"Explicit":true}`)); err != nil {
		t.Fatal(err)
	}
	recovered := New(e.config)
	defer recovered.Close()
	if err := recovered.initialize(time.Now()); !errors.Is(err, ErrRecovery) {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(e.config.Root, "inventory.json")); string(raw) != "corrupt" {
		t.Fatal("interrupted reset auto-discarded corruption")
	}
	if err := recovered.FactoryReset(context.Background(), FactoryResetConfirmation); !errors.Is(err, ErrRestartRequired) {
		t.Fatal(err)
	}
}
func TestConcurrentCoordinationWritesAndCleanupJoin(t *testing.T) {
	e, key := fixtureEngine(t)
	b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
	stage(t, e, b, raw)
	for i := 0; i < 30; i++ {
		begin := make(chan struct{})
		done := make(chan error, 2)
		go func() {
			<-begin
			done <- e.exec.Configure(execution.Definition{ID: "synthetic/test", Module: "synthetic", Mode: execution.Parallel, Concurrency: 1, Queue: 4, TTL: time.Hour})
		}()
		go func() { <-begin; done <- e.Cleanup() }()
		close(begin)
		for j := 0; j < 2; j++ {
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("coordination/cleanup lock cycle")
			}
		}
	}
}

func TestRebootScheduleReplayStaysWithinDeclaredHorizon(t *testing.T) {
	e, key := fixtureEngine(t)
	b, raw := bundle(t, key, "synthetic", "1.0.0", "normal")
	stage(t, e, b, raw)
	start := time.Now().UTC()
	if err := e.timeline.Put(timeline.Schedule{ID: "synthetic/tick", Start: start, Every: time.Minute, Horizon: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if err := e.SetDesired("synthetic", false); err != nil {
		t.Fatal(err)
	}
	e.Close()
	restarted := New(e.config)
	defer restarted.Close()
	now := start.Add(48 * time.Hour)
	if err := restarted.initialize(now); err != nil {
		t.Fatal(err)
	}
	pending := restarted.timeline.Snapshot().Pending
	if len(pending) != 60 {
		t.Fatal("unbounded or incomplete horizon replay", len(pending))
	}
	for _, occurrence := range pending {
		if !occurrence.Recovered || !occurrence.Logical.After(now.Add(-time.Hour)) || occurrence.Logical.After(now) {
			t.Fatal(occurrence)
		}
	}
	if _, err := restarted.timeline.Advance(now, true); err != nil {
		t.Fatal(err)
	}
	if len(restarted.timeline.Snapshot().Pending) != 60 {
		t.Fatal("duplicate replay")
	}
	if restarted.Snapshot().Modules[0].Desired {
		t.Fatal("disabled intent lost during replay")
	}
}

func TestExplicitResetUnlinksOwnedLinksWithoutFollowing(t *testing.T) {
	e, _ := fixtureEngine(t)
	sentinel := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, filepath.Join(e.config.Root, "owned-link")); err != nil {
		t.Fatal(err)
	}
	if err := e.FactoryReset(context.Background(), FactoryResetConfirmation); !errors.Is(err, ErrRestartRequired) {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(sentinel); string(got) != "preserve" {
		t.Fatal("explicit reset followed link")
	}
}
