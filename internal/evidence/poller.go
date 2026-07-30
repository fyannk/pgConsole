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

package evidence

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"log/slog"
	"time"

	"github.com/fyannk/pgconsole/internal/observe"
	"github.com/fyannk/pgconsole/internal/redact"
)

// Polling bounds. Successes wait the interval plus bounded jitter;
// failures back off exponentially so a stopped sidecar never becomes a
// request storm against the shared socket.
const (
	// pollInterval separates successful snapshot polls.
	pollInterval = 15 * time.Second
	// pollJitterMax bounds the added per-poll jitter.
	pollJitterMax = 3 * time.Second
	// pollBackoffInitial is the first failure delay.
	pollBackoffInitial = time.Second
	// pollBackoffMax caps the failure delay.
	pollBackoffMax = 30 * time.Second
)

// Fetcher performs the bounded evidence fetches. The concrete Client
// implements it; the fake drives tests.
type Fetcher interface {
	// FetchSnapshot returns the validated projection or a kind-carrying
	// error.
	FetchSnapshot(ctx context.Context) (Report, error)
	// FetchBackups assembles the backup collection of one publication
	// identity, reporting consumer-side truncation.
	FetchBackups(ctx context.Context, revision, generation uint64) ([]RepoBackup, bool, error)
}

// Poller maintains the store from a Fetcher: it polls the sidecar's
// snapshot route in the background, publishes validated projections,
// and marks the retained report stale with the failure kind whenever a
// poll fails. Browser requests never reach the sidecar; they read the
// store.
type Poller struct {
	fetcher Fetcher
	store   *Store
	clock   observe.Clock
	logger  *slog.Logger
	backoff time.Duration
	jitter  func(time.Duration) time.Duration

	// Last assembled collection, reused while the evidence generation
	// is unchanged: pages derive from the immutable generation, so a
	// revision-only bump (a failed sidecar refresh) re-reads nothing.
	assembled           bool
	assembledGeneration uint64
	backups             []RepoBackup
	backupsTruncated    bool
}

// NewPoller wires a poller onto a store.
func NewPoller(fetcher Fetcher, store *Store, clock observe.Clock, logger *slog.Logger) *Poller {
	return &Poller{
		fetcher: fetcher,
		store:   store,
		clock:   clock,
		logger:  logger,
		backoff: pollBackoffInitial,
		jitter:  randomJitter,
	}
}

// randomJitter returns a uniform duration in [0, maximum). A failed
// entropy read degrades to half the bound; jitter is pacing, not a
// security property.
func randomJitter(maximum time.Duration) time.Duration {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return maximum / 2
	}
	return time.Duration(binary.LittleEndian.Uint64(buf[:]) % uint64(maximum)) //nolint:gosec // maximum is a small positive constant; the conversion cannot overflow.
}

// Run blocks until ctx is done, maintaining the store. Cancellation is
// the one clean exit; a cancellation-kind failure during shutdown is
// not recorded as lost contact.
func (p *Poller) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		report, err := p.fetcher.FetchSnapshot(ctx)
		if err == nil {
			err = p.assemble(ctx, report)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			kind := KindOf(err)
			p.store.markFailed(kind)
			p.logger.Info("evidence contact lost",
				slog.String("kind", string(kind)),
				slog.String("category", redact.Safe(err)))
			if p.wait(ctx, p.nextBackoff()) != nil {
				return nil
			}
			continue
		}
		p.store.publish(report, p.backups, p.backupsTruncated, p.clock.Now())
		p.backoff = pollBackoffInitial
		if p.wait(ctx, pollInterval+p.jitter(pollJitterMax)) != nil {
			return nil
		}
	}
}

// assemble refreshes the backup collection for the report's
// publication identity. A generation of zero means no complete scan —
// no items exist to fetch. An unchanged generation reuses the previous
// assembly: pages derive from the immutable generation. Nothing is
// published on failure, so a report and its collection always land
// atomically or not at all.
func (p *Poller) assemble(ctx context.Context, report Report) error {
	if report.EvidenceGeneration == 0 {
		p.assembled, p.assembledGeneration, p.backups, p.backupsTruncated = true, 0, nil, false
		return nil
	}
	if p.assembled && p.assembledGeneration == report.EvidenceGeneration {
		return nil
	}
	backups, truncated, err := p.fetcher.FetchBackups(ctx, report.Revision, report.EvidenceGeneration)
	if err != nil {
		return err
	}
	p.assembled, p.assembledGeneration, p.backups, p.backupsTruncated = true, report.EvidenceGeneration, backups, truncated
	return nil
}

// nextBackoff returns the current failure delay and doubles it up to
// the bound.
func (p *Poller) nextBackoff() time.Duration {
	d := p.backoff
	p.backoff *= 2
	if p.backoff > pollBackoffMax {
		p.backoff = pollBackoffMax
	}
	return d
}

// wait sleeps for d or until ctx is done.
func (p *Poller) wait(ctx context.Context, d time.Duration) error {
	return p.clock.Wait(ctx, d)
}
