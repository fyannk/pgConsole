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
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeClock records waits and returns instantly, so poller schedules
// are observable without real time.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Wait(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.waits = append(c.waits, d)
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return ctx.Err()
}

func (c *fakeClock) recorded() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

// scriptedFetcher returns the scripted results in order, then cancels
// the run. Backup assemblies are scripted separately and consumed one
// per call.
type scriptedFetcher struct {
	results     []func() (Report, error)
	pages       []func() ([]RepoBackup, bool, error)
	cancel      context.CancelFunc
	calls       int
	pageCalls   int
	pageQueries []uint64
}

func (f *scriptedFetcher) FetchSnapshot(context.Context) (Report, error) {
	if f.calls >= len(f.results) {
		f.cancel()
		return Report{}, context.Canceled
	}
	result := f.results[f.calls]
	f.calls++
	return result()
}

func (f *scriptedFetcher) FetchBackups(_ context.Context, _, generation uint64) ([]RepoBackup, bool, error) {
	f.pageQueries = append(f.pageQueries, generation)
	if f.pageCalls >= len(f.pages) {
		return nil, false, nil
	}
	result := f.pages[f.pageCalls]
	f.pageCalls++
	return result()
}

func runPoller(t *testing.T, results []func() (Report, error)) (*Store, *fakeClock) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fetcher := &scriptedFetcher{results: results, cancel: cancel}
	store := NewStore()
	clock := newFakeClock()
	poller := NewPoller(fetcher, store, clock, slog.New(slog.DiscardHandler))
	poller.jitter = func(time.Duration) time.Duration { return time.Second }
	if err := poller.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return store, clock
}

func TestPollerPublishesAndPacesWithJitter(t *testing.T) {
	t.Parallel()
	ok := func() (Report, error) { return Report{Fingerprint: "sha256:aa"}, nil }
	store, clock := runPoller(t, []func() (Report, error){ok, ok})

	status := store.CurrentEvidence()
	if !status.HasReport || status.Snapshot.Generation != 2 || status.Snapshot.Stale {
		t.Fatalf("status = %+v", status)
	}
	waits := clock.recorded()
	if len(waits) != 2 || waits[0] != pollInterval+time.Second || waits[1] != pollInterval+time.Second {
		t.Errorf("waits = %v", waits)
	}
}

func TestPollerFailureBacksOffAndRetainsStale(t *testing.T) {
	t.Parallel()
	ok := func() (Report, error) { return Report{Fingerprint: "sha256:aa"}, nil }
	failed := func() (Report, error) { return Report{}, fail(FailureUnavailable, nil) }
	store, clock := runPoller(t, []func() (Report, error){ok, failed, failed, failed})

	status := store.CurrentEvidence()
	if !status.HasReport || !status.Snapshot.Stale || status.Failure != FailureUnavailable {
		t.Fatalf("status = %+v", status)
	}
	if status.Snapshot.Report.Fingerprint != "sha256:aa" {
		t.Error("stale retention lost the last-good report")
	}
	waits := clock.recorded()
	want := []time.Duration{pollInterval + time.Second, pollBackoffInitial, 2 * pollBackoffInitial, 4 * pollBackoffInitial}
	if len(waits) != len(want) {
		t.Fatalf("waits = %v", waits)
	}
	for index, wait := range want {
		if waits[index] != wait {
			t.Errorf("wait[%d] = %v, want %v", index, waits[index], wait)
		}
	}
}

func TestPollerSuccessResetsBackoff(t *testing.T) {
	t.Parallel()
	ok := func() (Report, error) { return Report{}, nil }
	failed := func() (Report, error) { return Report{}, fail(FailureBusy, nil) }
	_, clock := runPoller(t, []func() (Report, error){failed, failed, ok, failed})

	waits := clock.recorded()
	want := []time.Duration{pollBackoffInitial, 2 * pollBackoffInitial, pollInterval + time.Second, pollBackoffInitial}
	for index, wait := range want {
		if waits[index] != wait {
			t.Errorf("wait[%d] = %v, want %v", index, waits[index], wait)
		}
	}
}

func TestPollerBackoffIsCapped(t *testing.T) {
	t.Parallel()
	failed := func() (Report, error) { return Report{}, fail(FailureUnavailable, nil) }
	var results []func() (Report, error)
	for range 8 {
		results = append(results, failed)
	}
	_, clock := runPoller(t, results)

	waits := clock.recorded()
	if last := waits[len(waits)-1]; last != pollBackoffMax {
		t.Errorf("final backoff = %v, want %v", last, pollBackoffMax)
	}
}

func TestPollerShutdownCancellationIsNotContactLoss(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	fetcher := &scriptedFetcher{cancel: cancel}
	store := NewStore()
	poller := NewPoller(fetcher, store, newFakeClock(), slog.New(slog.DiscardHandler))
	if err := poller.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status := store.CurrentEvidence(); status.Failure != FailureNone {
		t.Errorf("shutdown recorded a failure: %+v", status)
	}
}

func TestPollerAssemblesBackupsAndSkipsUnchangedGeneration(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	withEvidence := func() (Report, error) { return Report{Revision: 5, EvidenceGeneration: 3}, nil }
	revisionBump := func() (Report, error) { return Report{Revision: 6, EvidenceGeneration: 3}, nil }
	fetcher := &scriptedFetcher{
		results: []func() (Report, error){withEvidence, revisionBump},
		pages: []func() ([]RepoBackup, bool, error){
			func() ([]RepoBackup, bool, error) {
				return []RepoBackup{{Server: "orders", BackupID: "b1"}}, false, nil
			},
		},
		cancel: cancel,
	}
	store := NewStore()
	poller := NewPoller(fetcher, store, newFakeClock(), slog.New(slog.DiscardHandler))
	poller.jitter = func(time.Duration) time.Duration { return 0 }
	if err := poller.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	status := store.CurrentEvidence()
	if !status.HasReport || len(status.Snapshot.Backups) != 1 || status.Snapshot.Backups[0].BackupID != "b1" {
		t.Fatalf("assembled publication = %+v", status.Snapshot)
	}
	if len(fetcher.pageQueries) != 1 {
		t.Errorf("page fetches = %v, want one: an unchanged generation reuses the assembly", fetcher.pageQueries)
	}
	if status.Snapshot.Report.Revision != 6 {
		t.Errorf("revision-only bump not republished: %+v", status.Snapshot.Report)
	}
}

func TestPollerPageFailureIsAtomicAndRetainsAssembly(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	generationThree := func() (Report, error) { return Report{Revision: 5, EvidenceGeneration: 3}, nil }
	generationFour := func() (Report, error) { return Report{Revision: 7, EvidenceGeneration: 4}, nil }
	fetcher := &scriptedFetcher{
		results: []func() (Report, error){generationThree, generationFour},
		pages: []func() ([]RepoBackup, bool, error){
			func() ([]RepoBackup, bool, error) {
				return []RepoBackup{{Server: "orders", BackupID: "b1"}}, false, nil
			},
			func() ([]RepoBackup, bool, error) { return nil, false, fail(FailurePublicationChanged, nil) },
		},
		cancel: cancel,
	}
	store := NewStore()
	poller := NewPoller(fetcher, store, newFakeClock(), slog.New(slog.DiscardHandler))
	poller.jitter = func(time.Duration) time.Duration { return 0 }
	if err := poller.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	status := store.CurrentEvidence()
	if !status.HasReport || !status.Snapshot.Stale || status.Failure != FailurePublicationChanged {
		t.Fatalf("status = %+v", status)
	}
	if status.Snapshot.Report.EvidenceGeneration != 3 || len(status.Snapshot.Backups) != 1 {
		t.Error("partial assembly leaked: the retained publication must stay the last atomic one")
	}
}
