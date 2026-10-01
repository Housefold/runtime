package timeline

import (
	"errors"
	"github.com/housefold/runtime/internal/durable"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

func TestRestartReplay(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := durable.NewFile(filepath.Join(t.TempDir(), "timeline"))
	tl, err := Open(store, start)
	if err != nil {
		t.Fatal(err)
	}
	if err = tl.Put(Schedule{ID: "synthetic", Start: start.Add(time.Minute), Every: time.Minute, Horizon: 3 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	first, err := tl.Advance(start.Add(2*time.Minute), false)
	if err != nil || len(first) != 2 {
		t.Fatal(first, err)
	}
	tl, err = Open(store, start)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tl.Advance(start.Add(10*time.Minute), true)
	if err != nil || len(out) != 3 {
		t.Fatal(out, err)
	}
	for i, o := range out {
		if !o.Recovered || !o.Logical.Equal(start.Add(time.Duration(i+8)*time.Minute)) {
			t.Fatal(o)
		}
	}
	again, err := tl.Advance(start.Add(10*time.Minute), true)
	if err != nil || len(again) != 0 {
		t.Fatal(again, err)
	}
	if len(tl.Snapshot().Pending) != 5 {
		t.Fatal("lost pending")
	}
	snap := tl.Snapshot()
	delete(snap.Pending, first[0].ID)
	if len(tl.Snapshot().Pending) != 5 {
		t.Fatal("ownership")
	}
}

type failing struct {
	durable.Store
	fail bool
}

func (f *failing) Save(b []byte) error {
	if f.fail {
		return syscall.ENOSPC
	}
	return f.Store.Save(b)
}
func TestFailureAndBounds(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &failing{Store: durable.NewFile(filepath.Join(t.TempDir(), "state"))}
	tl, _ := Open(store, start)
	tl.Put(Schedule{ID: "synthetic", Start: start.Add(time.Second), Every: time.Second})
	before := tl.Snapshot()
	store.fail = true
	if _, err := tl.Advance(start.Add(time.Hour), false); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, tl.Snapshot()) {
		t.Fatal("failed commit published")
	}
	store.fail = false
	if _, err := tl.Advance(start.Add(2*time.Hour), false); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, tl.Snapshot()) {
		t.Fatal("overflow moved watermark")
	}
	if err := tl.SetHorizon(MaxHorizon + 1); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}
