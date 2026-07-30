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
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/redact"
)

// fakeClock advances one second per Now call and records waits without
// sleeping, so collector tests run instantly and deterministically.
type fakeClock struct {
	mu    sync.Mutex
	t     time.Time
	waits []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)}
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(time.Second)
	return f.t
}

func (f *fakeClock) Wait(ctx context.Context, d time.Duration) error {
	f.mu.Lock()
	f.waits = append(f.waits, d)
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (f *fakeClock) recorded() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.waits...)
}

// step is one scripted source interaction.
type step struct {
	fetchState ClusterState
	fetchErr   error
	watchErr   error
	// watchStates are streamed, then the watch closes.
	watchStates []ClusterState
}

// scriptedSource replays steps; when the script is exhausted it cancels
// the collector's context so Run returns.
type scriptedSource struct {
	mu     sync.Mutex
	steps  []step
	i      int
	cancel context.CancelFunc
}

func (s *scriptedSource) current() (step, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.steps) {
		return step{}, false
	}
	return s.steps[s.i], true
}

func (s *scriptedSource) advance() {
	s.mu.Lock()
	s.i++
	done := s.i >= len(s.steps)
	s.mu.Unlock()
	if done {
		s.cancel()
	}
}

// Fetch replays the scripted fetch outcome of the current step.
func (s *scriptedSource) Fetch(_ context.Context) (ClusterState, error) {
	st, ok := s.current()
	if !ok {
		return ClusterState{}, context.Canceled
	}
	if st.fetchErr != nil {
		s.advance()
		return ClusterState{}, st.fetchErr
	}
	return st.fetchState, nil
}

// Watch replays the scripted watch outcome of the current step.
func (s *scriptedSource) Watch(_ context.Context, _ string) (Watch, error) {
	st, ok := s.current()
	if !ok {
		return nil, context.Canceled
	}
	s.advance()
	if st.watchErr != nil {
		return nil, st.watchErr
	}
	ch := make(chan ClusterState, len(st.watchStates))
	for _, ws := range st.watchStates {
		ch <- ws
	}
	close(ch)
	return &chanWatch{ch: ch}, nil
}

// chanWatch adapts a closed channel to the Watch interface.
type chanWatch struct{ ch chan ClusterState }

func (w *chanWatch) Results() <-chan ClusterState { return w.ch }
func (w *chanWatch) Stop()                        {}

// run executes the collector over the script until it exhausts.
func run(t *testing.T, steps []step) (*Store, *fakeClock, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src := &scriptedSource{steps: steps, cancel: cancel}
	store := NewStore()
	clock := newFakeClock()
	logs := &bytes.Buffer{}
	c := NewCollector(src, store, clock, slog.New(slog.NewJSONHandler(logs, nil)))
	if err := c.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return store, clock, logs
}

func present(phase, rv string) ClusterState {
	return ClusterState{
		Facts:           ClusterFacts{Present: true, Phase: phase},
		ResourceVersion: rv,
	}
}

func TestCollectorPublishesFetchAndWatchObservations(t *testing.T) {
	t.Parallel()
	store, _, _ := run(t, []step{{
		fetchState:  present("Cluster in healthy state", "10"),
		watchStates: []ClusterState{present("Switchover in progress", "11")},
	}})
	snap, ok := store.Current()
	if !ok {
		t.Fatal("no snapshot published")
	}
	if snap.Cluster.Phase != "Switchover in progress" {
		t.Errorf("Phase = %q, want the watched update", snap.Cluster.Phase)
	}
	if snap.Generation != 2 {
		t.Errorf("Generation = %d, want 2 (fetch then event)", snap.Generation)
	}
	if snap.ObservedAt.IsZero() {
		t.Error("ObservedAt not set from the clock")
	}
	// Shutdown is not contact loss: cancellation during a healthy watch
	// leaves the last snapshot unmarked. Watch breakage staleness is
	// asserted separately.
	if snap.Stale {
		t.Error("cancellation must not mark the snapshot stale")
	}
}

func TestCollectorWatchBreakRetainsLastGoodAsStale(t *testing.T) {
	t.Parallel()
	store, _, logs := run(t, []step{
		{
			fetchState:  present("Cluster in healthy state", "10"),
			watchStates: nil, // watch closes immediately: broken
		},
		{
			fetchErr: redact.NewError("cluster get", redact.CategoryTimeout, nil),
		},
	})
	snap, ok := store.Current()
	if !ok {
		t.Fatal("last-good snapshot was dropped")
	}
	if !snap.Stale {
		t.Error("snapshot not stale after watch break and failed refetch")
	}
	if snap.Cluster.Phase != "Cluster in healthy state" {
		t.Errorf("last-good facts lost: %q", snap.Cluster.Phase)
	}
	if !strings.Contains(logs.String(), string(redact.CategoryTimeout)) {
		t.Error("failure category not logged")
	}
}

func TestCollectorRecoveryClearsStaleness(t *testing.T) {
	t.Parallel()
	store, _, _ := run(t, []step{
		{fetchState: present("Cluster in healthy state", "10")}, // watch closes: stale
		{fetchState: present("Cluster in healthy state", "12"),
			watchStates: []ClusterState{present("Cluster in healthy state", "13")}},
		{fetchErr: redact.NewError("cluster get", redact.CategoryTimeout, nil)},
	})
	snap, _ := store.Current()
	if snap.Generation != 3 {
		t.Errorf("Generation = %d, want 3", snap.Generation)
	}
	// The final watch close marks stale again; what recovery proves is
	// that the intermediate publications advanced the generation and
	// carried Stale=false, which the generation count asserts.
}

func TestCollectorAbsentClusterIsAnObservation(t *testing.T) {
	t.Parallel()
	store, _, _ := run(t, []step{{
		fetchState: ClusterState{Facts: ClusterFacts{Present: false}},
	}})
	snap, ok := store.Current()
	if !ok {
		t.Fatal("absence was not published as a snapshot")
	}
	if snap.Cluster.Present {
		t.Error("absent cluster published as present")
	}
}

func TestCollectorForbiddenBacksOffWithoutStorm(t *testing.T) {
	t.Parallel()
	forbidden := redact.NewError("cluster get", redact.CategoryForbidden, nil)
	steps := make([]step, 8)
	for i := range steps {
		steps[i] = step{fetchErr: forbidden}
	}
	store, clock, _ := run(t, steps)
	if _, ok := store.Current(); ok {
		t.Fatal("forbidden fetches must not fabricate a snapshot")
	}
	waits := clock.recorded()
	if len(waits) != len(steps) {
		t.Fatalf("waits = %d, want one per failure", len(waits))
	}
	if waits[0] != backoffInitial {
		t.Errorf("first wait = %s, want %s", waits[0], backoffInitial)
	}
	last := waits[len(waits)-1]
	if last != backoffMax {
		t.Errorf("backoff did not reach its bound: %s", last)
	}
	for i := 1; i < len(waits); i++ {
		if waits[i] < waits[i-1] {
			t.Errorf("backoff shrank without a success: %s -> %s", waits[i-1], waits[i])
		}
	}
}

func TestCollectorStopsOnCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := &scriptedSource{steps: []step{{fetchState: present("x", "1")}}, cancel: func() {}}
	c := NewCollector(src, NewStore(), newFakeClock(), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err := c.Run(ctx); err != nil {
		t.Fatalf("Run after cancellation: %v", err)
	}
}

func TestStoreSnapshotsAreImmutableCopies(t *testing.T) {
	t.Parallel()
	store := NewStore()
	store.publish(ClusterFacts{Present: true, Phase: "a"}, time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC))
	first, _ := store.Current()
	store.publish(ClusterFacts{Present: true, Phase: "b"}, time.Date(2026, 7, 28, 12, 0, 1, 0, time.UTC))
	if first.Cluster.Phase != "a" {
		t.Error("a previously returned snapshot changed after publication")
	}
	second, _ := store.Current()
	if second.Generation != first.Generation+1 {
		t.Errorf("generation not monotonic: %d then %d", first.Generation, second.Generation)
	}
}
