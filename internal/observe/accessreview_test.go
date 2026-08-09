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

type accessStep struct {
	state    AccessReviewState
	fetchErr error
	changes  []AccessRequestChange
}

type scriptedAccessSource struct {
	mu     sync.Mutex
	steps  []accessStep
	i      int
	cancel context.CancelFunc
}

func (s *scriptedAccessSource) FetchAccessReview(_ context.Context) (AccessReviewState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.steps) {
		return AccessReviewState{}, context.Canceled
	}
	step := s.steps[s.i]
	if step.fetchErr != nil {
		s.i++
		if s.i == len(s.steps) {
			s.cancel()
		}
	}
	return step.state, step.fetchErr
}

func (s *scriptedAccessSource) WatchAccessReview(_ context.Context, _ AccessReviewState) (AccessReviewWatch, error) {
	s.mu.Lock()
	step := s.steps[s.i]
	s.i++
	done := s.i == len(s.steps)
	s.mu.Unlock()
	changes := make(chan AccessRequestChange, len(step.changes))
	for _, change := range step.changes {
		changes <- change
	}
	close(changes)
	if done {
		s.cancel()
	}
	return staticAccessWatch{changes}, nil
}

type staticAccessWatch struct{ changes <-chan AccessRequestChange }

func (w staticAccessWatch) Changes() <-chan AccessRequestChange { return w.changes }
func (staticAccessWatch) Stop()                                 {}

func runAccessReview(t *testing.T, steps []accessStep) *AccessReviewStore {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &scriptedAccessSource{steps: steps, cancel: cancel}
	store := NewAccessReviewStore()
	collector := NewAccessReviewCollector(source, store, newFakeClock(), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err := collector.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return store
}

// TestAccessReviewCollectorSeedsWatchesAndRetainsStale proves the seed
// plus watch upsert publishes, and a later fetch failure retains the
// last-good view stale.
func TestAccessReviewCollectorSeedsWatchesAndRetainsStale(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	store := runAccessReview(t, []accessStep{
		{
			state: AccessReviewState{
				Requests: []AccessRequestFacts{{Name: "req-1", UID: "u1", Subject: "a", State: AccessRequestPending, CreatedAt: created}},
			},
			changes: []AccessRequestChange{{Put: &AccessRequestFacts{Name: "req-2", UID: "u2", Subject: "b", State: AccessRequestPending, CreatedAt: created.Add(time.Minute)}}},
		},
		{fetchErr: redact.NewError("access requests list", redact.CategoryForbidden, nil)},
	})
	snap, ok := store.CurrentAccessReview()
	if !ok || !snap.Stale {
		t.Fatalf("last-good review not retained stale: %+v ok=%v", snap, ok)
	}
	if len(snap.Requests) != 2 {
		t.Fatalf("view lost content: %+v", snap)
	}
}

// TestAccessReviewDeletionChecksUID proves a delete for a stale
// incarnation does not remove the live request.
func TestAccessReviewDeletionChecksUID(t *testing.T) {
	t.Parallel()
	store := runAccessReview(t, []accessStep{
		{
			state:   AccessReviewState{Requests: []AccessRequestFacts{{Name: "req-1", UID: "u-new", State: AccessRequestPending}}},
			changes: []AccessRequestChange{{Delete: &AccessRequestDeletion{Name: "req-1", UID: "u-old"}}},
		},
	})
	snap, _ := store.CurrentAccessReview()
	if len(snap.Requests) != 1 {
		t.Fatalf("stale-UID deletion removed the live request: %+v", snap)
	}
}

// TestAccessReviewStoreOrdersPendingFirst proves the ordering: pending
// oldest-first, then decided newest-first.
func TestAccessReviewStoreOrdersPendingFirst(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	oldDecision := base.Add(-2 * time.Hour)
	newDecision := base.Add(-1 * time.Hour)
	store := NewAccessReviewStore()
	store.publish([]AccessRequestFacts{
		{Name: "decided-old", State: AccessRequestApproved, DecidedAt: &oldDecision},
		{Name: "pending-new", State: AccessRequestPending, CreatedAt: base.Add(-10 * time.Minute)},
		{Name: "decided-new", State: AccessRequestDenied, DecidedAt: &newDecision},
		{Name: "pending-old", State: AccessRequestPending, CreatedAt: base.Add(-30 * time.Minute)},
	}, base, false)

	snap, _ := store.CurrentAccessReview()
	got := make([]string, len(snap.Requests))
	for i, r := range snap.Requests {
		got[i] = r.Name
	}
	want := []string{"pending-old", "pending-new", "decided-new", "decided-old"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

// TestAccessReviewStoreBoundsAndFlagsTruncation proves the request bound
// holds and the truncation flag surfaces.
func TestAccessReviewStoreBoundsAndFlagsTruncation(t *testing.T) {
	t.Parallel()
	requests := make([]AccessRequestFacts, MaxAccessRequests+50)
	for i := range requests {
		requests[i] = AccessRequestFacts{Name: fmt.Sprintf("req-%04d", i), State: AccessRequestPending, CreatedAt: time.Unix(int64(i), 0)}
	}
	store := NewAccessReviewStore()
	store.publish(requests, time.Unix(1000, 0), false)
	snap, _ := store.CurrentAccessReview()
	if len(snap.Requests) != MaxAccessRequests || !snap.RequestsTruncated {
		t.Fatalf("request bound not visible: len=%d truncated=%v", len(snap.Requests), snap.RequestsTruncated)
	}
}

// TestAccessReviewStorePublishesATruncatedSetBelowTheBound proves the
// rendered cut is decided by the length and never by the truncation
// flag. The flag is sticky for the life of a seed, so a set that was
// once over the bound and has since shrunk below it still arrives here
// flagged; cutting on the flag sliced past the end and panicked.
func TestAccessReviewStorePublishesATruncatedSetBelowTheBound(t *testing.T) {
	t.Parallel()
	requests := []AccessRequestFacts{
		{Name: "req-a", State: AccessRequestPending, CreatedAt: time.Unix(1, 0)},
		{Name: "req-b", State: AccessRequestPending, CreatedAt: time.Unix(2, 0)},
	}
	store := NewAccessReviewStore()
	store.publish(requests, time.Unix(1000, 0), true)

	snap, ok := store.CurrentAccessReview()
	if !ok {
		t.Fatal("no snapshot published")
	}
	if len(snap.Requests) != len(requests) {
		t.Errorf("published %d requests, want all %d retained", len(snap.Requests), len(requests))
	}
	if !snap.RequestsTruncated {
		t.Error("the source truncation flag was dropped; the page would claim a complete queue")
	}
}
