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

// Backoff bounds of the collector's retry loop. Failures back off
// exponentially so a persistent denial or outage never becomes a
// request storm; any success resets the backoff.
const (
	backoffInitial = time.Second
	backoffMax     = 30 * time.Second
)

// Collector maintains the store from a Source: it seeds with the pinned
// fetch, follows the watch, republishes on every event, and marks the
// retained snapshot stale whenever contact is lost. The loop doing all
// of that is shared (loop.go); what follows is only what is specific to
// the cluster resource.
//
// This is the singleton shape: the retained state is one value and a
// watch item replaces it wholesale. The collection shape, where retained
// state is a keyed set folded item by item, is PodCollector and its
// siblings.
type Collector struct {
	source Source
	store  *Store
	clock  Clock
	logger *slog.Logger
	facts  ClusterFacts
}

// NewCollector wires a collector onto a store.
func NewCollector(source Source, store *Store, clock Clock, logger *slog.Logger) *Collector {
	return &Collector{source: source, store: store, clock: clock, logger: logger}
}

// Run blocks until ctx is done, maintaining the store. It always returns
// nil: cancellation is the one clean exit.
func (c *Collector) Run(ctx context.Context) error {
	return newLoop[string, ClusterState](c, c.clock, c.logger).Run(ctx)
}

// op names this resource in contact-loss logs.
func (c *Collector) op() string { return "cluster" }

// seed takes the pinned get and returns the resource version the
// name-scoped watch resumes from. An absent cluster is a successful
// observation, not an error, so it seeds like any other.
func (c *Collector) seed(ctx context.Context) (string, error) {
	state, err := c.source.Fetch(ctx)
	if err != nil {
		return "", err
	}
	c.facts = state.Facts
	return state.ResourceVersion, nil
}

// follow starts the name-scoped watch from the seed's resource version.
func (c *Collector) follow(ctx context.Context, from string) (<-chan ClusterState, func(), error) {
	w, err := c.source.Watch(ctx, from)
	if err != nil {
		return nil, nil, err
	}
	return w.Results(), w.Stop, nil
}

// apply replaces the retained facts. A singleton watch delivers complete
// observations rather than deltas, so every item is recognized.
func (c *Collector) apply(state ClusterState) bool {
	c.facts = state.Facts
	return true
}

// publish snapshots the retained facts into the store.
func (c *Collector) publish(observedAt time.Time) { c.store.publish(c.facts, observedAt) }

// markStale marks the retained snapshot stale, if one exists.
func (c *Collector) markStale() { c.store.markStale() }
