package audit

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/housefold/runtime/internal/durable"
)

const admin = "0123456789abcdef0123456789abcdef"

func TestDurableAuditRetentionAttributionRecoveryAndIsolation(t *testing.T) {
	store := durable.NewFile(filepath.Join(t.TempDir(), "audit.json"))
	j, err := Open(store, true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxEntries+40; i++ {
		id, err := j.Begin(admin, "restart", "synthetic", time.Unix(int64(i+1), 0))
		if err != nil {
			t.Fatal(err)
		}
		if i != MaxEntries+39 {
			if err = j.Finish(id, "completed"); err != nil {
				t.Fatal(err)
			}
		}
	}
	rows := j.Entries()
	if len(rows) != MaxEntries || rows[0].Sequence != 41 || rows[len(rows)-1].User != admin {
		t.Fatal(rows)
	}
	rows[0].User = "MUTATED"
	if j.Entries()[0].User != admin {
		t.Fatal("snapshot alias")
	}
	restored, err := Open(store, false)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Entries()[MaxEntries-1].Outcome != "interrupted" {
		t.Fatal("interrupted intent retried/misrepresented")
	}
	if restored.Finish(MaxEntries+40, "completed") != ErrUnavailable {
		t.Fatal("prior boot intent accepted")
	}
}

type failingStore struct {
	durable.Store
	err error
}

func (s *failingStore) Save(raw []byte) error {
	if s.err != nil {
		return s.err
	}
	return s.Store.Save(raw)
}
func TestAuditCommitFailurePreservesJournalAndPoisonsUncertainty(t *testing.T) {
	store := &failingStore{Store: durable.NewFile(filepath.Join(t.TempDir(), "audit.json"))}
	j, err := Open(store, true)
	if err != nil {
		t.Fatal(err)
	}
	store.err = errors.New("disk full")
	if _, err = j.Begin(admin, "stop", "synthetic", time.Now()); err == nil || len(j.Entries()) != 0 {
		t.Fatal("failed commit published")
	}
	store.err = durable.ErrUncertain
	if _, err = j.Begin(admin, "stop", "synthetic", time.Now()); !errors.Is(err, durable.ErrUncertain) {
		t.Fatal(err)
	}
	store.err = nil
	if _, err = j.Begin(admin, "stop", "synthetic", time.Now()); err != ErrUnavailable {
		t.Fatal("uncertainty unfenced", err)
	}
}
func TestCorruptMissingAndInvalidAuditNeverRecreated(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "audit.json")
	store := durable.NewFile(path)
	if _, err := Open(store, false); err != ErrUnavailable {
		t.Fatal("missing journal recreated")
	}
	raw := []byte("CORRUPT_SYNTHETIC")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(store, true); err != ErrUnavailable {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(raw) {
		t.Fatal("corrupt journal discarded")
	}
	for _, d := range []data{
		{Version: 2}, {Version: 1, Sequence: 9},
		{Version: 1, Sequence: 1, Entries: []Entry{{1, time.Now(), admin, "arbitrary", "synthetic", "requested"}}},
		{Version: 1, Sequence: 1, Entries: []Entry{{1, time.Now(), admin, "restart", "../../foreign", "requested"}}},
		{Version: 1, Sequence: 2, Entries: []Entry{{2, time.Now(), admin, "restart", "synthetic", "completed"}, {1, time.Now(), admin, "restart", "synthetic", "completed"}}},
	} {
		raw, _ := json.Marshal(d)
		if err := store.Save(raw); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(store, true); err != ErrUnavailable {
			t.Fatal("bad durable shape accepted", d)
		}
	}
}
func TestConcurrentAuditBoundedAndUnique(t *testing.T) {
	j, err := Open(durable.NewFile(filepath.Join(t.TempDir(), "audit.json")), true)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 40; n++ {
				if _, err := j.Begin(admin, "start", "synthetic", time.Now()); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	rows := j.Entries()
	if len(rows) != MaxEntries || rows[MaxEntries-1].Sequence != 320 {
		t.Fatal("concurrent journal lost records")
	}
}

func TestAuditAcceptsFullSignedIdentityLength(t *testing.T) {
	j, err := Open(durable.NewFile(filepath.Join(t.TempDir(), "audit.json")), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = j.Begin(admin, "install", strings.Repeat("a", 128), time.Now()); err != nil {
		t.Fatal("signed identity rejected", err)
	}
	if _, err = j.Begin(admin, "install", strings.Repeat("a", 129), time.Now()); err == nil {
		t.Fatal("excessive identity accepted")
	}
}

func TestMalformedInitializedAuditCannotBecomeEmpty(t *testing.T) {
	store := durable.NewFile(filepath.Join(t.TempDir(), "audit.json"))
	for _, raw := range []string{`null`, `{}`, `{"Version":1,"unknown":"PRIVATE"}`} {
		if err := store.Save([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(store, false); err != ErrUnavailable {
			t.Fatal("malformed journal silently initialized", raw, err)
		}
		preserved, err := store.Load()
		if err != nil || string(preserved) != raw {
			t.Fatal("malformed journal changed", raw, err)
		}
	}
}
