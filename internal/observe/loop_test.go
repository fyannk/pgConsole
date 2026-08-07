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
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// probeFeed is a feed with no resource behind it, so the loop's own
// contract can be asserted without a store, a source or a conversion in
// the way. Every collector in this package delegates to the same loop,
// so what holds here holds for all of them.
type probeFeed struct {
	mu sync.Mutex
	// seedErr fails every seed when set. Sticky rather than scripted:
	// the backoff assertions need a failure that never resolves.
	seedErr error
	// streams is consumed one per follow call.
	streams []chan int
	// unrecognized are the items apply reports as carrying nothing.
	unrecognized map[int]bool

	seedCalls   int
	applied     []int
	publishedAt []time.Time
	staleCalls  int
}

// Asserted for the same reason the real feeds are in loop.go: these
// methods are reached only through a type parameter, so nothing else
// tells the analysis they are live.
var _ feed[string, int] = (*probeFeed)(nil)

func (p *probeFeed) op() string { return "probe" }

func (p *probeFeed) seed(context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seedCalls++
	if p.seedErr != nil {
		return "", p.seedErr
	}
	return "rv", nil
}

func (p *probeFeed) follow(_ context.Context, _ string) (<-chan int, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	i := p.seedCalls - 1
	if i < 0 || i >= len(p.streams) {
		// Nothing scripted: an already-closed stream ends this pass
		// without blocking.
		closed := make(chan int)
		close(closed)
		return closed, func() {}, nil
	}
	return p.streams[i], func() {}, nil
}

func (p *probeFeed) apply(item int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.applied = append(p.applied, item)
	return !p.unrecognized[item]
}

func (p *probeFeed) publish(at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publishedAt = append(p.publishedAt, at)
}

func (p *probeFeed) markStale() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.staleCalls++
}

func (p *probeFeed) publications() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.publishedAt)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// TestLoopPublishesTheSeedBeforeFollowing proves the seed reaches the
// store on its own. A collector that published only on watch items would
// render nothing at all for a resource that never changes.
func TestLoopPublishesTheSeedBeforeFollowing(t *testing.T) {
	t.Parallel()
	feed := &probeFeed{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newLoop[string, int](feed, newFakeClock(), quietLogger()).Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Cancelled before entry: nothing observed, nothing published.
	if feed.publications() != 0 {
		t.Errorf("published %d times under an already-cancelled context", feed.publications())
	}

	// A watch that stays open holds the loop inside consume, so the one
	// publication that exists can only be the seed's.
	feed = &probeFeed{}
	open := make(chan int)
	feed.streams = []chan int{open}
	ctx, cancel = context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = newLoop[string, int](feed, newFakeClock(), quietLogger()).Run(ctx)
		close(done)
	}()
	waitFor(t, func() bool { return feed.publications() == 1 })
	cancel()
	close(open)
	<-done
}

// TestLoopPublishesOncePerRecognizedItem proves the generation advances
// for every observation the server sent and for nothing else. An item
// carrying nothing must not advance it; a recognized item must, even
// when it changed no retained value, because a UID-mismatched deletion
// is still an observation.
func TestLoopPublishesOncePerRecognizedItem(t *testing.T) {
	t.Parallel()
	stream := make(chan int, 3)
	stream <- 1
	stream <- 2 // unrecognized
	stream <- 3
	close(stream)

	feed := &probeFeed{unrecognized: map[int]bool{2: true}}
	// consume directly, not Run: Run re-seeds once a stream closes, and
	// each re-seed publishes again, so a count taken through Run would
	// be a race against the retry loop rather than a statement about
	// items.
	clock := newFakeClock()
	newLoop[string, int](feed, clock, quietLogger()).
		consume(context.Background(), stream, func() {})

	feed.mu.Lock()
	defer feed.mu.Unlock()
	if len(feed.publishedAt) != 2 {
		t.Errorf("published %d times for three items, one of them carrying nothing; want 2", len(feed.publishedAt))
	}
	if len(feed.applied) != 3 {
		t.Errorf("applied %v, want all three items folded", feed.applied)
	}
	// Exactly one clock read per publication and none anywhere else.
	// Asserted as a total count, not as gaps between stamps: a stray
	// read outside the per-item path shifts every stamp equally and
	// leaves the gaps at one second, so gaps alone would not catch it.
	// Every ObservedAt assertion in this package is implicitly a
	// clock-call-count assertion, which is why this one is explicit.
	if reads := clock.reads(); reads != len(feed.publishedAt) {
		t.Errorf("the loop read the clock %d times for %d publications; it must read it once per publication and never elsewhere", reads, len(feed.publishedAt))
	}
}

// TestLoopDrainsDeliveredItemsBeforeCancelling proves shutdown timing
// never drops an observation the server already sent.
func TestLoopDrainsDeliveredItemsBeforeCancelling(t *testing.T) {
	t.Parallel()
	stream := make(chan int, 2)
	stream <- 7
	stream <- 8

	feed := &probeFeed{}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancelled up front: the drain-first select must still consume both
	// buffered items before honoring it. Asserted against consume rather
	// than Run, because Run returns on a cancelled context before it
	// ever reaches a watch.
	cancel()
	go func() {
		time.Sleep(10 * time.Millisecond)
		close(stream)
	}()
	newLoop[string, int](feed, newFakeClock(), quietLogger()).consume(ctx, stream, func() {})

	feed.mu.Lock()
	defer feed.mu.Unlock()
	if len(feed.applied) != 2 {
		t.Errorf("applied %v, want both buffered items drained before cancellation", feed.applied)
	}
}

// TestLoopMarksStaleAndBacksOffToTheBound proves a persistent failure
// retains the last-good snapshot, marks it stale on every attempt, and
// never becomes a request storm.
func TestLoopMarksStaleAndBacksOffToTheBound(t *testing.T) {
	t.Parallel()
	feed := &probeFeed{seedErr: errors.New("denied")}
	clock := newFakeClock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = newLoop[string, int](feed, clock, quietLogger()).Run(ctx)
		close(done)
	}()
	waitFor(t, func() bool {
		clock.mu.Lock()
		defer clock.mu.Unlock()
		return len(clock.waits) >= 10
	})
	cancel()
	<-done

	clock.mu.Lock()
	defer clock.mu.Unlock()
	if clock.waits[0] != backoffInitial {
		t.Errorf("first wait %s, want %s", clock.waits[0], backoffInitial)
	}
	for i, d := range clock.waits {
		if d > backoffMax {
			t.Fatalf("wait %d is %s, past the %s bound", i, d, backoffMax)
		}
	}
	if last := clock.waits[len(clock.waits)-1]; last != backoffMax {
		t.Errorf("last wait %s, want the backoff to have reached %s", last, backoffMax)
	}

	feed.mu.Lock()
	defer feed.mu.Unlock()
	if feed.staleCalls < 10 {
		t.Errorf("markStale called %d times, want one per failed attempt", feed.staleCalls)
	}
	if len(feed.publishedAt) != 0 {
		t.Error("a failing seed published")
	}
}

// TestLoopReseedsAfterTheWatchEnds proves a watch that ends without
// cancellation is contact loss followed by a fresh seed, not a silent
// stop.
func TestLoopReseedsAfterTheWatchEnds(t *testing.T) {
	t.Parallel()
	first := make(chan int)
	close(first)
	second := make(chan int)

	feed := &probeFeed{streams: []chan int{first, second}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = newLoop[string, int](feed, newFakeClock(), quietLogger()).Run(ctx)
		close(done)
	}()
	waitFor(t, func() bool {
		feed.mu.Lock()
		defer feed.mu.Unlock()
		return feed.seedCalls >= 2
	})
	cancel()
	close(second)
	<-done

	feed.mu.Lock()
	defer feed.mu.Unlock()
	if feed.staleCalls == 0 {
		t.Error("a watch ending without cancellation did not mark the snapshot stale")
	}
}

// waitFor polls cond until it holds or the test times out, so the
// assertions above never depend on goroutine scheduling.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never held")
}
