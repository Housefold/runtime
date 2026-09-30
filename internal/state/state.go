// Package state defines private, transport-independent Runtime consumer values.
// These types are not a public module or network contract.
package state

import (
	"errors"
	"time"
)

type Entity struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged time.Time      `json:"last_changed"`
	LastUpdated time.Time      `json:"last_updated"`
}

type Position struct {
	Generation uint64 `json:"generation"`
	Revision   uint64 `json:"revision"`
}

type Snapshot struct {
	Generation uint64            `json:"generation"`
	Revision   uint64            `json:"revision"`
	Fresh      bool              `json:"fresh"`
	States     map[string]Entity `json:"states"`
}

// Read distinguishes an unknown initial cache from an absent entity in a
// published (possibly stale) generation. Found never implies freshness.
type Read struct {
	Position
	Fresh  bool
	Known  bool
	Found  bool
	Entity Entity
}

type Kind string

const (
	Reset     Kind = "reset"
	Add       Kind = "add"
	Update    Kind = "update"
	Remove    Kind = "remove"
	Freshness Kind = "freshness"
)

type Event struct {
	Position
	Kind     Kind      `json:"kind"`
	Fresh    bool      `json:"fresh"`
	EntityID string    `json:"entity_id,omitempty"`
	Entity   *Entity   `json:"entity,omitempty"`
	Snapshot *Snapshot `json:"snapshot,omitempty"`
}

var (
	ErrOverflow        = errors.New("state subscription overflow")
	ErrCanceled        = errors.New("state subscription canceled")
	ErrShutdown        = errors.New("state source shut down")
	ErrInitialTooLarge = errors.New("initial state exceeds subscription payload bound")
	ErrCapacity        = errors.New("state subscription capacity exhausted")
)
