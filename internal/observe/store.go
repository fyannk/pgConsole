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
	"sync"
	"time"
)

// Store holds the current snapshot for concurrent readers. Writers are
// the collector only; readers receive value copies, so a published
// snapshot is never mutated.
type Store struct {
	mu   sync.RWMutex
	snap Snapshot
	has  bool
}

// NewStore returns an empty store: Current reports no snapshot until
// the first successful observation.
func NewStore() *Store {
	return &Store{}
}

// Current returns the snapshot and whether one exists.
func (s *Store) Current() (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.has
}

// publish replaces the snapshot with a fresh observation, advancing the
// generation and clearing staleness.
func (s *Store) publish(facts ClusterFacts, observedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = Snapshot{
		Generation: s.snap.Generation + 1,
		ObservedAt: observedAt,
		Stale:      false,
		Cluster:    facts,
	}
	s.has = true
}

// markStale marks the retained snapshot stale. Without a snapshot it
// does nothing: there is no last-good observation to retain, and the
// absence of a snapshot already renders as unknown.
func (s *Store) markStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has {
		return
	}
	s.snap.Stale = true
}
