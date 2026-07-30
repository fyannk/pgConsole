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
	"sort"
	"sync"
	"time"

	"github.com/fyannk/pgConsole/internal/redact"
)

// MaxPods bounds the retained and rendered instance pods. A CloudNativePG
// cluster holds at most a few dozen; the bound exists so a mislabeled
// flood can never grow memory or the page without saying so.
const MaxPods = 500

// PodFacts is the Kubernetes-observed state of one instance pod. Empty
// strings and nil numerics are unreported facts and render as unknown.
type PodFacts struct {
	// Name is the pod name.
	Name string
	// UID distinguishes incarnations sharing a name across restarts.
	UID string
	// Role is the instance role label value, such as "primary".
	Role string
	// Phase is the pod phase.
	Phase string
	// Ready reports the pod Ready condition; nil when unreported.
	Ready *bool
	// Restarts is the summed container restart count; nil when
	// unreported.
	Restarts *int
	// Node is the assigned node name.
	Node string
	// Image is the PostgreSQL container image.
	Image string
	// Deleting reports a set deletion timestamp.
	Deleting bool
}

// PodDeletion identifies a removed pod incarnation. The UID guards
// against the name being reused by a newer incarnation: a deletion only
// removes the pod whose UID still matches.
type PodDeletion struct {
	// Name is the deleted pod's name.
	Name string
	// UID is the deleted incarnation.
	UID string
}

// PodEvent is one incremental change delivered by a pod watch. Exactly
// one field is set.
type PodEvent struct {
	// Put upserts one pod by name.
	Put *PodFacts
	// Delete removes one pod incarnation.
	Delete *PodDeletion
}

// PodWatch is a running watch on the cluster's pods. Events is closed
// when the watch ends for any reason; the collector then re-seeds with
// a fresh fetch. Stop releases the watch and must be safe to call after
// Events closed.
type PodWatch interface {
	// Events streams incremental changes until the watch ends.
	Events() <-chan PodEvent
	// Stop releases the watch.
	Stop()
}

// PodSource produces observations of the cluster's instance pods. The
// concrete implementation selects by the cluster's labels and verifies
// membership; the fake drives tests.
type PodSource interface {
	// FetchPods returns the complete current member set and the
	// resource version to resume watching from.
	FetchPods(ctx context.Context) (pods []PodFacts, resourceVersion string, err error)
	// WatchPods streams changes from the given resource version.
	WatchPods(ctx context.Context, fromResourceVersion string) (PodWatch, error)
}

// PodsSnapshot is the rendered pod set. It is immutable and carries its
// own staleness, independent of the cluster snapshot: sources fail
// independently.
type PodsSnapshot struct {
	// Generation increases by one on every publication.
	Generation uint64
	// ObservedAt is the time of the last successful contact.
	ObservedAt time.Time
	// Stale reports lost contact; the pod set below is the retained
	// last-good observation.
	Stale bool
	// Truncated reports that more than MaxPods matched and the set was
	// cut at the bound.
	Truncated bool
	// Pods is sorted by name and bounded by MaxPods.
	Pods []PodFacts
}

// PodStore holds the current pods snapshot for concurrent readers.
type PodStore struct {
	mu   sync.RWMutex
	snap PodsSnapshot
	has  bool
}

// NewPodStore returns an empty store.
func NewPodStore() *PodStore {
	return &PodStore{}
}

// CurrentPods returns the snapshot and whether one exists.
func (s *PodStore) CurrentPods() (PodsSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.has
}

// publish replaces the snapshot, advancing the generation and clearing
// staleness. The pod set is sorted and bounded here so no publication
// can bypass the bound.
func (s *PodStore) publish(pods []PodFacts, observedAt time.Time) {
	sorted := append([]PodFacts(nil), pods...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	truncated := false
	if len(sorted) > MaxPods {
		sorted = sorted[:MaxPods]
		truncated = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = PodsSnapshot{
		Generation: s.snap.Generation + 1,
		ObservedAt: observedAt,
		Truncated:  truncated,
		Pods:       sorted,
	}
	s.has = true
}

// markStale marks the retained snapshot stale, if one exists.
func (s *PodStore) markStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has {
		return
	}
	s.snap.Stale = true
}

// PodCollector maintains the pod store from a PodSource with the same
// contract as the cluster collector: seed, follow, republish per event,
// stale on contact loss, exponential backoff on failure.
type PodCollector struct {
	source  PodSource
	store   *PodStore
	clock   Clock
	logger  *slog.Logger
	backoff time.Duration
	state   map[string]PodFacts
}

// NewPodCollector wires a pod collector onto a store.
func NewPodCollector(source PodSource, store *PodStore, clock Clock, logger *slog.Logger) *PodCollector {
	return &PodCollector{
		source:  source,
		store:   store,
		clock:   clock,
		logger:  logger,
		backoff: backoffInitial,
	}
}

// Run blocks until ctx is done, maintaining the store. Cancellation is
// the one clean exit.
func (c *PodCollector) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		pods, rv, err := c.source.FetchPods(ctx)
		if err != nil {
			c.loseContact("pods fetch", err)
			if c.wait(ctx) != nil {
				return nil
			}
			continue
		}
		c.state = make(map[string]PodFacts, len(pods))
		for _, p := range pods {
			c.state[p.Name] = p
		}
		c.publish()

		w, err := c.source.WatchPods(ctx, rv)
		if err != nil {
			c.loseContact("pods watch start", err)
			if c.wait(ctx) != nil {
				return nil
			}
			continue
		}
		c.follow(ctx, w)
		if ctx.Err() != nil {
			return nil
		}
		c.loseContact("pods watch", nil)
		if c.wait(ctx) != nil {
			return nil
		}
	}
}

// follow consumes watch events until the watch ends or ctx is done,
// draining delivered events before honoring cancellation.
func (c *PodCollector) follow(ctx context.Context, w PodWatch) {
	defer w.Stop()
	for {
		select {
		case ev, ok := <-w.Events():
			if !ok {
				return
			}
			c.apply(ev)
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events():
			if !ok {
				return
			}
			c.apply(ev)
		}
	}
}

// apply folds one event into the state and republishes. A deletion
// whose UID no longer matches is ignored: the name was reused by a
// newer incarnation that must not be removed.
func (c *PodCollector) apply(ev PodEvent) {
	switch {
	case ev.Put != nil:
		c.state[ev.Put.Name] = *ev.Put
	case ev.Delete != nil:
		if current, ok := c.state[ev.Delete.Name]; ok && current.UID == ev.Delete.UID {
			delete(c.state, ev.Delete.Name)
		}
	default:
		return
	}
	c.publish()
}

// publish snapshots the current state and resets the backoff.
func (c *PodCollector) publish() {
	pods := make([]PodFacts, 0, len(c.state))
	for _, p := range c.state {
		pods = append(pods, p)
	}
	c.store.publish(pods, c.clock.Now())
	c.backoff = backoffInitial
}

// loseContact marks the snapshot stale and logs the failure category.
func (c *PodCollector) loseContact(op string, err error) {
	c.store.markStale()
	attrs := []any{slog.String("op", op)}
	if err != nil {
		attrs = append(attrs, slog.String("category", redact.Safe(err)))
	}
	c.logger.Info("contact lost", attrs...)
}

// wait sleeps the current backoff and doubles it up to the bound.
func (c *PodCollector) wait(ctx context.Context) error {
	d := c.backoff
	c.backoff *= 2
	if c.backoff > backoffMax {
		c.backoff = backoffMax
	}
	return c.clock.Wait(ctx, d)
}
