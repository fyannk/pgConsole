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

	"github.com/fyannk/pgConsole/internal/redact"
)

// podStep is one scripted pod-source interaction.
type podStep struct {
	fetchPods []PodFacts
	fetchRV   string
	fetchErr  error
	watchErr  error
	// watchEvents are streamed, then the watch closes.
	watchEvents []PodEvent
}

// scriptedPodSource replays steps; exhaustion cancels the collector.
type scriptedPodSource struct {
	mu     sync.Mutex
	steps  []podStep
	i      int
	cancel context.CancelFunc
}

func (s *scriptedPodSource) current() (podStep, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.steps) {
		return podStep{}, false
	}
	return s.steps[s.i], true
}

func (s *scriptedPodSource) advance() {
	s.mu.Lock()
	s.i++
	done := s.i >= len(s.steps)
	s.mu.Unlock()
	if done {
		s.cancel()
	}
}

// FetchPods replays the scripted fetch outcome.
func (s *scriptedPodSource) FetchPods(_ context.Context) ([]PodFacts, string, error) {
	st, ok := s.current()
	if !ok {
		return nil, "", context.Canceled
	}
	if st.fetchErr != nil {
		s.advance()
		return nil, "", st.fetchErr
	}
	return st.fetchPods, st.fetchRV, nil
}

// WatchPods replays the scripted watch outcome.
func (s *scriptedPodSource) WatchPods(_ context.Context, _ string) (PodWatch, error) {
	st, ok := s.current()
	if !ok {
		return nil, context.Canceled
	}
	s.advance()
	if st.watchErr != nil {
		return nil, st.watchErr
	}
	ch := make(chan PodEvent, len(st.watchEvents))
	for _, ev := range st.watchEvents {
		ch <- ev
	}
	close(ch)
	return &chanPodWatch{ch: ch}, nil
}

// chanPodWatch adapts a closed channel to the PodWatch interface.
type chanPodWatch struct{ ch chan PodEvent }

func (w *chanPodWatch) Events() <-chan PodEvent { return w.ch }
func (w *chanPodWatch) Stop()                   {}

// runPods executes the pod collector over the script until exhaustion.
func runPods(t *testing.T, steps []podStep) *PodStore {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &scriptedPodSource{steps: steps, cancel: cancel}
	store := NewPodStore()
	c := NewPodCollector(src, store, newFakeClock(), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err := c.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return store
}

func pod(name, uid, role string) PodFacts {
	return PodFacts{Name: name, UID: uid, Role: role, Phase: "Running"}
}

func TestPodCollectorPublishesFetchAndEvents(t *testing.T) {
	t.Parallel()
	store := runPods(t, []podStep{{
		fetchPods: []PodFacts{pod("orders-1", "u1", "primary"), pod("orders-2", "u2", "replica")},
		fetchRV:   "10",
		watchEvents: []PodEvent{
			{Put: &PodFacts{Name: "orders-3", UID: "u3", Role: "replica", Phase: "Pending"}},
		},
	}})
	snap, ok := store.CurrentPods()
	if !ok {
		t.Fatal("no snapshot published")
	}
	if len(snap.Pods) != 3 {
		t.Fatalf("pods = %d, want 3", len(snap.Pods))
	}
	if snap.Pods[0].Name != "orders-1" || snap.Pods[2].Name != "orders-3" {
		t.Errorf("pods not sorted by name: %+v", snap.Pods)
	}
}

// TestPodCollectorDeletionChecksUID proves a deletion whose name was
// reused by a newer incarnation does not remove the newer pod.
func TestPodCollectorDeletionChecksUID(t *testing.T) {
	t.Parallel()
	store := runPods(t, []podStep{{
		fetchPods: []PodFacts{pod("orders-1", "old", "primary")},
		fetchRV:   "10",
		watchEvents: []PodEvent{
			// The replacement arrives before the old incarnation's
			// deletion, as watches may deliver after a fast restart.
			{Put: &PodFacts{Name: "orders-1", UID: "new", Role: "primary", Phase: "Running"}},
			{Delete: &PodDeletion{Name: "orders-1", UID: "old"}},
			// A matching deletion of a different pod removes it.
			{Put: &PodFacts{Name: "orders-2", UID: "u2", Role: "replica"}},
			{Delete: &PodDeletion{Name: "orders-2", UID: "u2"}},
		},
	}})
	snap, _ := store.CurrentPods()
	if len(snap.Pods) != 1 {
		t.Fatalf("pods = %d, want the surviving incarnation only", len(snap.Pods))
	}
	if snap.Pods[0].UID != "new" {
		t.Errorf("surviving UID = %q, want the newer incarnation", snap.Pods[0].UID)
	}
}

func TestPodCollectorWatchBreakRetainsStale(t *testing.T) {
	t.Parallel()
	store := runPods(t, []podStep{
		{fetchPods: []PodFacts{pod("orders-1", "u1", "primary")}, fetchRV: "10"},
		{fetchErr: redact.NewError("pods list", redact.CategoryTimeout, nil)},
	})
	snap, ok := store.CurrentPods()
	if !ok {
		t.Fatal("last-good snapshot dropped")
	}
	if !snap.Stale {
		t.Error("snapshot not stale after watch break and failed refetch")
	}
	if len(snap.Pods) != 1 {
		t.Error("last-good pod set lost")
	}
}

// TestPodStoreBoundsAndFlagsTruncation proves the MaxPods bound holds
// for any publication and is visible, never silent.
func TestPodStoreBoundsAndFlagsTruncation(t *testing.T) {
	t.Parallel()
	flood := make([]PodFacts, MaxPods+250)
	for i := range flood {
		flood[i] = pod(fmt.Sprintf("orders-%04d", i), fmt.Sprintf("u%d", i), "replica")
	}
	store := runPods(t, []podStep{{fetchPods: flood, fetchRV: "10"}})
	snap, _ := store.CurrentPods()
	if len(snap.Pods) != MaxPods {
		t.Fatalf("pods = %d, want bounded at %d", len(snap.Pods), MaxPods)
	}
	if !snap.Truncated {
		t.Fatal("truncation not flagged")
	}
}

func TestPodStoreSnapshotsAreImmutableCopies(t *testing.T) {
	t.Parallel()
	store := NewPodStore()
	source := []PodFacts{pod("orders-1", "u1", "primary")}
	store.publish(source, newFakeClock().Now())
	first, _ := store.CurrentPods()
	source[0].Role = "mutated"
	store.publish([]PodFacts{pod("orders-1", "u1", "replica")}, newFakeClock().Now())
	if first.Pods[0].Role != "primary" {
		t.Error("a previously returned snapshot changed after publication")
	}
}
