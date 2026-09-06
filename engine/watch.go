package writ

import (
	"context"
	"time"

	"github.com/writtendev/writ/engine/projection"
)

// EventKind represents the nature of an event emitted by the store.
type EventKind string

const (
	// EventCreated indicates that the collaborative object was created in the refresh batch.
	EventCreated EventKind = "created"

	// EventChanged indicates that an existing collaborative object was updated in the refresh batch.
	EventChanged EventKind = "changed"

	// EventReset indicates that subscribers should discard cached state and re-query the projection.
	// This occurs on full rebuilds, chain rollbacks, or when a subscriber channel overflows.
	EventReset EventKind = "reset"
)

// Event represents a change notification for a collaborative object in the store.
type Event struct {
	// Kind is the type of event (created, changed, or reset).
	Kind EventKind `json:"kind"`

	// ObjectType is the type of collaborative object affected (e.g. "review", "issue", "comment", "project", "cycle").
	ObjectType string `json:"object_type,omitempty"`

	// ObjectID is the unique identifier of the collaborative object.
	ObjectID string `json:"object_id,omitempty"`

	// OpTypes lists the distinct operation types applied to this object in the batch.
	OpTypes []string `json:"op_types,omitempty"`

	// At is the timestamp when the event was emitted.
	At time.Time `json:"at"`
}

const watchBufferSize = 128

type subscriber struct {
	ch       chan Event
	overflow bool
}

func (sub *subscriber) emit(ev Event) {
	if sub.overflow {
		resetEv := Event{
			Kind: EventReset,
			At:   ev.At,
		}
		select {
		case sub.ch <- resetEv:
			sub.overflow = false
			if ev.Kind != EventReset {
				select {
				case sub.ch <- ev:
				default:
					sub.overflow = true
				}
			}
		default:
			// Channel is still full; keep overflow latched and drop event.
		}
		return
	}

	select {
	case sub.ch <- ev:
	default:
		sub.overflow = true
	}
}

// Watch returns a receive-only channel of Events describing changes to collaborative objects in the store.
// Subscribers receive domain-shaped change notifications on local writes and post-fetch refolds.
//
// Every event is published only after the underlying projection transaction commits, ensuring that
// state is immediately queryable upon receipt.
//
// Each subscriber receives events over a dedicated 128-element buffer. If a consumer falls behind
// and the buffer overflows, intermediate events are dropped and a single EventReset is delivered
// once capacity becomes available, signalling that the consumer should re-query.
//
// Cancelling ctx unsubscribes and closes the returned channel. Closing the Store closes all active
// subscriber channels.
func (s *Store) Watch(ctx context.Context) <-chan Event {
	if s == nil {
		ch := make(chan Event)
		close(ch)
		return ch
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	ch := make(chan Event, watchBufferSize)
	if s.closed {
		close(ch)
		return ch
	}

	sub := &subscriber{
		ch: ch,
	}
	s.subscribers = append(s.subscribers, sub)

	if ctx != nil && ctx.Done() != nil {
		go func() {
			<-ctx.Done()
			s.removeSubscriber(sub)
		}()
	}

	return ch
}

func (s *Store) removeSubscriber(sub *subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}

	for i, candidate := range s.subscribers {
		if candidate == sub {
			s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
			close(sub.ch)
			return
		}
	}
}

func (s *Store) emitLocked(stats projection.Stats) {
	if len(s.subscribers) == 0 {
		return
	}

	now := time.Now().UTC()

	if stats.Rebuilt {
		resetEv := Event{
			Kind: EventReset,
			At:   now,
		}
		for _, sub := range s.subscribers {
			sub.emit(resetEv)
		}
		return
	}

	if len(stats.Changed) == 0 {
		return
	}

	for _, c := range stats.Changed {
		kind := EventChanged
		if c.Created {
			kind = EventCreated
		}
		ev := Event{
			Kind:       kind,
			ObjectType: c.ObjectType,
			ObjectID:   c.ObjectID,
			OpTypes:    c.OpTypes,
			At:         now,
		}
		for _, sub := range s.subscribers {
			sub.emit(ev)
		}
	}
}
