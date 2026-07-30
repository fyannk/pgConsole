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
	"log/slog"
	"time"

	"context"

	"github.com/fyannk/pgConsole/internal/redact"
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
// retained snapshot stale whenever contact is lost.
type Collector struct {
	source  Source
	store   *Store
	clock   Clock
	logger  *slog.Logger
	backoff time.Duration
}

// NewCollector wires a collector onto a store.
func NewCollector(source Source, store *Store, clock Clock, logger *slog.Logger) *Collector {
	return &Collector{
		source:  source,
		store:   store,
		clock:   clock,
		logger:  logger,
		backoff: backoffInitial,
	}
}

// Run blocks until ctx is done, maintaining the store. It always
// returns ctx.Err's nil-normalized form: cancellation is the one clean
// exit.
func (c *Collector) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		state, err := c.source.Fetch(ctx)
		if err != nil {
			c.loseContact("fetch", err)
			if c.wait(ctx) != nil {
				return nil
			}
			continue
		}
		c.observed(state)

		w, err := c.source.Watch(ctx, state.ResourceVersion)
		if err != nil {
			c.loseContact("watch start", err)
			if c.wait(ctx) != nil {
				return nil
			}
			continue
		}
		c.follow(ctx, w)
		if ctx.Err() != nil {
			return nil
		}
		// The watch ended without cancellation: contact is lost until
		// the next successful fetch.
		c.loseContact("watch", nil)
		if c.wait(ctx) != nil {
			return nil
		}
	}
}

// follow consumes watch results until the watch ends or ctx is done.
// Already-delivered results are drained before cancellation is honored,
// so an observation the server sent is never dropped by shutdown timing.
func (c *Collector) follow(ctx context.Context, w Watch) {
	defer w.Stop()
	for {
		select {
		case state, ok := <-w.Results():
			if !ok {
				return
			}
			c.observed(state)
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return
		case state, ok := <-w.Results():
			if !ok {
				return
			}
			c.observed(state)
		}
	}
}

// observed publishes a successful observation and resets the backoff.
func (c *Collector) observed(state ClusterState) {
	c.store.publish(state.Facts, c.clock.Now())
	c.backoff = backoffInitial
}

// loseContact marks the snapshot stale and logs the category of the
// failure, never its text.
func (c *Collector) loseContact(op string, err error) {
	c.store.markStale()
	attrs := []any{slog.String("op", op)}
	if err != nil {
		attrs = append(attrs, slog.String("category", redact.Safe(err)))
	}
	c.logger.Info("contact lost", attrs...)
}

// wait sleeps the current backoff and doubles it up to the bound.
func (c *Collector) wait(ctx context.Context) error {
	d := c.backoff
	c.backoff *= 2
	if c.backoff > backoffMax {
		c.backoff = backoffMax
	}
	return c.clock.Wait(ctx, d)
}
