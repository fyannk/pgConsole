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
	"time"
)

// PoolerPodSource produces the pods of the cluster's connection poolers.
//
// The facts are PodFacts and the snapshot is a PodsSnapshot: a pooler pod
// and an instance pod carry the same observations, so they share the
// types and the rendering. What differs is which pods are selected and
// how membership is proven, and that difference belongs in the adapter,
// not in a parallel set of near-identical structs.
type PoolerPodSource interface {
	// FetchPoolerPods returns the verified pooler pod set and the
	// resource version to resume watching from.
	FetchPoolerPods(ctx context.Context) (pods []PodFacts, resourceVersion string, err error)
	// WatchPoolerPods streams changes from the given resource version.
	WatchPoolerPods(ctx context.Context, fromResourceVersion string) (PodWatch, error)
}

// PoolerPodStore holds the pooler pod snapshot.
//
// It wraps a PodStore rather than being one, so the two cannot be wired
// into each other's place. They carry the same facts about different
// pods; a swap would compile, render plausibly, and put instance pods
// under a heading that says pooler.
type PoolerPodStore struct{ *PodStore }

// NewPoolerPodStore returns an empty store.
func NewPoolerPodStore() *PoolerPodStore { return &PoolerPodStore{PodStore: NewPodStore()} }

// CurrentPoolerPods returns the snapshot and whether one exists.
func (s *PoolerPodStore) CurrentPoolerPods() (PodsSnapshot, bool) { return s.CurrentPods() }

// PoolerPodCollector maintains a pooler pod store from a
// PoolerPodSource on the shared loop.
type PoolerPodCollector struct {
	source PoolerPodSource
	store  *PoolerPodStore
	clock  Clock
	logger *slog.Logger
	state  keyed[PodFacts]
}

// NewPoolerPodCollector wires a pooler pod collector onto a store.
func NewPoolerPodCollector(source PoolerPodSource, store *PoolerPodStore, clock Clock, logger *slog.Logger) *PoolerPodCollector {
	return &PoolerPodCollector{source: source, store: store, clock: clock, logger: logger}
}

// Run blocks until ctx is done, maintaining the store.
func (c *PoolerPodCollector) Run(ctx context.Context) error {
	return newLoop[string, PodEvent](c, c.clock, c.logger).Run(ctx)
}

// op names this resource in contact-loss logs.
func (c *PoolerPodCollector) op() string { return "pooler pods" }

// seed replaces the retained set and returns the watch cursor.
func (c *PoolerPodCollector) seed(ctx context.Context) (string, error) {
	pods, rv, err := c.source.FetchPoolerPods(ctx)
	if err != nil {
		return "", err
	}
	c.state = make(keyed[PodFacts], len(pods))
	for _, p := range pods {
		c.state.put(p, podRetention)
	}
	return rv, nil
}

// follow starts the pooler pod watch from the seed's resource version.
func (c *PoolerPodCollector) follow(ctx context.Context, from string) (<-chan PodEvent, func(), error) {
	w, err := c.source.WatchPoolerPods(ctx, from)
	if err != nil {
		return nil, nil, err
	}
	return w.Events(), w.Stop, nil
}

// apply folds one event into the retained set.
func (c *PoolerPodCollector) apply(ev PodEvent) bool {
	switch {
	case ev.Put != nil:
		c.state.put(*ev.Put, podRetention)
	case ev.Delete != nil:
		c.state.remove(ev.Delete.Name, ev.Delete.UID, podRetention)
	default:
		return false
	}
	return true
}

// publish snapshots the retained set into the store.
func (c *PoolerPodCollector) publish(observedAt time.Time) {
	c.store.publish(c.state.list(), observedAt)
}

// markStale marks the retained snapshot stale, if one exists.
func (c *PoolerPodCollector) markStale() { c.store.markStale() }
