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

// MaxQuorumStandbys bounds the rendered synchronous-standby list.
const MaxQuorumStandbys = 32

// FailoverQuorumFacts is the operator-reported failover quorum of the
// target cluster.
//
// The resource is written by the primary's instance manager and reset by
// the operator, so every value here is a report about synchronous
// replication rather than a claim the console verifies. It exists only
// when the cluster runs synchronous replication with a quorum, which is
// why absence is a first-class state rather than an error.
type FailoverQuorumFacts struct {
	// Present reports that the API server confirmed the resource
	// exists. False is a successful observation of absence: the cluster
	// is not running failover quorum.
	Present bool
	// Method is the latest reported synchronous-replication method.
	Method string
	// Primary is the instance that last updated the quorum.
	Primary string
	// StandbyNumber is how many synchronous standbys a transaction
	// waits for.
	StandbyNumber int
	// Standbys is the bounded list of potentially synchronous instances.
	Standbys []string
	// StandbysTruncated reports that more standbys were reported than
	// the display bound.
	StandbysTruncated bool
}

// FailoverQuorumState is one complete observation. It carries no
// resource version for the same reason ClusterState does not: a
// singleton get yields no watch-safe cursor.
type FailoverQuorumState struct {
	// Facts is the observed quorum, or an absence.
	Facts FailoverQuorumFacts
}

// FailoverQuorumWatch is a running watch on the cluster's quorum object.
type FailoverQuorumWatch interface {
	// Results streams observations until the watch ends.
	Results() <-chan FailoverQuorumState
	// Stop releases the watch.
	Stop()
}

// FailoverQuorumSource produces observations of the one quorum object
// belonging to the target cluster.
type FailoverQuorumSource interface {
	// FetchFailoverQuorum returns the current state through a pinned
	// get. An absent object is a successful observation, not an error.
	FetchFailoverQuorum(ctx context.Context) (FailoverQuorumState, error)
	// WatchFailoverQuorum streams observations from the server's
	// current state: the present object is re-delivered as the first
	// result, then changes follow.
	WatchFailoverQuorum(ctx context.Context) (FailoverQuorumWatch, error)
}

// FailoverQuorumSnapshot is the rendered quorum, immutable and carrying
// its own staleness.
type FailoverQuorumSnapshot struct {
	// Generation increases by one on every publication.
	Generation uint64
	// ObservedAt is the time of the last successful contact.
	ObservedAt time.Time
	// Stale reports lost contact; the facts below are the retained
	// last-good observation.
	Stale bool
	// Quorum is the last observed state.
	Quorum FailoverQuorumFacts
}

// FailoverQuorumStore holds the current quorum snapshot for concurrent
// readers.
type FailoverQuorumStore struct {
	mu   sync.RWMutex
	snap FailoverQuorumSnapshot
	has  bool
}

// NewFailoverQuorumStore returns an empty store.
func NewFailoverQuorumStore() *FailoverQuorumStore { return &FailoverQuorumStore{} }

// CurrentFailoverQuorum returns the snapshot and whether one exists.
func (s *FailoverQuorumStore) CurrentFailoverQuorum() (FailoverQuorumSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.has
}

// publish replaces the snapshot, advancing the generation and clearing
// staleness.
func (s *FailoverQuorumStore) publish(facts FailoverQuorumFacts, observedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = FailoverQuorumSnapshot{
		Generation: s.snap.Generation + 1,
		ObservedAt: observedAt,
		Quorum:     facts,
	}
	s.has = true
}

// markStale marks the retained snapshot stale, if one exists.
func (s *FailoverQuorumStore) markStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has {
		return
	}
	s.snap.Stale = true
}

// FailoverQuorumCollector maintains the quorum store on the shared loop.
// This is the singleton shape: the retained state is one value and a
// watch item replaces it wholesale.
type FailoverQuorumCollector struct {
	source FailoverQuorumSource
	store  *FailoverQuorumStore
	clock  Clock
	logger *slog.Logger
	facts  FailoverQuorumFacts
}

// NewFailoverQuorumCollector wires a quorum collector onto a store.
func NewFailoverQuorumCollector(source FailoverQuorumSource, store *FailoverQuorumStore, clock Clock, logger *slog.Logger) *FailoverQuorumCollector {
	return &FailoverQuorumCollector{source: source, store: store, clock: clock, logger: logger}
}

// Run blocks until ctx is done, maintaining the store.
func (c *FailoverQuorumCollector) Run(ctx context.Context) error {
	return newLoop[struct{}, FailoverQuorumState](c, c.clock, c.logger).Run(ctx)
}

// op names this resource in contact-loss logs.
func (c *FailoverQuorumCollector) op() string { return "failover quorum" }

// seed takes the pinned get. An absent object seeds like any other, so
// the name-scoped watch that follows delivers an addition if the cluster
// later starts running a quorum. It hands the watch nothing: a singleton
// get yields no watch-safe cursor — see ClusterState.
func (c *FailoverQuorumCollector) seed(ctx context.Context) (struct{}, error) {
	state, err := c.source.FetchFailoverQuorum(ctx)
	if err != nil {
		return struct{}{}, err
	}
	c.facts = state.Facts
	return struct{}{}, nil
}

// follow starts the name-scoped watch from the server's current state.
func (c *FailoverQuorumCollector) follow(ctx context.Context, _ struct{}) (<-chan FailoverQuorumState, func(), error) {
	w, err := c.source.WatchFailoverQuorum(ctx)
	if err != nil {
		return nil, nil, err
	}
	return w.Results(), w.Stop, nil
}

// apply replaces the retained facts. A singleton watch delivers complete
// observations rather than deltas, so every item is recognized.
func (c *FailoverQuorumCollector) apply(state FailoverQuorumState) bool {
	c.facts = state.Facts
	return true
}

// publish snapshots the retained facts into the store.
func (c *FailoverQuorumCollector) publish(observedAt time.Time) {
	c.store.publish(c.facts, observedAt)
}

// markStale marks the retained snapshot stale, if one exists.
func (c *FailoverQuorumCollector) markStale() { c.store.markStale() }
