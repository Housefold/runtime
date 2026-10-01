package supervisor

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/housefold/runtime/internal/estate"
)

func TestBIOSRealEstateRecoveryResetDoesNotSilentlyDiscard(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "inventory.json")
	sentinel := []byte("CORRUPT_SYNTHETIC_STATE_PRESERVE_UNTIL_CONFIRMED")
	if err := os.WriteFile(path, sentinel, 0600); err != nil {
		t.Fatal(err)
	}
	store := &StatusStore{}
	owner := estate.New(estate.Config{Root: root, OnRecovery: store.SetRecoveryRequired})
	owner.EnterRecovery()
	store.SetEstate(owner)
	store.SetManagement(owner, testAuthorizer(func(_ context.Context, id string) error {
		if id != testAdmin {
			return errManagement
		}
		return nil
	}))
	s := NewService(store)
	if w := biosRequest(s, "GET", "/", testAdmin, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "recovery_required") {
		t.Fatal(w.Code, w.Body.String())
	}
	key, _ := s.issue(testAdmin)
	form := url.Values{"csrf": {key}, "operation": {"factory_reset"}}
	if w := biosRequest(s, "POST", "/manage", testAdmin, form); w.Code != 400 {
		t.Fatal("unconfirmed reset", w.Code)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != string(sentinel) {
		t.Fatal("unconfirmed reset changed data", err)
	}
	form.Set("confirmation", estate.FactoryResetConfirmation)
	if w := biosRequest(s, "POST", "/manage", testAdmin, form); w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.workers.Wait()
	if rows, err := os.ReadDir(root); err != nil || len(rows) != 0 {
		t.Fatal("confirmed reset did not clear owned data", err, len(rows))
	}
	if !strings.Contains(s.operation().Result, "Restart Runtime") {
		t.Fatal(s.operation())
	}
	// The canceled estate cannot resume operations until normal App restart.
	if owner.SetDesired("synthetic", true) != estate.ErrRecovery {
		t.Fatal("post-reset admission")
	}
	fresh := estate.New(estate.Config{Root: root})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); fresh.Run(ctx) }()
	ready, release := context.WithTimeout(context.Background(), time.Second)
	defer release()
	if err := fresh.Ready(ready); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	<-done
}
