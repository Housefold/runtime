package ha

import (
	"context"
	"encoding/json"

	"github.com/housefold/runtime/internal/state"
)

const (
	maxSubscriptionEvents     = 256
	maxSubscriptionBytes      = 8 * 1024 * 1024
	maxSubscribers            = 16
	maxTotalSubscriptionBytes = 128 * 1024 * 1024
)

type queuedStateEvent struct {
	event state.Event
	bytes int
}

// Subscription owns an independent bounded queue. Next transfers ownership of
// each event to the caller. Terminal outcomes discard pending events and remain
// observable on every subsequent Next. All fields are guarded by owner.mu.
type Subscription struct {
	owner    *StateSession
	queue    []queuedStateEvent
	bytes    int
	terminal error
	notify   chan struct{}
	done     chan struct{}
	stop     func() bool
}

func (s *StateSession) positionLocked() state.Position {
	return state.Position{Generation: s.metadata.Generation, Revision: s.metadata.Revision}
}

func (s *StateSession) Read(entityID string) state.Read {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entity, found := s.states[entityID]
	return state.Read{Position: s.positionLocked(), Fresh: s.metadata.Fresh,
		Known: s.metadata.Generation > 0, Found: found, Entity: cloneEntityState(entity)}
}

// Subscribe registers atomically with a complete initial reset. The initial
// view is queued and counted just like later resets; a large view is rejected.
func (s *StateSession) Subscribe(ctx context.Context) (*Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, state.ErrShutdown
	}
	if ctx.Err() != nil {
		return nil, state.ErrCanceled
	}
	if len(s.subscribers) >= maxSubscribers {
		return nil, state.ErrCapacity
	}
	initial := state.Event{Position: s.positionLocked(), Kind: state.Reset, Fresh: s.metadata.Fresh,
		Snapshot: &StateSnapshot{Generation: s.metadata.Generation, Revision: s.metadata.Revision, Fresh: s.metadata.Fresh, States: s.states}}
	size := eventSize(initial)
	if size > maxSubscriptionBytes {
		return nil, state.ErrInitialTooLarge
	}
	if s.subscriberBytes+size > maxTotalSubscriptionBytes {
		return nil, state.ErrCapacity
	}
	sub := &Subscription{owner: s, notify: make(chan struct{}, 1), done: make(chan struct{})}
	if s.subscribers == nil {
		s.subscribers = make(map[*Subscription]struct{})
	}
	s.subscribers[sub] = struct{}{}
	sub.enqueueLocked(initial, size)
	sub.stop = context.AfterFunc(ctx, func() { sub.Close() })
	return sub, nil
}

// Next's context cancels only this wait, without destroying the subscription.
// The Subscribe context or Close ends the subscription with ErrCanceled.
func (sub *Subscription) Next(ctx context.Context) (state.Event, error) {
	for {
		s := sub.owner
		s.mu.Lock()
		if sub.terminal != nil {
			err := sub.terminal
			s.mu.Unlock()
			return state.Event{}, err
		}
		if ctx.Err() != nil {
			s.mu.Unlock()
			return state.Event{}, ctx.Err()
		}
		if len(sub.queue) > 0 {
			item := sub.queue[0]
			sub.queue[0] = queuedStateEvent{}
			sub.queue = sub.queue[1:]
			sub.bytes -= item.bytes
			s.subscriberBytes -= item.bytes
			s.mu.Unlock()
			return item.event, nil
		}
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return state.Event{}, ctx.Err()
		case <-sub.done:
		case <-sub.notify:
		}
	}
}

func (sub *Subscription) Done() <-chan struct{} { return sub.done }
func (sub *Subscription) Close() {
	sub.owner.mu.Lock()
	defer sub.owner.mu.Unlock()
	sub.finishLocked(state.ErrCanceled)
}

func (sub *Subscription) finishLocked(err error) {
	if sub.terminal != nil {
		return
	}
	sub.terminal = err
	sub.owner.subscriberBytes -= sub.bytes
	sub.bytes = 0
	sub.queue = nil
	delete(sub.owner.subscribers, sub)
	if sub.stop != nil {
		sub.stop()
	}
	close(sub.done)
}

func eventSize(event state.Event) int {
	raw, err := json.Marshal(event)
	if err != nil {
		return maxSubscriptionBytes + 1
	}
	return len(raw)
}

func cloneStateEvent(event state.Event) state.Event {
	if event.Entity != nil {
		entity := cloneEntityState(*event.Entity)
		event.Entity = &entity
	}
	if event.Snapshot != nil {
		snapshot := *event.Snapshot
		snapshot.States = cloneStateMap(snapshot.States)
		event.Snapshot = &snapshot
	}
	return event
}

func (sub *Subscription) enqueueLocked(event state.Event, size int) {
	sub.queue = append(sub.queue, queuedStateEvent{event: cloneStateEvent(event), bytes: size})
	sub.bytes += size
	sub.owner.subscriberBytes += size
	select {
	case sub.notify <- struct{}{}:
	default:
	}
}

func (s *StateSession) broadcastLocked(event state.Event) {
	if len(s.subscribers) == 0 {
		return
	}
	size := eventSize(event)
	// Each admitted subscriber reserves 8 MiB; four subscribers bound total
	// queued canonical payload to 32 MiB without evicting unrelated consumers.
	for sub := range s.subscribers {
		if len(sub.queue) >= maxSubscriptionEvents || sub.bytes+size > maxSubscriptionBytes {
			sub.finishLocked(state.ErrOverflow)
		}
	}
	for sub := range s.subscribers {
		sub.enqueueLocked(event, size)
	}
}

func (s *StateSession) staleLocked() {
	if !s.metadata.Fresh {
		return
	}
	s.metadata.Fresh = false
	s.broadcastLocked(state.Event{Position: s.positionLocked(), Kind: state.Freshness, Fresh: false})
}

func (s *StateSession) shutdownSubscriptions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.staleLocked()
	for sub := range s.subscribers {
		sub.finishLocked(state.ErrShutdown)
	}
}
