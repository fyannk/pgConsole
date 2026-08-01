// Copyright 2026 The pgConsole Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package observe

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Event bounds. The rendered list and the retained state are both
// bounded so a busy namespace can never grow memory or the page
// without a visible truncation.
const (
	// MaxEvents bounds the rendered event list.
	MaxEvents = 100
	// MaxEventsRetained bounds the collector's internal state; the
	// oldest event is evicted when a new one arrives at the bound.
	MaxEventsRetained = 300
)

// EventFacts is one Kubernetes-observed event prepared for the console.
type EventFacts struct {
	// Name is the event object's name, the deduplication key.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// Kind is the involved object's kind, such as "Cluster" or "Pod".
	Kind string
	// Object is the involved object's name.
	Object string
	// Type is the event type, such as "Normal" or "Warning".
	Type string
	// Reason is the machine reason.
	Reason string
	// Message is the bounded human message.
	Message string
	// Count is the delivery count of the event.
	Count int
	// LastSeen is the most recent occurrence known to the API server.
	LastSeen time.Time
}

// EventDeletion identifies a removed event incarnation.
type EventDeletion struct {
	// Name is the deleted event's name.
	Name string
	// UID is the deleted incarnation.
	UID string
}

// EventChange is one incremental change delivered by an event watch.
// Exactly one field is set.
type EventChange struct {
	// Put upserts one event by name.
	Put *EventFacts
	// Delete removes one event incarnation.
	Delete *EventDeletion
}

// EventWatch is a running watch on the namespace's candidate events.
// Changes is closed when the watch ends; Stop releases it.
type EventWatch interface {
	// Changes streams incremental changes until the watch ends.
	Changes() <-chan EventChange
	// Stop releases the watch.
	Stop()
}

// EventSource produces candidate events. The concrete implementation
// filters to the cluster's candidate objects at the boundary; the
// final pod-membership decision belongs to rendering.
type EventSource interface {
	// FetchEvents returns the current candidate set and the resource
	// version to resume watching from.
	FetchEvents(ctx context.Context) (events []EventFacts, resourceVersion string, err error)
	// WatchEvents streams changes from the given resource version.
	WatchEvents(ctx context.Context, fromResourceVersion string) (EventWatch, error)
}

// EventsSnapshot is the rendered event list: newest first, age-pruned,
// bounded, with its own staleness.
type EventsSnapshot struct {
	// Generation increases by one on every publication.
	Generation uint64
	// ObservedAt is the time of the last successful contact.
	ObservedAt time.Time
	// Stale reports lost contact.
	Stale bool
	// Truncated reports that more candidates existed than the bound.
	Truncated bool
	// Events is sorted newest first, then by name.
	Events []EventFacts
}

// EventStore holds the current events snapshot for concurrent readers.
type EventStore struct {
	mu   sync.RWMutex
	snap EventsSnapshot
	has  bool
}

// NewEventStore returns an empty store.
func NewEventStore() *EventStore {
	return &EventStore{}
}

// CurrentEvents returns the snapshot and whether one exists.
func (s *EventStore) CurrentEvents() (EventsSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.has
}

// publish replaces the snapshot: events older than the window are
// dropped, the rest sorted newest first and bounded, with truncation
// flagged.
func (s *EventStore) publish(events []EventFacts, observedAt time.Time, window time.Duration) {
	cutoff := observedAt.Add(-window)
	kept := make([]EventFacts, 0, len(events))
	for _, e := range events {
		if e.LastSeen.Before(cutoff) {
			continue
		}
		kept = append(kept, e)
	}
	kept, truncated := bounded(kept, lessEventRecency, MaxEvents)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = EventsSnapshot{
		Generation: s.snap.Generation + 1,
		ObservedAt: observedAt,
		Truncated:  truncated,
		Events:     kept,
	}
	s.has = true
}

// markStale marks the retained snapshot stale, if one exists.
func (s *EventStore) markStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has {
		return
	}
	s.snap.Stale = true
}

// lessEventRecency orders the rendered event list newest first, with
// the name breaking ties so the order is total and a redraw never
// reshuffles two events sharing a timestamp.
func lessEventRecency(a, b EventFacts) bool {
	if !a.LastSeen.Equal(b.LastSeen) {
		return a.LastSeen.After(b.LastSeen)
	}
	return a.Name < b.Name
}

// eventRetention identifies retained events and bounds them in memory.
// The oldest entry loses: an event's value to an operator decays with
// age, so under a flood the recent ones are the ones worth keeping.
// Eviction here is deliberately silent — it sets no truncation flag,
// because the bound an operator is shown is the rendered one applied at
// publication, and reporting this one too would claim two different
// truncations for one screen.
var eventRetention = retention[EventFacts]{
	Name:      func(e EventFacts) string { return e.Name },
	UID:       func(e EventFacts) string { return e.UID },
	Limit:     MaxEventsRetained,
	Evictable: func(a, b EventFacts) bool { return a.LastSeen.Before(b.LastSeen) },
}

// EventCollector maintains the event store from an EventSource with the
// same contract as the other collectors: seed, follow, per-change
// republication, stale retention, backoff. That contract is the shared
// loop in loop.go; what follows is only the event-specific part of it.
type EventCollector struct {
	source EventSource
	store  *EventStore
	clock  Clock
	logger *slog.Logger
	window time.Duration
	state  keyed[EventFacts]
}

// NewEventCollector wires an event collector onto a store. window is
// the configured event age window.
func NewEventCollector(source EventSource, store *EventStore, window time.Duration, clock Clock, logger *slog.Logger) *EventCollector {
	return &EventCollector{source: source, store: store, clock: clock, logger: logger, window: window}
}

// Run blocks until ctx is done, maintaining the store. Cancellation is
// the one clean exit.
func (c *EventCollector) Run(ctx context.Context) error {
	return newLoop[string, EventChange](c, c.clock, c.logger).Run(ctx)
}

// op names this resource in contact-loss logs.
func (c *EventCollector) op() string { return "events" }

// seed replaces the retained event set and returns the resource version
// the watch resumes from.
func (c *EventCollector) seed(ctx context.Context) (string, error) {
	events, rv, err := c.source.FetchEvents(ctx)
	if err != nil {
		return "", err
	}
	c.state = make(keyed[EventFacts], len(events))
	for _, e := range events {
		c.state.put(e, eventRetention)
	}
	return rv, nil
}

// follow starts the event watch from the seed's resource version.
func (c *EventCollector) follow(ctx context.Context, from string) (<-chan EventChange, func(), error) {
	w, err := c.source.WatchEvents(ctx, from)
	if err != nil {
		return nil, nil, err
	}
	return w.Changes(), w.Stop, nil
}

// apply folds one change into the retained set. Duplicate names replace
// the previous entry — an event's count updates arrive this way. It
// reports whether the change was recognized; a change carrying nothing
// is not.
func (c *EventCollector) apply(ev EventChange) bool {
	switch {
	case ev.Put != nil:
		c.state.put(*ev.Put, eventRetention)
	case ev.Delete != nil:
		c.state.remove(ev.Delete.Name, ev.Delete.UID, eventRetention)
	default:
		return false
	}
	return true
}

// publish snapshots the retained set into the store. The age window is
// applied there, at publication, so a retained event that has aged out
// stops being rendered without being forgotten.
func (c *EventCollector) publish(observedAt time.Time) {
	c.store.publish(c.state.list(), observedAt, c.window)
}

// markStale marks the retained snapshot stale, if one exists.
func (c *EventCollector) markStale() { c.store.markStale() }
