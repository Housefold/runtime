// Package execution owns bounded durable trigger admission; module logic stays outside.
package execution

import (
	"encoding/json"
	"errors"
	"github.com/housefold/runtime/internal/durable"
	"github.com/housefold/runtime/internal/timeline"
	"os"
	"sort"
	"sync"
	"time"
)

const MaxDefinitions = 128
const MaxRecords = 4096
const MaxQueue = 64
const MaxParallel = 32

var ErrLimit = errors.New("execution bound or invalid request")
var ErrExpired = errors.New("occurrence precedes retained execution horizon")

type Mode string

const (
	Single   Mode = "single"
	Restart  Mode = "restart"
	Queued   Mode = "queued"
	Parallel Mode = "parallel"
)

type Phase string

const (
	Pending     Phase = "pending"
	Running     Phase = "running"
	Canceling   Phase = "canceling"
	Completed   Phase = "completed"
	Canceled    Phase = "canceled"
	Expired     Phase = "expired"
	Dropped     Phase = "dropped"
	Interrupted Phase = "interrupted"
)

type Definition struct {
	ID          string
	Module      string
	Mode        Mode
	Queue       int
	Concurrency int
	TTL         time.Duration
}
type Record struct {
	Dispatched bool
	ID         string
	Definition string
	Module     string
	Generation uint64
	Occurrence timeline.Occurrence
	Phase      Phase
	Sequence   uint64
	Expires    time.Time
}
type Data struct {
	Version     int
	Floor       time.Time
	Sequence    uint64
	Definitions map[string]Definition
	Records     map[string]Record
}
type Manager struct {
	poisoned bool
	mu       sync.Mutex
	store    durable.Store
	data     Data
}

func terminal(p Phase) bool {
	return p == Completed || p == Canceled || p == Expired || p == Dropped || p == Interrupted
}
func validDef(d Definition) bool {
	return d.ID != "" && len(d.ID) <= 128 && d.Module != "" && len(d.Module) <= 128 && d.Queue >= 0 && d.Queue <= MaxQueue && d.Concurrency >= 1 && d.Concurrency <= MaxParallel && d.TTL > 0 && d.TTL <= timeline.MaxHorizon && (d.Mode == Single || d.Mode == Restart || d.Mode == Queued || d.Mode == Parallel)
}
func Open(store durable.Store) (*Manager, error) {
	raw, err := store.Load()
	d := Data{Version: 1, Definitions: map[string]Definition{}, Records: map[string]Record{}}
	fresh := errors.Is(err, os.ErrNotExist)
	if !fresh {
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &d) != nil || d.Version != 1 || d.Definitions == nil || d.Records == nil || len(d.Definitions) > MaxDefinitions || len(d.Records) > MaxRecords {
			return nil, durable.ErrCorrupt
		}
	}
	for id, def := range d.Definitions {
		if id != def.ID || !validDef(def) {
			return nil, durable.ErrCorrupt
		}
	}
	changed := fresh
	for id, r := range d.Records {
		if id != r.ID || r.Sequence == 0 || r.Sequence > d.Sequence || r.Occurrence.ID != id || (!terminal(r.Phase) && r.Phase != Pending && r.Phase != Running && r.Phase != Canceling) {
			return nil, durable.ErrCorrupt
		}
		if _, ok := d.Definitions[r.Definition]; !ok {
			return nil, durable.ErrCorrupt
		}
		if r.Phase == Running || r.Phase == Canceling {
			r.Phase = Interrupted
			d.Records[id] = r
			changed = true
		}
	}
	m := &Manager{store: store, data: d}
	if changed {
		if err = m.commit(d); err != nil {
			return nil, err
		}
	}
	return m, nil
}
func copyData(d Data) Data {
	raw, _ := json.Marshal(d)
	var c Data
	_ = json.Unmarshal(raw, &c)
	return c
}
func (m *Manager) commit(d Data) error {
	if m.poisoned {
		return durable.ErrUncertain
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err = m.store.Save(raw); err != nil {
		if errors.Is(err, durable.ErrUncertain) {
			m.poisoned = true
		}
		return err
	}
	m.data = d
	return nil
}
func (m *Manager) Snapshot() Data { m.mu.Lock(); defer m.mu.Unlock(); return copyData(m.data) }
func (m *Manager) Configure(def Definition) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !validDef(def) {
		return ErrLimit
	}
	d := copyData(m.data)
	if old, ok := d.Definitions[def.ID]; ok && old != def {
		for _, r := range d.Records {
			if r.Definition == def.ID && !terminal(r.Phase) {
				return ErrLimit
			}
		}
	} else if !ok && len(d.Definitions) >= MaxDefinitions {
		return ErrLimit
	}
	d.Definitions[def.ID] = def
	return m.commit(d)
}
func promote(d *Data, def Definition, now time.Time) {
	running := 0
	var pending []Record
	for id, r := range d.Records {
		if r.Definition != def.ID {
			continue
		}
		if r.Phase == Pending && !now.Before(r.Expires) {
			r.Phase = Expired
			d.Records[id] = r
		}
		if r.Phase == Running || r.Phase == Canceling {
			running++
		}
		if r.Phase == Pending && now.Before(r.Expires) {
			pending = append(pending, r)
		}
	}
	sortRecords(pending)
	limit := 1
	if def.Mode == Parallel {
		limit = def.Concurrency
	}
	for _, r := range pending {
		if running >= limit {
			break
		}
		r.Phase = Running
		d.Records[r.ID] = r
		running++
	}
}
func sortRecords(rows []Record) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && rows[j].Sequence < rows[j-1].Sequence; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

// Admit is the common entry point for external observations, cron and delayed
// Runtime occurrences. A duplicate ID returns the durable record unchanged.
func (m *Manager) Admit(defID string, o timeline.Occurrence, now time.Time) (Record, error) {
	return m.AdmitGeneration(defID, o, now, 0)
}
func (m *Manager) AdmitGeneration(defID string, o timeline.Occurrence, now time.Time, generation uint64) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.poisoned {
		return Record{}, durable.ErrUncertain
	}
	if existing, ok := m.data.Records[o.ID]; ok {
		return existing, nil
	}
	def, ok := m.data.Definitions[defID]
	if !ok || o.ID == "" || len(o.ID) > 128 || o.Logical.IsZero() || o.Logical.After(now) || len(m.data.Records) >= MaxRecords {
		return Record{}, ErrLimit
	}
	if !o.Logical.After(m.data.Floor) {
		return Record{}, ErrExpired
	}
	d := copyData(m.data)
	promote(&d, def, now)
	running, pending := 0, 0
	for _, r := range d.Records {
		if r.Definition == def.ID {
			if r.Phase == Running || r.Phase == Canceling {
				running++
			}
			if r.Phase == Pending {
				pending++
			}
		}
	}
	d.Sequence++
	r := Record{ID: o.ID, Definition: def.ID, Module: def.Module, Generation: generation, Occurrence: o, Phase: Pending, Sequence: d.Sequence, Expires: o.Logical.Add(def.TTL)}
	if !now.Before(r.Expires) {
		r.Phase = Expired
	} else {
		switch def.Mode {
		case Single:
			if running+pending > 0 {
				r.Phase = Dropped
			}
		case Queued, Parallel:
			if pending >= def.Queue && running >= func() int {
				if def.Mode == Parallel {
					return def.Concurrency
				}
				return 1
			}() {
				r.Phase = Dropped
			}
		case Restart:
			for id, old := range d.Records {
				if old.Definition != def.ID {
					continue
				}
				if old.Phase == Running {
					old.Phase = Canceling
				}
				if old.Phase == Pending {
					old.Phase = Canceled
				}
				d.Records[id] = old
			}
		}
	}
	d.Records[r.ID] = r
	promote(&d, def, now)
	r = d.Records[r.ID]
	if err := m.commit(d); err != nil {
		return Record{}, err
	}
	return r, nil
}
func (m *Manager) Finish(id string, canceled bool, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.data.Records[id]
	if !ok || terminal(r.Phase) {
		return ErrLimit
	}
	d := copyData(m.data)
	if canceled || r.Phase == Canceling || r.Phase == Pending {
		r.Phase = Canceled
	} else {
		r.Phase = Completed
	}
	d.Records[id] = r
	promote(&d, d.Definitions[r.Definition], now)
	return m.commit(d)
}

// Retire advances a durable floor: old IDs remain non-admissible after pruning.
func (m *Manager) Retire(floor time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if floor.Before(m.data.Floor) {
		return ErrLimit
	}
	d := copyData(m.data)
	for id, r := range d.Records {
		if !r.Occurrence.Logical.After(floor) {
			if !terminal(r.Phase) {
				return ErrLimit
			}
			delete(d.Records, id)
		}
	}
	d.Floor = floor
	return m.commit(d)
}

// ResumePending promotes durable queued work after recovery, preserving IDs.
// Running work interrupted by a Runtime failure is not automatically repeated.
func (m *Manager) ResumePending(now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := copyData(m.data)
	for _, def := range d.Definitions {
		promote(&d, def, now)
	}
	return m.commit(d)
}

// AdmitTimeline acknowledges only after durable admission. A crash between the
// two stores replays the same ID, which Admit returns without duplicate work.
func (m *Manager) AdmitTimeline(t *timeline.Timeline, definition func(string) string, now time.Time) error {
	d := t.Snapshot()
	rows := make([]timeline.Occurrence, 0, len(d.Pending))
	for _, o := range d.Pending {
		rows = append(rows, o)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Logical.Equal(rows[j].Logical) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].Logical.Before(rows[j].Logical)
	})
	for _, o := range rows {
		if _, err := m.Admit(definition(o.Schedule), o, now); err != nil {
			return err
		}
		if err := t.Ack(o.ID); err != nil {
			return err
		}
	}
	return nil
}
func (m *Manager) InFlight(module string, generation uint64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.data.Records {
		if r.Module == module && r.Generation == generation && !terminal(r.Phase) {
			n++
		}
	}
	return n
}
func (m *Manager) InterruptGeneration(module string, generation uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := copyData(m.data)
	for id, r := range d.Records {
		if r.Module == module && r.Generation == generation && !terminal(r.Phase) {
			r.Phase = Interrupted
			d.Records[id] = r
		}
	}
	return m.commit(d)
}
func (m *Manager) Owns(id, module string, generation uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.data.Records[id]
	return !m.poisoned && ok && r.Module == module && r.Generation == generation && (r.Phase == Running || r.Phase == Canceling)
}

// ClaimNext durably claims delivery once. A failure after claim is interrupted
// work on restart, never a blind duplicate execution.
func (m *Manager) ClaimNext(module string, generation uint64) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.poisoned {
		return Record{}, false, durable.ErrUncertain
	}
	var selected Record
	for _, r := range m.data.Records {
		if r.Module == module && r.Generation == generation && r.Phase == Running && !r.Dispatched && (selected.ID == "" || r.Sequence < selected.Sequence) {
			selected = r
		}
	}
	if selected.ID == "" {
		return Record{}, false, nil
	}
	d := copyData(m.data)
	selected.Dispatched = true
	d.Records[selected.ID] = selected
	if err := m.commit(d); err != nil {
		return Record{}, false, err
	}
	return selected, true, nil
}
func (m *Manager) PauseGeneration(module string, generation uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := copyData(m.data)
	for id, r := range d.Records {
		if r.Module == module && r.Generation == generation && (r.Phase == Running || r.Phase == Canceling) {
			r.Phase = Interrupted
			d.Records[id] = r
		}
	}
	return m.commit(d)
}
func (m *Manager) RebindPending(module string, old, new uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := copyData(m.data)
	for id, r := range d.Records {
		if r.Module == module && r.Generation == old && r.Phase == Pending {
			r.Generation = new
			d.Records[id] = r
		}
	}
	return m.commit(d)
}

func (m *Manager) Healthy() bool { m.mu.Lock(); defer m.mu.Unlock(); return !m.poisoned }
