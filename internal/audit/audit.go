// Package audit owns the bounded durable administrative journal. Household
// contents, credentials, free-form errors and reset confirmation are never stored.
package audit

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/housefold/runtime/internal/durable"
)

const MaxEntries = 256

var ErrUnavailable = errors.New("administrative audit unavailable")
var userID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var subject = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type Entry struct {
	Sequence  uint64    `json:"sequence"`
	At        time.Time `json:"at"`
	User      string    `json:"ha_user_id"`
	Operation string    `json:"operation"`
	Subject   string    `json:"subject,omitempty"`
	Outcome   string    `json:"outcome"`
}
type data struct {
	Version  int
	Sequence uint64
	Entries  []Entry
}
type Journal struct {
	mu       sync.Mutex
	store    durable.Store
	data     data
	poisoned bool
}

func validOperation(op string) bool {
	switch op {
	case "start", "stop", "restart", "remove", "recover", "rollback", "clear_volatile", "cleanup", "factory_reset", "refresh_catalog", "install":
		return true
	}
	return false
}
func validOutcome(out string) bool {
	switch out {
	case "requested", "completed", "failed", "interrupted":
		return true
	}
	return false
}
func Open(store durable.Store, create bool) (*Journal, error) {
	raw, err := store.Load()
	d := data{Version: 1}
	fresh := errors.Is(err, os.ErrNotExist)
	if fresh && !create {
		return nil, ErrUnavailable
	}
	if !fresh && (err != nil || len(raw) > 128<<10 || json.Unmarshal(raw, &d) != nil || d.Version != 1 || len(d.Entries) > MaxEntries) {
		return nil, ErrUnavailable
	}
	last := uint64(0)
	dirty := fresh
	for i, e := range d.Entries {
		if e.Sequence == 0 || e.Sequence <= last || e.Sequence > d.Sequence || e.At.IsZero() || !userID.MatchString(e.User) || !validOperation(e.Operation) || !validOutcome(e.Outcome) || (e.Subject != "" && !subject.MatchString(e.Subject)) {
			return nil, ErrUnavailable
		}
		last = e.Sequence
		if e.Outcome == "requested" {
			d.Entries[i].Outcome = "interrupted"
			dirty = true
		}
	}
	if last != d.Sequence {
		return nil, ErrUnavailable
	}
	j := &Journal{store: store, data: d}
	if dirty {
		if err = j.save(d); err != nil {
			return nil, err
		}
	}
	return j, nil
}
func (j *Journal) save(d data) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return ErrUnavailable
	}
	if err = j.store.Save(raw); err != nil {
		if errors.Is(err, durable.ErrUncertain) || errors.Is(err, durable.ErrCorrupt) {
			j.poisoned = true
		}
		return err
	}
	j.data = d
	return nil
}
func (j *Journal) Begin(user, op, id string, now time.Time) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.poisoned || !userID.MatchString(user) || !validOperation(op) || (id != "" && !subject.MatchString(id)) || now.IsZero() || j.data.Sequence == math.MaxUint64 {
		return 0, ErrUnavailable
	}
	d := j.data
	d.Sequence++
	d.Entries = append(append([]Entry(nil), d.Entries...), Entry{d.Sequence, now.UTC(), user, op, id, "requested"})
	if len(d.Entries) > MaxEntries {
		d.Entries = append([]Entry(nil), d.Entries[len(d.Entries)-MaxEntries:]...)
	}
	if err := j.save(d); err != nil {
		return 0, err
	}
	return d.Sequence, nil
}
func (j *Journal) Finish(sequence uint64, outcome string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.poisoned || (outcome != "completed" && outcome != "failed") {
		return ErrUnavailable
	}
	d := j.data
	d.Entries = append([]Entry(nil), d.Entries...)
	for i, e := range d.Entries {
		if e.Sequence == sequence && e.Outcome == "requested" {
			d.Entries[i].Outcome = outcome
			return j.save(d)
		}
	}
	return ErrUnavailable
}
func (j *Journal) Entries() []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Entry(nil), j.data.Entries...)
}
