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

// PrimaryLeaseFacts is the Kubernetes Lease CloudNativePG 1.30 keeps
// as its primary-election gate, named after the cluster. The instance
// promoted to primary acquires it and renews it continuously; a primary
// shutting down releases it. Everything here is the Lease object's own
// spec, read verbatim: the console adds no judgement of its own.
type PrimaryLeaseFacts struct {
	// Present reports that the API server confirmed the Lease exists.
	// False is a successful observation of absence: an operator before
	// 1.30 keeps none.
	Present bool
	// Holder is the instance holding the lease, empty when released.
	Holder string
	// AcquiredAt and RenewedAt are the holder's own stamps; nil when
	// the Lease carries none.
	AcquiredAt *time.Time
	RenewedAt  *time.Time
	// DurationSeconds is how long a renewal is good for; nil when the
	// Lease carries none.
	DurationSeconds *int32
	// Transitions counts how many times the holder changed, as the
	// holders themselves maintain it; nil when the Lease carries none.
	Transitions *int32
}

// PrimaryLeaseState is one complete observation delivered by a source.
type PrimaryLeaseState struct {
	// Facts is the observed lease, or an absence.
	Facts PrimaryLeaseFacts
}

// PrimaryLeaseWatch is a running name-scoped watch on the Lease.
type PrimaryLeaseWatch interface {
	// Results streams observations until the watch ends.
	Results() <-chan PrimaryLeaseState
	// Stop releases the watch.
	Stop()
}

// PrimaryLeaseSource produces observations of the one Lease.
type PrimaryLeaseSource interface {
	// FetchPrimaryLease returns the current state through a pinned get.
	// An absent object is a successful observation, not an error.
	FetchPrimaryLease(ctx context.Context) (PrimaryLeaseState, error)
	// WatchPrimaryLease streams observations from the server's current
	// state: the present object is re-delivered as the first result,
	// then changes follow.
	WatchPrimaryLease(ctx context.Context) (PrimaryLeaseWatch, error)
}

// PrimaryLeaseSnapshot is what the console reads: the last observed
// lease and its own staleness.
type PrimaryLeaseSnapshot struct {
	// Generation increases by one on every publication.
	Generation uint64
	// ObservedAt is the time of the last successful contact.
	ObservedAt time.Time
	// Stale reports lost contact; the facts below are the retained
	// last-good observation.
	Stale bool
	// Lease is the last observed state.
	Lease PrimaryLeaseFacts
}

// PrimaryLeaseStore holds the current lease snapshot for concurrent
// readers.
type PrimaryLeaseStore struct {
	mu   sync.RWMutex
	snap PrimaryLeaseSnapshot
	has  bool
}

// NewPrimaryLeaseStore returns an empty store.
func NewPrimaryLeaseStore() *PrimaryLeaseStore { return &PrimaryLeaseStore{} }

// CurrentPrimaryLease returns the snapshot and whether one exists.
func (s *PrimaryLeaseStore) CurrentPrimaryLease() (PrimaryLeaseSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.has
}

func (s *PrimaryLeaseStore) publish(facts PrimaryLeaseFacts, observedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = PrimaryLeaseSnapshot{
		Generation: s.snap.Generation + 1,
		ObservedAt: observedAt,
		Lease:      facts,
	}
	s.has = true
}

func (s *PrimaryLeaseStore) markStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has {
		return
	}
	s.snap.Stale = true
}

// PrimaryLeaseCollector maintains the store through the shared
// seed-and-follow loop, exactly as the failover-quorum collector does:
// one pinned get, one name-scoped watch, and a re-seed on every break.
type PrimaryLeaseCollector struct {
	source PrimaryLeaseSource
	store  *PrimaryLeaseStore
	clock  Clock
	logger *slog.Logger
	facts  PrimaryLeaseFacts
}

// NewPrimaryLeaseCollector wires a source to a store.
func NewPrimaryLeaseCollector(source PrimaryLeaseSource, store *PrimaryLeaseStore, clock Clock, logger *slog.Logger) *PrimaryLeaseCollector {
	return &PrimaryLeaseCollector{source: source, store: store, clock: clock, logger: logger}
}

// Run blocks until ctx is done, maintaining the store.
func (c *PrimaryLeaseCollector) Run(ctx context.Context) error {
	return newLoop[struct{}, PrimaryLeaseState](c, c.clock, c.logger).Run(ctx)
}

func (c *PrimaryLeaseCollector) op() string { return "primary lease" }

func (c *PrimaryLeaseCollector) seed(ctx context.Context) (struct{}, error) {
	state, err := c.source.FetchPrimaryLease(ctx)
	if err != nil {
		return struct{}{}, err
	}
	c.facts = state.Facts
	return struct{}{}, nil
}

func (c *PrimaryLeaseCollector) follow(ctx context.Context, _ struct{}) (<-chan PrimaryLeaseState, func(), error) {
	w, err := c.source.WatchPrimaryLease(ctx)
	if err != nil {
		return nil, nil, err
	}
	return w.Results(), w.Stop, nil
}

func (c *PrimaryLeaseCollector) apply(state PrimaryLeaseState) bool {
	c.facts = state.Facts
	return true
}

func (c *PrimaryLeaseCollector) publish(observedAt time.Time) {
	c.store.publish(c.facts, observedAt)
}

func (c *PrimaryLeaseCollector) markStale() {
	c.store.markStale()
}
