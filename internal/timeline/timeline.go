// Package timeline owns deterministic schedule occurrences, never HA history.
package timeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/housefold/runtime/internal/durable"
	"os"
	"sort"
	"sync"
	"time"
)

const MaxSchedules = 128
const MaxPending = 4096
const MaxHorizon = 7 * 24 * time.Hour
const DefaultHorizon = time.Hour

var ErrLimit = errors.New("timeline bound or invalid schedule")

type Schedule struct {
	ID      string        `json:"id"`
	Start   time.Time     `json:"start"`
	Every   time.Duration `json:"every"`
	Horizon time.Duration `json:"horizon"`
}
type Occurrence struct {
	ID        string    `json:"id"`
	Schedule  string    `json:"schedule"`
	Logical   time.Time `json:"logical"`
	Recovered bool      `json:"recovered"`
}
type Data struct {
	Version   int                   `json:"version"`
	Watermark time.Time             `json:"watermark"`
	Horizon   time.Duration         `json:"horizon"`
	Schedules map[string]Schedule   `json:"schedules"`
	Pending   map[string]Occurrence `json:"pending"`
}
type Timeline struct {
	mu    sync.Mutex
	store durable.Store
	data  Data
}

func Open(store durable.Store, initial time.Time) (*Timeline, error) {
	raw, err := store.Load()
	var data Data
	if errors.Is(err, os.ErrNotExist) {
		data = Data{Version: 1, Watermark: initial.UTC(), Horizon: DefaultHorizon, Schedules: map[string]Schedule{}, Pending: map[string]Occurrence{}}
		raw, _ = json.Marshal(data)
		if err = store.Save(raw); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if json.Unmarshal(raw, &data) != nil {
		return nil, durable.ErrCorrupt
	}
	if !valid(data) {
		return nil, durable.ErrCorrupt
	}
	return &Timeline{store: store, data: data}, nil
}
func valid(d Data) bool {
	if d.Version != 1 || d.Watermark.IsZero() || d.Horizon <= 0 || d.Horizon > MaxHorizon || d.Schedules == nil || d.Pending == nil || len(d.Schedules) > MaxSchedules || len(d.Pending) > MaxPending {
		return false
	}
	for id, s := range d.Schedules {
		if id != s.ID || !validSchedule(s) {
			return false
		}
	}
	for id, o := range d.Pending {
		if id != o.ID || o.Schedule == "" || o.Logical.IsZero() || o.ID != occurrenceID(o.Schedule, o.Logical) {
			return false
		}
	}
	return true
}
func validSchedule(s Schedule) bool {
	return len(s.ID) > 0 && len(s.ID) <= 128 && !s.Start.IsZero() && s.Every >= 0 && s.Horizon >= 0 && s.Horizon <= MaxHorizon && (s.Every == 0 || s.Every >= time.Second)
}
func clone(d Data) Data {
	raw, _ := json.Marshal(d)
	var copy Data
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func (t *Timeline) Snapshot() Data { t.mu.Lock(); defer t.mu.Unlock(); return clone(t.data) }
func (t *Timeline) commit(d Data) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err = t.store.Save(raw); err != nil {
		return err
	}
	t.data = d
	return nil
}
func (t *Timeline) Put(s Schedule) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !validSchedule(s) {
		return ErrLimit
	}
	d := clone(t.data)
	if _, ok := d.Schedules[s.ID]; !ok && len(d.Schedules) >= MaxSchedules {
		return ErrLimit
	}
	s.Start = s.Start.UTC()
	d.Schedules[s.ID] = s
	return t.commit(d)
}
func (t *Timeline) SetHorizon(h time.Duration) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if h <= 0 || h > MaxHorizon {
		return ErrLimit
	}
	d := clone(t.data)
	d.Horizon = h
	return t.commit(d)
}
func occurrenceID(id string, at time.Time) string {
	sum := sha256.Sum256([]byte(id + "\x00" + at.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}
func (t *Timeline) Advance(now time.Time, recovered bool) ([]Occurrence, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now = now.UTC()
	if now.Before(t.data.Watermark) {
		return nil, ErrLimit
	}
	d := clone(t.data)
	var out []Occurrence
	for _, s := range d.Schedules {
		h := d.Horizon
		if s.Horizon > 0 {
			h = s.Horizon
		}
		from := d.Watermark
		if recovered && now.Add(-h).After(from) {
			from = now.Add(-h)
		}
		at := s.Start
		if !at.After(from) {
			if s.Every == 0 {
				continue
			}
			steps := from.Sub(at)/s.Every + 1
			at = at.Add(steps * s.Every)
		}
		for !at.After(now) {
			if len(d.Pending) >= MaxPending {
				return nil, ErrLimit
			}
			o := Occurrence{Schedule: s.ID, Logical: at, Recovered: recovered}
			o.ID = occurrenceID(s.ID, at)
			if _, ok := d.Pending[o.ID]; !ok {
				d.Pending[o.ID] = o
				out = append(out, o)
			}
			if s.Every == 0 {
				break
			}
			at = at.Add(s.Every)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Logical.Equal(out[j].Logical) {
			return out[i].ID < out[j].ID
		}
		return out[i].Logical.Before(out[j].Logical)
	})
	d.Watermark = now
	if err := t.commit(d); err != nil {
		return nil, err
	}
	return out, nil
}
func (t *Timeline) Ack(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	d := clone(t.data)
	delete(d.Pending, id)
	return t.commit(d)
}
