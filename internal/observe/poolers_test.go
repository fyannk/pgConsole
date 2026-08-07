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

	"github.com/fyannk/pgConsole/internal/redact"
)

type poolerStep struct {
	poolers   []PoolerFacts
	truncated bool
	fetchErr  error
	changes   []PoolerChange
}

type scriptedPoolerSource struct {
	mu     sync.Mutex
	steps  []poolerStep
	i      int
	cancel context.CancelFunc
}

func (s *scriptedPoolerSource) FetchPoolers(_ context.Context) ([]PoolerFacts, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.steps) {
		return nil, "", false, context.Canceled
	}
	step := s.steps[s.i]
	if step.fetchErr != nil {
		s.i++
		if s.i == len(s.steps) {
			s.cancel()
		}
	}
	return step.poolers, "rv", step.truncated, step.fetchErr
}

func (s *scriptedPoolerSource) WatchPoolers(_ context.Context, _ string) (PoolerWatch, error) {
	s.mu.Lock()
	step := s.steps[s.i]
	s.i++
	done := s.i == len(s.steps)
	s.mu.Unlock()
	changes := make(chan PoolerChange, len(step.changes))
	for _, change := range step.changes {
		changes <- change
	}
	close(changes)
	if done {
		s.cancel()
	}
	return staticPoolerWatch{changes}, nil
}

type staticPoolerWatch struct{ changes <-chan PoolerChange }

func (w staticPoolerWatch) Changes() <-chan PoolerChange { return w.changes }
func (staticPoolerWatch) Stop()                          {}

func runPoolers(t *testing.T, steps []poolerStep) *PoolerStore {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &scriptedPoolerSource{steps: steps, cancel: cancel}
	store := NewPoolerStore()
	collector := NewPoolerCollector(source, store, newFakeClock(), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err := collector.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return store
}

func poolerFacts(name string) PoolerFacts {
	return PoolerFacts{Name: name, UID: "u-" + name, Type: "rw", Phase: "active"}
}

// TestPoolerCollectorPublishesAndRetainsOnContactLoss proves the section
// keeps its last-good set when the watch breaks and says so, rather than
// blanking and implying no poolers exist.
func TestPoolerCollectorPublishesAndRetainsOnContactLoss(t *testing.T) {
	t.Parallel()
	store := runPoolers(t, []poolerStep{
		{poolers: []PoolerFacts{poolerFacts("orders-rw")}},
		{fetchErr: redact.NewError("poolers list", redact.CategoryForbidden, nil)},
	})

	snap, ok := store.CurrentPoolers()
	if !ok {
		t.Fatal("no snapshot published")
	}
	if len(snap.Poolers) != 1 || snap.Poolers[0].Name != "orders-rw" {
		t.Fatalf("retained set = %+v, want the last-good pooler", snap.Poolers)
	}
	if !snap.Stale {
		t.Error("contact was lost and the snapshot is not marked stale")
	}
}

// TestPoolerCollectorFoldsChangesAndChecksUIDOnDelete proves a deletion
// for an incarnation that no longer exists cannot remove the pooler that
// reused its name.
func TestPoolerCollectorFoldsChangesAndChecksUIDOnDelete(t *testing.T) {
	t.Parallel()
	added := poolerFacts("orders-ro")
	store := runPoolers(t, []poolerStep{{
		poolers: []PoolerFacts{poolerFacts("orders-rw")},
		changes: []PoolerChange{
			{Put: &added},
			{Delete: &PoolerDeletion{Name: "orders-rw", UID: "stale-incarnation"}},
		},
	}})

	snap, _ := store.CurrentPoolers()
	if len(snap.Poolers) != 2 {
		t.Fatalf("published %d poolers, want both retained: %+v", len(snap.Poolers), snap.Poolers)
	}
	if snap.Poolers[0].Name != "orders-ro" || snap.Poolers[1].Name != "orders-rw" {
		t.Errorf("order = %+v, want name-sorted", snap.Poolers)
	}
}

// TestPoolerStoreBoundsAndFlagsTruncation proves neither memory nor the
// page grows with a namespace holding more poolers than the bound, and
// that the cut is visible rather than silent.
func TestPoolerStoreBoundsAndFlagsTruncation(t *testing.T) {
	t.Parallel()
	poolers := make([]PoolerFacts, MaxPoolers+25)
	for i := range poolers {
		poolers[i] = poolerFacts(fmt.Sprintf("pooler-%04d", i))
	}
	store := NewPoolerStore()
	store.publish(poolers, time.Unix(1000, 0), false)

	snap, _ := store.CurrentPoolers()
	if len(snap.Poolers) != MaxPoolers {
		t.Fatalf("published %d poolers, want the bound of %d", len(snap.Poolers), MaxPoolers)
	}
	if !snap.Truncated {
		t.Error("the cut is not visible on the page")
	}
}

// TestPoolerStorePublishesATruncatedSetBelowTheBound proves the cut is
// decided by the length and never by the sticky source flag — the same
// slice panic the review and backup stores carried.
func TestPoolerStorePublishesATruncatedSetBelowTheBound(t *testing.T) {
	t.Parallel()
	store := NewPoolerStore()
	store.publish([]PoolerFacts{poolerFacts("orders-rw")}, time.Unix(1000, 0), true)

	snap, ok := store.CurrentPoolers()
	if !ok {
		t.Fatal("no snapshot published")
	}
	if len(snap.Poolers) != 1 {
		t.Errorf("published %d poolers, want the one retained", len(snap.Poolers))
	}
	if !snap.Truncated {
		t.Error("the source truncation flag was dropped")
	}
}
