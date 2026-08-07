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

// MaxPoolers bounds retained and rendered Pooler resources. One extra is
// retained internally as a truncation sentinel.
const MaxPoolers = 100

// PoolerFacts is the bounded operator-reported state of one Pooler.
//
// The connection pooler sits between the applications and the database,
// so what an operator needs from this screen is which endpoint it fronts
// and whether it is accepting connections. Nothing from
// status.secrets crosses this boundary: the console never reads Secret
// material, and a pooler's credential references are not review data.
type PoolerFacts struct {
	// Name is the resource name.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// Type is the endpoint the pooler fronts: rw, ro or r.
	Type string
	// PoolMode is the configured PgBouncer pooling mode, session or
	// transaction. Empty when the resource did not report one.
	PoolMode string
	// DesiredInstances is the requested pooler pod count. Nil when the
	// resource did not report one.
	DesiredInstances *int32
	// ReadyInstances is the operator-reported scheduled pod count.
	ReadyInstances int32
	// Phase is the operator-reported lifecycle phase: active, paused,
	// inactive or failed.
	Phase string
	// PhaseReason is the operator's explanation of the phase.
	PhaseReason string
	// Image is the resolved pgbouncer image the operator reports. While
	// the phase is inactive or failed it may be the last known one.
	Image string
	// CreatedAt is the resource creation time.
	CreatedAt time.Time
}

// PoolerDeletion identifies a removed Pooler incarnation.
type PoolerDeletion struct {
	// Name is the removed resource name.
	Name string
	// UID is the removed incarnation.
	UID string
}

// PoolerChange is one change from the pooler watch. Exactly one field is
// set.
type PoolerChange struct {
	// Put upserts one Pooler.
	Put *PoolerFacts
	// Delete removes one Pooler incarnation.
	Delete *PoolerDeletion
}

// PoolerWatch is a running watch on the namespace's Pooler resources.
type PoolerWatch interface {
	// Changes streams changes until the watch ends.
	Changes() <-chan PoolerChange
	// Stop releases the watch.
	Stop()
}

// PoolerSource produces the target cluster's Pooler resources. The
// Kubernetes adapter lists namespace-scoped and selects by
// spec.cluster.name.
type PoolerSource interface {
	// FetchPoolers returns the current selected set and the resource
	// version to resume watching from.
	FetchPoolers(ctx context.Context) (poolers []PoolerFacts, resourceVersion string, truncated bool, err error)
	// WatchPoolers streams changes from the given resource version.
	WatchPoolers(ctx context.Context, fromResourceVersion string) (PoolerWatch, error)
}

// PoolersSnapshot is the rendered pooler set, immutable and carrying its
// own staleness: sources fail independently.
type PoolersSnapshot struct {
	// Generation increases by one on every publication.
	Generation uint64
	// ObservedAt is the time of the last successful contact.
	ObservedAt time.Time
	// Stale reports lost contact; the set below is the retained
	// last-good observation.
	Stale bool
	// Truncated reports that more matched than the bound.
	Truncated bool
	// Poolers is sorted by name and bounded by MaxPoolers.
	Poolers []PoolerFacts
}

// PoolerStore holds the current poolers snapshot for concurrent readers.
type PoolerStore struct {
	mu   sync.RWMutex
	snap PoolersSnapshot
	has  bool
}

// NewPoolerStore returns an empty store.
func NewPoolerStore() *PoolerStore { return &PoolerStore{} }

// CurrentPoolers returns the snapshot and whether one exists.
func (s *PoolerStore) CurrentPoolers() (PoolersSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.has
}

// publish replaces the snapshot, advancing the generation and clearing
// staleness. The set is sorted and bounded here so no publication can
// bypass the bound.
func (s *PoolerStore) publish(poolers []PoolerFacts, observedAt time.Time, sourceTruncated bool) {
	sorted, cut := bounded(poolers, lessPoolerName, MaxPoolers)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = PoolersSnapshot{
		Generation: s.snap.Generation + 1,
		ObservedAt: observedAt,
		Truncated:  sourceTruncated || cut,
		Poolers:    sorted,
	}
	s.has = true
}

// markStale marks the retained snapshot stale, if one exists.
func (s *PoolerStore) markStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has {
		return
	}
	s.snap.Stale = true
}

// lessPoolerName orders the published set by name. A pooler is a
// standing piece of infrastructure rather than an event, so it has no
// meaningful recency to order by.
func lessPoolerName(a, b PoolerFacts) bool { return a.Name < b.Name }

// poolerRetention identifies retained poolers and bounds them at one
// above the rendered bound, the extra entry being the truncation
// sentinel. The lexically largest name loses, matching lessPoolerName so
// an evicted entry is one the page would have cut anyway.
var poolerRetention = retention[PoolerFacts]{
	Name:      func(p PoolerFacts) string { return p.Name },
	UID:       func(p PoolerFacts) string { return p.UID },
	Limit:     MaxPoolers + 1,
	Evictable: func(a, b PoolerFacts) bool { return a.Name > b.Name },
}

// PoolerCollector maintains the pooler store from a PoolerSource on the
// shared loop: seed, follow, republish per change, stale on contact
// loss, exponential backoff on failure.
type PoolerCollector struct {
	source    PoolerSource
	store     *PoolerStore
	clock     Clock
	logger    *slog.Logger
	state     keyed[PoolerFacts]
	truncated bool
}

// NewPoolerCollector wires a pooler collector onto a store.
func NewPoolerCollector(source PoolerSource, store *PoolerStore, clock Clock, logger *slog.Logger) *PoolerCollector {
	return &PoolerCollector{source: source, store: store, clock: clock, logger: logger}
}

// Run blocks until ctx is done, maintaining the store. Cancellation is
// the one clean exit.
func (c *PoolerCollector) Run(ctx context.Context) error {
	return newLoop[string, PoolerChange](c, c.clock, c.logger).Run(ctx)
}

// op names this resource in contact-loss logs.
func (c *PoolerCollector) op() string { return "poolers" }

// seed replaces the retained set and returns the resource version the
// watch resumes from.
func (c *PoolerCollector) seed(ctx context.Context) (string, error) {
	poolers, rv, truncated, err := c.source.FetchPoolers(ctx)
	if err != nil {
		return "", err
	}
	c.truncated = truncated
	c.state = make(keyed[PoolerFacts], len(poolers))
	for _, p := range poolers {
		if c.state.put(p, poolerRetention) {
			c.truncated = true
		}
	}
	return rv, nil
}

// follow starts the pooler watch from the seed's resource version.
func (c *PoolerCollector) follow(ctx context.Context, from string) (<-chan PoolerChange, func(), error) {
	w, err := c.source.WatchPoolers(ctx, from)
	if err != nil {
		return nil, nil, err
	}
	return w.Changes(), w.Stop, nil
}

// apply folds one change into the retained set. It reports whether the
// change was recognized; a change carrying nothing is not.
func (c *PoolerCollector) apply(change PoolerChange) bool {
	switch {
	case change.Put != nil:
		if c.state.put(*change.Put, poolerRetention) {
			c.truncated = true
		}
	case change.Delete != nil:
		c.state.remove(change.Delete.Name, change.Delete.UID, poolerRetention)
	default:
		return false
	}
	return true
}

// publish snapshots the retained set into the store.
func (c *PoolerCollector) publish(observedAt time.Time) {
	c.store.publish(c.state.list(), observedAt, c.truncated)
}

// markStale marks the retained snapshot stale, if one exists.
func (c *PoolerCollector) markStale() { c.store.markStale() }
