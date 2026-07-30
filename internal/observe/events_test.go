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
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/fyannk/pgconsole/internal/redact"
)

// eventStep is one scripted event-source interaction.
type eventStep struct {
	fetchEvents []EventFacts
	fetchRV     string
	fetchErr    error
	watchErr    error
	// watchChanges are streamed, then the watch closes.
	watchChanges []EventChange
}

// scriptedEventSource replays steps; exhaustion cancels the collector.
type scriptedEventSource struct {
	mu     sync.Mutex
	steps  []eventStep
	i      int
	cancel context.CancelFunc
}

func (s *scriptedEventSource) current() (eventStep, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.steps) {
		return eventStep{}, false
	}
	return s.steps[s.i], true
}

func (s *scriptedEventSource) advance() {
	s.mu.Lock()
	s.i++
	done := s.i >= len(s.steps)
	s.mu.Unlock()
	if done {
		s.cancel()
	}
}

// FetchEvents replays the scripted fetch outcome.
func (s *scriptedEventSource) FetchEvents(_ context.Context) ([]EventFacts, string, error) {
	st, ok := s.current()
	if !ok {
		return nil, "", context.Canceled
	}
	if st.fetchErr != nil {
		s.advance()
		return nil, "", st.fetchErr
	}
	return st.fetchEvents, st.fetchRV, nil
}

// WatchEvents replays the scripted watch outcome.
func (s *scriptedEventSource) WatchEvents(_ context.Context, _ string) (EventWatch, error) {
	st, ok := s.current()
	if !ok {
		return nil, context.Canceled
	}
	s.advance()
	if st.watchErr != nil {
		return nil, st.watchErr
	}
	ch := make(chan EventChange, len(st.watchChanges))
	for _, ev := range st.watchChanges {
		ch <- ev
	}
	close(ch)
	return &chanEventWatch{ch: ch}, nil
}

// chanEventWatch adapts a closed channel to the EventWatch interface.
type chanEventWatch struct{ ch chan EventChange }

func (w *chanEventWatch) Changes() <-chan EventChange { return w.ch }
func (w *chanEventWatch) Stop()                       {}

// runEvents executes the event collector over the script until it
// exhausts, with a one-hour window.
func runEvents(t *testing.T, steps []eventStep) *EventStore {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &scriptedEventSource{steps: steps, cancel: cancel}
	store := NewEventStore()
	c := NewEventCollector(src, store, time.Hour, newFakeClock(), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err := c.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return store
}

// eventAt builds an event whose LastSeen sits the given duration before
// the fake clock's epoch region.
func eventAt(name string, age time.Duration) EventFacts {
	base := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	return EventFacts{
		Name: name, UID: "u-" + name, Kind: "Cluster", Object: "orders",
		Type: "Normal", Reason: "r", Count: 1,
		LastSeen: base.Add(-age),
	}
}

func TestEventCollectorPublishesAndSortsNewestFirst(t *testing.T) {
	t.Parallel()
	store := runEvents(t, []eventStep{{
		fetchEvents: []EventFacts{eventAt("older", 30*time.Minute), eventAt("newer", time.Minute)},
		fetchRV:     "10",
		watchChanges: []EventChange{
			{Put: ptrEvent(eventAt("newest", 10*time.Second))},
		},
	}})
	snap, ok := store.CurrentEvents()
	if !ok {
		t.Fatal("no snapshot published")
	}
	if len(snap.Events) != 3 {
		t.Fatalf("events = %d, want 3", len(snap.Events))
	}
	if snap.Events[0].Name != "newest" || snap.Events[2].Name != "older" {
		t.Errorf("events not newest-first: %+v", snap.Events)
	}
}

func ptrEvent(e EventFacts) *EventFacts { return &e }

// TestEventCollectorWindowPrunesAtPublish proves events beyond the
// window never enter a snapshot.
func TestEventCollectorWindowPrunesAtPublish(t *testing.T) {
	t.Parallel()
	store := runEvents(t, []eventStep{{
		fetchEvents: []EventFacts{
			eventAt("fresh", 30*time.Minute),
			eventAt("expired", 3*time.Hour),
		},
		fetchRV: "10",
	}})
	snap, _ := store.CurrentEvents()
	if len(snap.Events) != 1 || snap.Events[0].Name != "fresh" {
		t.Fatalf("window not applied at publish: %+v", snap.Events)
	}
}

// TestEventCollectorDuplicateNamesReplace proves a repeated event name
// replaces the prior entry — count updates arrive this way — and never
// duplicates rows.
func TestEventCollectorDuplicateNamesReplace(t *testing.T) {
	t.Parallel()
	first := eventAt("e1", time.Minute)
	second := eventAt("e1", 30*time.Second)
	second.Count = 5
	store := runEvents(t, []eventStep{{
		fetchEvents:  []EventFacts{first},
		fetchRV:      "10",
		watchChanges: []EventChange{{Put: &second}},
	}})
	snap, _ := store.CurrentEvents()
	if len(snap.Events) != 1 {
		t.Fatalf("duplicate name produced %d rows", len(snap.Events))
	}
	if snap.Events[0].Count != 5 {
		t.Errorf("count update lost: %+v", snap.Events[0])
	}
}

func TestEventCollectorDeletionChecksUID(t *testing.T) {
	t.Parallel()
	replaced := eventAt("e1", time.Minute)
	replaced.UID = "new"
	store := runEvents(t, []eventStep{{
		fetchEvents: []EventFacts{eventAt("e1", 2*time.Minute)},
		fetchRV:     "10",
		watchChanges: []EventChange{
			{Put: &replaced},
			{Delete: &EventDeletion{Name: "e1", UID: "u-e1"}},
		},
	}})
	snap, _ := store.CurrentEvents()
	if len(snap.Events) != 1 || snap.Events[0].UID != "new" {
		t.Fatalf("stale deletion removed the newer incarnation: %+v", snap.Events)
	}
}

func TestEventCollectorWatchBreakRetainsStale(t *testing.T) {
	t.Parallel()
	store := runEvents(t, []eventStep{
		{fetchEvents: []EventFacts{eventAt("e1", time.Minute)}, fetchRV: "10"},
		{fetchErr: redact.NewError("events list", redact.CategoryForbidden, nil)},
	})
	snap, ok := store.CurrentEvents()
	if !ok {
		t.Fatal("last-good snapshot dropped")
	}
	if !snap.Stale {
		t.Error("snapshot not stale after watch break and failed refetch")
	}
}

// TestEventCollectorBoundsRetentionAndRendering proves both bounds: the
// retained state evicts the oldest at MaxEventsRetained, and the
// snapshot truncates visibly at MaxEvents.
func TestEventCollectorBoundsRetentionAndRendering(t *testing.T) {
	t.Parallel()
	flood := make([]EventFacts, MaxEventsRetained)
	for i := range flood {
		flood[i] = eventAt(fmt.Sprintf("e%04d", i), time.Duration(i+1)*time.Second)
	}
	newest := eventAt("overflow", 0)
	store := runEvents(t, []eventStep{{
		fetchEvents:  flood,
		fetchRV:      "10",
		watchChanges: []EventChange{{Put: &newest}},
	}})
	snap, _ := store.CurrentEvents()
	if len(snap.Events) != MaxEvents {
		t.Fatalf("rendered events = %d, want bounded at %d", len(snap.Events), MaxEvents)
	}
	if !snap.Truncated {
		t.Fatal("truncation not flagged")
	}
	if snap.Events[0].Name != "overflow" {
		t.Errorf("newest event lost by retention eviction: %q", snap.Events[0].Name)
	}
}
