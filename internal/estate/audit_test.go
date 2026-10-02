package estate

import (
	"context"
	"github.com/housefold/runtime/internal/diagnostics"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const syntheticAdmin = "0123456789abcdef0123456789abcdef"

func TestEstateAuditIntegrityRecoveryAndShutdownFencing(t *testing.T) {
	e, _ := fixtureEngine(t)
	n, err := e.BeginAdmin(syntheticAdmin, "cleanup", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err = e.FinishAdmin(n, true, false); err != nil {
		t.Fatal(err)
	}
	n, err = e.BeginAdmin(syntheticAdmin, "restart", "synthetic", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	before, err := os.ReadFile(filepath.Join(e.config.Root, "audit.json"))
	if err != nil {
		t.Fatal(err)
	}
	if e.FinishAdmin(n, true, false) == nil {
		t.Fatal("audit write passed stopped estate checkpoint")
	}
	after, err := os.ReadFile(filepath.Join(e.config.Root, "audit.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("post-shutdown audit changed checkpoint")
	}
	next := New(e.config)
	if err = next.initialize(time.Now()); err != nil {
		t.Fatal("normal audited shutdown failed restore", err)
	}
	t.Cleanup(next.Close)
	rows := next.AuditSnapshot().Entries
	if len(rows) != 2 || rows[0].User != syntheticAdmin || rows[0].Outcome != "completed" || rows[1].Outcome != "interrupted" {
		t.Fatal(rows)
	}
	// A marked missing journal is corruption, never a fresh store.
	if err = os.Remove(filepath.Join(e.config.Root, "audit.json")); err != nil {
		t.Fatal(err)
	}
	missing := New(e.config)
	if err = missing.initialize(time.Now()); err == nil {
		t.Fatal("missing marked audit silently recreated")
	}
	if _, err = os.Stat(filepath.Join(e.config.Root, "audit.json")); !os.IsNotExist(err) {
		t.Fatal("missing audit recreated", err)
	}
}
func TestConfirmedResetCanRecoverUnwritableAudit(t *testing.T) {
	e, _ := fixtureEngine(t)
	e.mu.Lock()
	e.storageDegraded = true
	e.mu.Unlock()
	if _, err := e.BeginAdmin(syntheticAdmin, "cleanup", "", time.Now()); err == nil {
		t.Fatal("ordinary operation passed unauditable storage")
	}
	n, err := e.BeginAdmin(syntheticAdmin, "factory_reset", "", time.Now())
	if err != nil || n != 0 {
		t.Fatal("reset trapped by failed audit", n, err)
	}
	if err = e.FactoryReset(context.Background(), FactoryResetConfirmation); err != ErrRestartRequired {
		t.Fatal(err)
	}
	if err = e.FinishAdmin(n, true, true); err != nil {
		t.Fatal(err)
	}
	if rows, err := os.ReadDir(e.config.Root); err != nil || len(rows) != 0 {
		t.Fatal("reset recreated audit", len(rows), err)
	}
}

type syntheticLogSink struct{}

func (syntheticLogSink) Write(context.Context, []byte) error { return nil }
func (syntheticLogSink) Close() error                        { return nil }
func TestNativeModuleIPCLogsAttributedAndFloodConfined(t *testing.T) {
	e, key := fixtureEngine(t)
	logs := diagnostics.New(syntheticLogSink{})
	defer logs.Close()
	e.config.Logger = slog.New(logs)
	normal, raw := bundle(t, key, "healthy", "1.0.0", "normal")
	stage(t, e, normal, raw)
	healthy := e.currentUnit("healthy")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Private ordered export barrier follows activated + module's ready log.
	if _, err := healthy.Export(ctx, false); err != nil {
		t.Fatal(err)
	}
	rows := logs.Snapshot().Entries
	found := false
	for _, row := range rows {
		if row.Module == "healthy" && row.Version == "1.0.0" && row.Generation == healthy.identity.Generation && row.Code == "ready" {
			found = true
		}
	}
	if !found {
		t.Fatal("actual module log lacked launcher attribution", rows)
	}
	flood, raw := bundle(t, key, "flood", "1.0.0", "log_flood")
	stage(t, e, flood, raw)
	child := e.currentUnit("flood")
	select {
	case <-child.readDone:
	case <-time.After(3 * time.Second):
		t.Fatal("IPC flood was not fenced")
	}
	if !e.router.Accepting("healthy") || e.Snapshot().Phase != "running" {
		t.Fatal("module log flood became Runtime/sibling failure")
	}
	if len(logs.Snapshot().Entries) > diagnostics.MaxLogs {
		t.Fatal("log flood exceeded retention")
	}
}
