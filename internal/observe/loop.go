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

	"github.com/fyannk/pgConsole/internal/redact"
)

// feed is one observed resource seen from the collector loop: a complete
// seed, a watch resumed from that seed, and the fold of one watch item
// into retained state. C is what the seed hands the watch — a resource
// version for a source watching one kind, the whole seed state for a
// source merging several. E is one watch item.
//
// Implemented by every collector in this package: Collector (Cluster),
// PodCollector, EventCollector, BackupCollector and
// AccessReviewCollector. Every method is unexported, because these are
// the only implementations and this is package machinery rather than an
// extension point.
//
// What varies between resources lives here and nowhere else. The loop
// below carries no per-resource flag and must never gain one: the moment
// it branches on which resource it is driving, the point of having one
// loop is gone and five subtly different loops have been recreated
// inside one function.
type feed[C any, E any] interface {
	// op names the resource in contact-loss logs, as in "pods" or
	// "backup catalog". The loop appends the phase.
	op() string
	// seed replaces the retained state with a complete observation and
	// returns what the watch resumes from. It does not publish: the loop
	// publishes the seed, so a seed can never reach the store without
	// the generation bump and the backoff reset that belong with it.
	seed(ctx context.Context) (C, error)
	// follow starts the watch from the seed's cursor. The returned
	// channel is closed by the source when the watch ends for any
	// reason; stop releases it and is always called by the loop, exactly
	// once.
	//
	// It returns a channel and a stop function rather than an interface
	// because the watch types this wraps name their accessors
	// differently — Results, Events, Changes — and an interface would
	// buy five adapters of pure ceremony over one method value.
	follow(ctx context.Context, from C) (items <-chan E, stop func(), err error)
	// apply folds one watch item into the retained state and reports
	// whether the item was recognized.
	//
	// Recognized, not changed. Only an item carrying nothing — a change
	// union with no field set — is unrecognized. A deletion whose UID no
	// longer matches the retained incarnation is recognized: it leaves
	// the newer incarnation in place, but it is still an observation and
	// still advances the generation, exactly as it did before this loop
	// existed. Returning false there would silently freeze generations
	// on those events, and nothing in this package would catch it.
	apply(item E) bool
	// publish snapshots the retained state into the resource's store.
	publish(observedAt time.Time)
	// markStale marks the retained snapshot stale, if one exists.
	markStale()
}

// loop is the shared collector engine: seed, publish, follow, fold and
// republish per item, mark the snapshot stale on any contact loss, and
// retry with a bounded exponential backoff. Every collector in this
// package is this loop plus a feed.
type loop[C any, E any] struct {
	feed    feed[C, E]
	clock   Clock
	logger  *slog.Logger
	backoff time.Duration
}

// newLoop wires a loop onto a feed. C and E cannot be inferred from a
// concrete feed implementation, so call sites name them; that is the
// price of keeping the seed's cursor an explicit value passed to the
// watch instead of a field mutated between two calls.
func newLoop[C any, E any](f feed[C, E], clock Clock, logger *slog.Logger) *loop[C, E] {
	return &loop[C, E]{feed: f, clock: clock, logger: logger, backoff: backoffInitial}
}

// Run blocks until ctx is done, maintaining the feed's store. It always
// returns nil: cancellation is the one clean exit.
//
// The loop is built inside each collector's Run rather than in its
// constructor, so a collector assembled as a bare value — which the
// tests do — is still usable for its fold behaviour without one. Run is
// called once per process, from internal/application.
func (l *loop[C, E]) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		cursor, err := l.feed.seed(ctx)
		if err != nil {
			l.loseContact(l.feed.op()+" fetch", err)
			if l.wait(ctx) != nil {
				return nil
			}
			continue
		}
		l.published()

		items, stop, err := l.feed.follow(ctx, cursor)
		if err != nil {
			l.loseContact(l.feed.op()+" watch start", err)
			if l.wait(ctx) != nil {
				return nil
			}
			continue
		}
		l.consume(ctx, items, stop)
		if ctx.Err() != nil {
			return nil
		}
		// The watch ended without cancellation: contact is lost until
		// the next successful seed.
		l.loseContact(l.feed.op()+" watch", nil)
		if l.wait(ctx) != nil {
			return nil
		}
	}
}

// consume folds items until the watch ends or ctx is done.
// Already-delivered items are drained before cancellation is honored, so
// an observation the server sent is never dropped by shutdown timing.
func (l *loop[C, E]) consume(ctx context.Context, items <-chan E, stop func()) {
	defer stop()
	for {
		select {
		case item, ok := <-items:
			if !ok {
				return
			}
			if l.feed.apply(item) {
				l.published()
			}
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return
		case item, ok := <-items:
			if !ok {
				return
			}
			if l.feed.apply(item) {
				l.published()
			}
		}
	}
}

// published snapshots the feed and resets the backoff. It is the one
// place the loop reads the clock: exactly one Now per publication and
// none anywhere else, so a snapshot's ObservedAt is the publication time
// and nothing else.
func (l *loop[C, E]) published() {
	l.feed.publish(l.clock.Now())
	l.backoff = backoffInitial
}

// loseContact marks the snapshot stale and logs the category of the
// failure, never its text.
func (l *loop[C, E]) loseContact(op string, err error) {
	l.feed.markStale()
	attrs := []any{slog.String("op", op)}
	if err != nil {
		attrs = append(attrs, slog.String("category", redact.Safe(err)))
	}
	l.logger.Info("contact lost", attrs...)
}

// wait sleeps the current backoff and doubles it up to the bound.
func (l *loop[C, E]) wait(ctx context.Context) error {
	d := l.backoff
	l.backoff *= 2
	if l.backoff > backoffMax {
		l.backoff = backoffMax
	}
	return l.clock.Wait(ctx, d)
}
