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

// MaxQuotas bounds the retained ResourceQuota set. A namespace rarely
// carries more than a handful.
const MaxQuotas = 16

// QuotaResourceFacts is one resource line of a ResourceQuota: the
// declared ceiling, the API server's reported usage, and whether the
// ceiling is reached — computed at the observation boundary, where the
// quantities can still be compared as quantities.
type QuotaResourceFacts struct {
	// Resource is the quota key, such as "requests.storage" or "pods".
	Resource string
	// Hard is the declared ceiling, as the API server states it.
	Hard string
	// Used is the reported usage, as the API server states it.
	Used string
	// Exhausted reports used >= hard. It is derived at conversion from
	// the two quantities above, which the reader can check against it.
	Exhausted bool
}

// QuotaFacts is the Kubernetes-reported state of one ResourceQuota.
type QuotaFacts struct {
	// Name is the resource name.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// Resources are the quota's lines, sorted by resource name and
	// bounded by the source.
	Resources []QuotaResourceFacts
}

// QuotaDeletion identifies a removed quota incarnation.
type QuotaDeletion struct {
	Name string
	UID  string
}

// QuotaChange is one change from the quota watch. Exactly one field is
// set.
type QuotaChange struct {
	Put    *QuotaFacts
	Delete *QuotaDeletion
}

// QuotaWatch is a running watch on the namespace's quotas.
type QuotaWatch interface {
	Changes() <-chan QuotaChange
	Stop()
}

// QuotasState is one complete seed.
type QuotasState struct {
	Quotas          []QuotaFacts
	ResourceVersion string
}

// QuotaSource produces the namespace's ResourceQuota objects. Like the
// image catalogs, quotas are namespace infrastructure with no cluster
// ownership to filter on: the whole namespaced set is the honest scope,
// because any quota in the namespace can refuse this cluster's objects.
type QuotaSource interface {
	FetchQuotas(ctx context.Context) (QuotasState, error)
	WatchQuotas(ctx context.Context, fromResourceVersion string) (QuotaWatch, error)
}

// QuotasSnapshot is the rendered quota set, immutable and carrying its
// own staleness.
type QuotasSnapshot struct {
	// Generation increases by one on every publication.
	Generation uint64
	// ObservedAt is the time of the last successful contact.
	ObservedAt time.Time
	// Stale reports lost contact.
	Stale bool
	// Quotas is sorted by name and bounded by MaxQuotas.
	Quotas []QuotaFacts
}

// QuotaStore holds the current quota snapshot for concurrent readers.
type QuotaStore struct {
	mu   sync.RWMutex
	snap QuotasSnapshot
	has  bool
}

// NewQuotaStore returns an empty store.
func NewQuotaStore() *QuotaStore { return &QuotaStore{} }

// CurrentQuotas returns the snapshot and whether one exists.
func (s *QuotaStore) CurrentQuotas() (QuotasSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.has
}

func (s *QuotaStore) publish(quotas []QuotaFacts, observedAt time.Time) {
	sorted, _ := bounded(quotas, lessQuotaName, MaxQuotas)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = QuotasSnapshot{
		Generation: s.snap.Generation + 1,
		ObservedAt: observedAt,
		Quotas:     sorted,
	}
	s.has = true
}

func (s *QuotaStore) markStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has {
		return
	}
	s.snap.Stale = true
}

// lessQuotaName orders quotas by name; a quota is standing
// configuration with no meaningful recency.
func lessQuotaName(a, b QuotaFacts) bool { return a.Name < b.Name }

// quotaRetention identifies retained quotas; the lexically largest
// name loses, matching lessQuotaName.
var quotaRetention = retention[QuotaFacts]{
	Name:      func(q QuotaFacts) string { return q.Name },
	UID:       func(q QuotaFacts) string { return q.UID },
	Limit:     MaxQuotas + 1,
	Evictable: func(a, b QuotaFacts) bool { return a.Name > b.Name },
}

// QuotaCollector maintains the quota store on the shared loop.
type QuotaCollector struct {
	source QuotaSource
	store  *QuotaStore
	clock  Clock
	logger *slog.Logger
	state  keyed[QuotaFacts]
}

// NewQuotaCollector wires a quota collector onto a store.
func NewQuotaCollector(source QuotaSource, store *QuotaStore, clock Clock, logger *slog.Logger) *QuotaCollector {
	return &QuotaCollector{source: source, store: store, clock: clock, logger: logger}
}

// Run blocks until ctx is done, maintaining the store.
func (c *QuotaCollector) Run(ctx context.Context) error {
	return newLoop[string, QuotaChange](c, c.clock, c.logger).Run(ctx)
}

func (c *QuotaCollector) op() string { return "resource quotas" }

func (c *QuotaCollector) seed(ctx context.Context) (string, error) {
	state, err := c.source.FetchQuotas(ctx)
	if err != nil {
		return "", err
	}
	c.state = make(keyed[QuotaFacts], len(state.Quotas))
	for _, quota := range state.Quotas {
		c.state.put(quota, quotaRetention)
	}
	return state.ResourceVersion, nil
}

func (c *QuotaCollector) follow(ctx context.Context, from string) (<-chan QuotaChange, func(), error) {
	w, err := c.source.WatchQuotas(ctx, from)
	if err != nil {
		return nil, nil, err
	}
	return w.Changes(), w.Stop, nil
}

func (c *QuotaCollector) apply(change QuotaChange) bool {
	switch {
	case change.Put != nil:
		c.state.put(*change.Put, quotaRetention)
	case change.Delete != nil:
		c.state.remove(change.Delete.Name, change.Delete.UID, quotaRetention)
	default:
		return false
	}
	return true
}

func (c *QuotaCollector) publish(observedAt time.Time) {
	c.store.publish(c.state.list(), observedAt)
}

func (c *QuotaCollector) markStale() { c.store.markStale() }
