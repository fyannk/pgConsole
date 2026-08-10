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

package logstream

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// scriptedSource hands out one stream per call, then errors.
type scriptedSource struct {
	members []Member

	mu      sync.Mutex
	streams []string
	opens   int
}

func (s *scriptedSource) Members() []Member { return s.members }

func (s *scriptedSource) Follow(context.Context, string, string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens++
	if len(s.streams) == 0 {
		return nil, errors.New("no more streams")
	}
	next := s.streams[0]
	s.streams = s.streams[1:]
	return io.NopCloser(strings.NewReader(next)), nil
}

func (s *scriptedSource) openCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opens
}

// recordingSink captures everything for assertions.
type recordingSink struct {
	mu    sync.Mutex
	lines []Line
	gaps  []string
}

func (r *recordingSink) Observe(line Line) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
}

func (r *recordingSink) Gap(_, _ string, _ time.Time, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gaps = append(r.gaps, reason)
}

func (r *recordingSink) snapshot() ([]Line, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Line(nil), r.lines...), append([]string(nil), r.gaps...)
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRunnerFollowsEachMemberAndSplitsLines proves the basic contract:
// one goroutine per container, each line delivered once with its origin.
func TestRunnerFollowsEachMemberAndSplitsLines(t *testing.T) {
	t.Parallel()
	source := &scriptedSource{
		members: []Member{{Pod: "orders-1", Container: "postgres"}},
		streams: []string{"first line\nsecond line\n"},
	}
	sink := &recordingSink{}
	runner := NewRunner(source, sink, fixedClock{base}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = runner.Run(ctx) }()

	waitFor(t, func() bool { lines, _ := sink.snapshot(); return len(lines) >= 2 })
	cancel()
	<-done

	lines, _ := sink.snapshot()
	if lines[0].Text != "first line" || lines[1].Text != "second line" {
		t.Errorf("lines not split as written: %+v", lines)
	}
	if lines[0].Pod != "orders-1" || lines[0].Container != "postgres" {
		t.Errorf("line lost its origin: %+v", lines[0])
	}
	if !lines[0].At.Equal(base) {
		t.Errorf("At = %v, want the console's clock", lines[0].At)
	}
}

// TestRunnerRecordsAGapOnReconnect is the honesty property of following
// a live stream. A stream ends on every container restart, and nothing
// can say what was emitted while disconnected — so the break is
// recorded rather than the two halves being joined silently.
func TestRunnerRecordsAGapOnReconnect(t *testing.T) {
	t.Parallel()
	source := &scriptedSource{
		members: []Member{{Pod: "orders-1", Container: "postgres"}},
		streams: []string{"before restart\n", "after restart\n"},
	}
	sink := &recordingSink{}
	runner := NewRunner(source, sink, fixedClock{base}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = runner.Run(ctx) }()

	waitFor(t, func() bool { _, gaps := sink.snapshot(); return len(gaps) >= 1 })
	cancel()
	<-done

	lines, gaps := sink.snapshot()
	if len(gaps) == 0 || !strings.Contains(gaps[0], "not observed") {
		t.Fatalf("reconnect did not record a gap: %+v", gaps)
	}
	// The first stream's content arrived before the gap; the gap is not
	// a substitute for the lines that did come through.
	if len(lines) == 0 || lines[0].Text != "before restart" {
		t.Errorf("lines before the gap were lost: %+v", lines)
	}
}

// TestRunnerStopsFollowingARemovedContainer proves the follower set
// tracks membership: a pod that is gone must not leave a goroutine
// reconnecting against it forever.
func TestRunnerStopsFollowingARemovedContainer(t *testing.T) {
	t.Parallel()
	source := &scriptedSource{
		members: []Member{{Pod: "orders-1", Container: "postgres"}},
		streams: []string{"only line\n"},
	}
	sink := &recordingSink{}
	runner := NewRunner(source, sink, fixedClock{base}, quietLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = runner.Run(ctx) }()
	waitFor(t, func() bool { lines, _ := sink.snapshot(); return len(lines) >= 1 })

	// The pod disappears; the next reconcile must stop its follower.
	source.mu.Lock()
	source.members = nil
	source.mu.Unlock()
	runner.reconcile(ctx)

	runner.mu.Lock()
	remaining := len(runner.running)
	runner.mu.Unlock()
	cancel()
	<-done

	if remaining != 0 {
		t.Errorf("followers still running for a removed pod: %d", remaining)
	}
}

// TestRunnerBacksOffRatherThanSpinning proves a container that cannot be
// followed does not become a reconnect loop against the API server.
func TestRunnerBacksOffRatherThanSpinning(t *testing.T) {
	t.Parallel()
	source := &scriptedSource{members: []Member{{Pod: "orders-1", Container: "postgres"}}}
	runner := NewRunner(source, &recordingSink{}, fixedClock{base}, quietLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = runner.Run(ctx)

	// With a two-second floor, a third of a second can afford one
	// attempt. Several would mean the backoff is not applied at all.
	if opens := source.openCount(); opens > 2 {
		t.Errorf("opened the stream %d times in 300ms; backoff is not holding", opens)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before the deadline")
}
