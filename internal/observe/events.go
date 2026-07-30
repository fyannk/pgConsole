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
	"sort"
	"sync"
	"time"

	"github.com/fyannk/pgConsole/internal/redact"
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
	sort.Slice(kept, func(i, j int) bool {
		if !kept[i].LastSeen.Equal(kept[j].LastSeen) {
			return kept[i].LastSeen.After(kept[j].LastSeen)
		}
		return kept[i].Name < kept[j].Name
	})
	truncated := false
	if len(kept) > MaxEvents {
		kept = kept[:MaxEvents]
		truncated = true
	}
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

// EventCollector maintains the event store from an EventSource with the
// same contract as the other collectors: seed, follow, per-change
// republication, stale retention, backoff.
type EventCollector struct {
	source  EventSource
	store   *EventStore
	clock   Clock
	logger  *slog.Logger
	window  time.Duration
	backoff time.Duration
	state   map[string]EventFacts
}

// NewEventCollector wires an event collector onto a store. window is
// the configured event age window.
func NewEventCollector(source EventSource, store *EventStore, window time.Duration, clock Clock, logger *slog.Logger) *EventCollector {
	return &EventCollector{
		source:  source,
		store:   store,
		clock:   clock,
		logger:  logger,
		window:  window,
		backoff: backoffInitial,
	}
}

// Run blocks until ctx is done, maintaining the store. Cancellation is
// the one clean exit.
func (c *EventCollector) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		events, rv, err := c.source.FetchEvents(ctx)
		if err != nil {
			c.loseContact("events fetch", err)
			if c.wait(ctx) != nil {
				return nil
			}
			continue
		}
		c.state = make(map[string]EventFacts, len(events))
		for _, e := range events {
			c.retain(e)
		}
		c.publish()

		w, err := c.source.WatchEvents(ctx, rv)
		if err != nil {
			c.loseContact("events watch start", err)
			if c.wait(ctx) != nil {
				return nil
			}
			continue
		}
		c.follow(ctx, w)
		if ctx.Err() != nil {
			return nil
		}
		c.loseContact("events watch", nil)
		if c.wait(ctx) != nil {
			return nil
		}
	}
}

// follow consumes watch changes until the watch ends or ctx is done,
// draining delivered changes before honoring cancellation.
func (c *EventCollector) follow(ctx context.Context, w EventWatch) {
	defer w.Stop()
	for {
		select {
		case ev, ok := <-w.Changes():
			if !ok {
				return
			}
			c.apply(ev)
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Changes():
			if !ok {
				return
			}
			c.apply(ev)
		}
	}
}

// apply folds one change into the state and republishes. Duplicate
// names replace the previous entry — an event's count updates arrive
// this way. A deletion whose UID no longer matches is ignored.
func (c *EventCollector) apply(ev EventChange) {
	switch {
	case ev.Put != nil:
		c.retain(*ev.Put)
	case ev.Delete != nil:
		if current, ok := c.state[ev.Delete.Name]; ok && current.UID == ev.Delete.UID {
			delete(c.state, ev.Delete.Name)
		}
	default:
		return
	}
	c.publish()
}

// retain upserts one event, evicting the oldest entry when the retained
// bound is reached by a new name.
func (c *EventCollector) retain(e EventFacts) {
	if _, exists := c.state[e.Name]; !exists && len(c.state) >= MaxEventsRetained {
		oldestName := ""
		var oldest time.Time
		for name, cur := range c.state {
			if oldestName == "" || cur.LastSeen.Before(oldest) {
				oldestName, oldest = name, cur.LastSeen
			}
		}
		delete(c.state, oldestName)
	}
	c.state[e.Name] = e
}

// publish snapshots the current state and resets the backoff.
func (c *EventCollector) publish() {
	events := make([]EventFacts, 0, len(c.state))
	for _, e := range c.state {
		events = append(events, e)
	}
	c.store.publish(events, c.clock.Now(), c.window)
	c.backoff = backoffInitial
}

// loseContact marks the snapshot stale and logs the failure category.
func (c *EventCollector) loseContact(op string, err error) {
	c.store.markStale()
	attrs := []any{slog.String("op", op)}
	if err != nil {
		attrs = append(attrs, slog.String("category", redact.Safe(err)))
	}
	c.logger.Info("contact lost", attrs...)
}

// wait sleeps the current backoff and doubles it up to the bound.
func (c *EventCollector) wait(ctx context.Context) error {
	d := c.backoff
	c.backoff *= 2
	if c.backoff > backoffMax {
		c.backoff = backoffMax
	}
	return c.clock.Wait(ctx, d)
}
