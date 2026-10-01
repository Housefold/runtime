package execution

import (
	"fmt"
	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/timeline"
	"path/filepath"
	"testing"
	"time"
)

func setup(t *testing.T, mode Mode) (*Manager, *durable.File, time.Time) {
	t.Helper()
	s := durable.NewFile(filepath.Join(t.TempDir(), "exec"))
	m, err := Open(s)
	if err != nil {
		t.Fatal(err)
	}
	err = m.Configure(Definition{ID: "automation", Module: "synthetic", Mode: mode, Concurrency: 2, Queue: 2, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return m, s, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}
func occurrence(id string, now time.Time) timeline.Occurrence {
	return timeline.Occurrence{ID: id, Schedule: "synthetic", Logical: now}
}
func TestModes(t *testing.T) {
	for _, mode := range []Mode{Single, Restart, Queued, Parallel} {
		t.Run(string(mode), func(t *testing.T) {
			m, _, now := setup(t, mode)
			for i := 0; i < 5; i++ {
				if _, err := m.Admit("automation", occurrence(fmt.Sprint(i), now), now); err != nil {
					t.Fatal(err)
				}
			}
			d := m.Snapshot()
			counts := map[Phase]int{}
			for _, r := range d.Records {
				counts[r.Phase]++
			}
			switch mode {
			case Single:
				if counts[Running] != 1 || counts[Dropped] != 4 {
					t.Fatal(counts)
				}
			case Restart:
				if counts[Canceling] != 1 || counts[Pending] != 1 || counts[Canceled] != 3 {
					t.Fatal(counts)
				}
				if err := m.Finish("0", true, now); err != nil {
					t.Fatal(err)
				}
				if m.Snapshot().Records["4"].Phase != Running {
					t.Fatal("restart before cancellation ack")
				}
			case Queued:
				if counts[Running] != 1 || counts[Pending] != 2 || counts[Dropped] != 2 {
					t.Fatal(counts)
				}
			case Parallel:
				if counts[Running] != 2 || counts[Pending] != 2 || counts[Dropped] != 1 {
					t.Fatal(counts)
				}
			}
		})
	}
}
func TestRecoveryExpiryAndDedup(t *testing.T) {
	m, s, now := setup(t, Queued)
	m.Admit("automation", occurrence("cron", now), now)
	m.Admit("automation", occurrence("delayed", now), now)
	restored, err := Open(s)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Snapshot().Records["cron"].Phase != Interrupted || restored.Snapshot().Records["delayed"].Phase != Pending {
		t.Fatal(restored.Snapshot())
	}
	r, err := restored.Admit("automation", occurrence("cron", now), now)
	if err != nil || r.Phase != Interrupted {
		t.Fatal(r, err)
	}
	restored.Finish("delayed", true, now)
	if err = restored.Retire(now); err != nil {
		t.Fatal(err)
	}
	if _, err = restored.Admit("automation", occurrence("cron", now), now); err != ErrExpired {
		t.Fatal(err)
	}
	r, err = restored.Admit("automation", occurrence("late", now.Add(time.Minute)), now.Add(2*time.Hour))
	if err != nil || r.Phase != Expired {
		t.Fatal(r, err)
	}
}
func TestSequenceModel(t *testing.T) {
	m, _, now := setup(t, Parallel)
	for i := 0; i < 200; i++ {
		id := fmt.Sprint(i)
		r, err := m.Admit("automation", occurrence(id, now), now)
		if err != nil {
			t.Fatal(err)
		}
		if r.Phase == Running {
			m.Finish(id, false, now)
		}
		running, pending := 0, 0
		for _, r := range m.Snapshot().Records {
			if r.Phase == Running {
				running++
			}
			if r.Phase == Pending {
				pending++
			}
		}
		if running > 2 || pending > 2 {
			t.Fatal(running, pending)
		}
	}
}
func TestTimelineCommonAdmission(t *testing.T) {
	m, _, now := setup(t, Queued)
	tl, err := timeline.Open(durable.NewFile(filepath.Join(t.TempDir(), "timeline")), now)
	if err != nil {
		t.Fatal(err)
	}
	tl.Put(timeline.Schedule{ID: "periodic", Start: now.Add(time.Minute), Every: time.Minute})
	tl.Put(timeline.Schedule{ID: "delayed", Start: now.Add(time.Minute)})
	tl.Advance(now.Add(time.Minute), false)
	if err = m.AdmitTimeline(tl, func(string) string { return "automation" }, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(tl.Snapshot().Pending) != 0 || len(m.Snapshot().Records) != 2 {
		t.Fatal("lost or duplicated work")
	}
	if err = m.AdmitTimeline(tl, func(string) string { return "automation" }, now.Add(time.Minute)); err != nil || len(m.Snapshot().Records) != 2 {
		t.Fatal(err)
	}
}
