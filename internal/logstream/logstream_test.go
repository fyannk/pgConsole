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
	"strings"
	"sync"
	"testing"
	"time"
)

var base = time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

func line(pod, container, text string, offset time.Duration) Line {
	return Line{Pod: pod, Container: container, Text: text, At: base.Add(offset)}
}

var archiveRule = Rule{
	ID:       "wal-archive-not-empty",
	Contains: []string{"WAL archive check failed", "Expected empty archive"},
	Summary:  "The configured WAL archive is not empty.",
}

// TestMatcherExpiresQuietObservations proves a finding cannot outlive
// its relevance: an observation nothing has re-matched within the
// window is dropped, while one whose lines keep coming stays, its
// original first-seen intact.
func TestMatcherExpiresQuietObservations(t *testing.T) {
	t.Parallel()
	current := base
	m := NewMatcher([]Rule{archiveRule}, time.Hour, func() time.Time { return current })

	m.Observe(line("orders-1", "postgres",
		"ERROR: WAL archive check failed for server orders: Expected empty archive", 0))
	if len(m.Observations()) != 1 {
		t.Fatal("fresh observation not retained")
	}

	// The same failure recurs fifty minutes in; the observation renews.
	m.Observe(line("orders-1", "postgres",
		"ERROR: WAL archive check failed for server orders: Expected empty archive", 50*time.Minute))
	current = base.Add(100 * time.Minute)
	observations := m.Observations()
	if len(observations) != 1 {
		t.Fatalf("recurring observation expired: %+v", observations)
	}
	if !observations[0].FirstSeen.Equal(base) || observations[0].Count != 2 {
		t.Errorf("renewal rewrote the history: %+v", observations[0])
	}

	// An hour of silence after the last match, and the claim is stale:
	// it is dropped entirely rather than shown as if current.
	current = base.Add(50*time.Minute + 61*time.Minute)
	if remaining := m.Observations(); len(remaining) != 0 {
		t.Errorf("quiet observation survived its window: %+v", remaining)
	}
}

// TestMatcherKeepsTheFindingNotTheLines is the property that makes
// continuous streaming affordable: however much is logged, the matcher
// retains one observation per rule per container and nothing else.
func TestMatcherKeepsTheFindingNotTheLines(t *testing.T) {
	t.Parallel()
	m := NewMatcher([]Rule{archiveRule}, 0, nil)
	for i := range 1000 {
		m.Observe(line("orders-1", "postgres", "ordinary chatter", time.Duration(i)*time.Second))
	}
	if len(m.Observations()) != 0 {
		t.Fatalf("unmatched lines were retained: %+v", m.Observations())
	}

	for i := range 50 {
		m.Observe(line("orders-1", "postgres",
			"ERROR: WAL archive check failed for server orders: Expected empty archive",
			time.Duration(i)*time.Second))
	}
	observations := m.Observations()
	if len(observations) != 1 {
		t.Fatalf("observations = %d, want one per rule per container", len(observations))
	}
	if observations[0].Count != 50 {
		t.Errorf("count = %d, want 50", observations[0].Count)
	}
	if !observations[0].FirstSeen.Equal(base) {
		t.Errorf("FirstSeen = %v, want the first occurrence", observations[0].FirstSeen)
	}
	if !observations[0].LastSeen.Equal(base.Add(49 * time.Second)) {
		t.Errorf("LastSeen = %v, want the most recent", observations[0].LastSeen)
	}
}

// TestMatcherKeepsTheFirstLineNotTheLatest proves the retained line is
// the earliest match. A later occurrence is usually the same failure
// repeating; the first is the one nearest the cause.
func TestMatcherKeepsTheFirstLineNotTheLatest(t *testing.T) {
	t.Parallel()
	m := NewMatcher([]Rule{archiveRule}, 0, nil)
	m.Observe(line("orders-1", "postgres",
		"first: WAL archive check failed — Expected empty archive", 0))
	m.Observe(line("orders-1", "postgres",
		"later: WAL archive check failed — Expected empty archive", time.Minute))
	if got := m.Observations()[0].Line; !strings.HasPrefix(got, "first:") {
		t.Errorf("retained line = %q, want the first match", got)
	}
}

// TestMatcherRequiresEverySubstring proves the rule is a conjunction, so
// a rule can be specific without a pattern language and cannot half-match.
func TestMatcherRequiresEverySubstring(t *testing.T) {
	t.Parallel()
	m := NewMatcher([]Rule{archiveRule}, 0, nil)
	m.Observe(line("orders-1", "postgres", "WAL archive check failed for server orders", 0))
	m.Observe(line("orders-1", "postgres", "Expected empty archive", time.Second))
	if len(m.Observations()) != 0 {
		t.Errorf("a partial match was reported: %+v", m.Observations())
	}
}

// TestMatcherSeparatesContainers proves a sidecar's finding is not
// attributed to postgres, which is the whole reason the container is
// part of the key.
func TestMatcherSeparatesContainers(t *testing.T) {
	t.Parallel()
	m := NewMatcher([]Rule{archiveRule}, 0, nil)
	const text = "WAL archive check failed: Expected empty archive"
	m.Observe(line("orders-1", "postgres", text, 0))
	m.Observe(line("orders-1", "plugin-barman-cloud", text, time.Second))
	if got := len(m.Observations()); got != 2 {
		t.Fatalf("observations = %d, want one per container", got)
	}
}

// TestMatcherForgetsAReplacedPod proves a pod that no longer exists does
// not leave findings behind forever.
func TestMatcherForgetsAReplacedPod(t *testing.T) {
	t.Parallel()
	m := NewMatcher([]Rule{archiveRule}, 0, nil)
	const text = "WAL archive check failed: Expected empty archive"
	m.Observe(line("orders-1", "postgres", text, 0))
	m.Observe(line("orders-2", "postgres", text, time.Second))
	m.Forget("orders-1")
	observations := m.Observations()
	if len(observations) != 1 || observations[0].Pod != "orders-2" {
		t.Errorf("Forget removed the wrong observations: %+v", observations)
	}
}

// TestMatcherBoundsARetainedLine proves a hostile line cannot make one
// observation large.
func TestMatcherBoundsARetainedLine(t *testing.T) {
	t.Parallel()
	m := NewMatcher([]Rule{archiveRule}, 0, nil)
	huge := "WAL archive check failed Expected empty archive " + strings.Repeat("x", 1<<20)
	m.Observe(line("orders-1", "postgres", huge, 0))
	if got := len(m.Observations()[0].Line); got != maxObservedLine {
		t.Errorf("retained line length = %d, want it bounded to %d", got, maxObservedLine)
	}
}

// TestBufferDisabledByDefaultKeepsNothing proves the default posture:
// with retention off, the console holds no log text at all.
func TestBufferDisabledByDefaultKeepsNothing(t *testing.T) {
	t.Parallel()
	b := NewBuffer(0, 0, time.Hour)
	if b.Enabled() {
		t.Fatal("a zero-byte buffer reports itself enabled")
	}
	b.Observe(line("orders-1", "postgres", "secret query text", 0))
	if retained, ok := b.Tail("orders-1", "postgres", base); ok || len(retained) != 0 {
		t.Errorf("a disabled buffer retained %d entries (ok=%v)", len(retained), ok)
	}
}

// TestBufferBoundsBytesNotLines is the property that stops log volume
// from deciding the console's memory. A container emitting long lines
// must lose its oldest content rather than grow.
func TestBufferBoundsBytesNotLines(t *testing.T) {
	t.Parallel()
	b := NewBuffer(4096, 8192, time.Hour)
	for i := range 500 {
		b.Observe(line("orders-1", "postgres", strings.Repeat("y", 500), time.Duration(i)*time.Second))
	}
	retained, ok := b.Tail("orders-1", "postgres", base.Add(500*time.Second))
	if !ok {
		t.Fatal("buffer reported nothing for a container it received lines for")
	}
	if total := totalBytes(retained); total > 4096 {
		t.Errorf("retained %d bytes, want the per-container bound of 4096 respected", total)
	}
	// Oldest-first eviction: what survives is the tail of the stream.
	if len(retained) == 0 || !retained[len(retained)-1].At.Equal(base.Add(499*time.Second)) {
		t.Error("the newest line was evicted, so eviction is not oldest-first")
	}
}

// TestBufferGlobalBoundStopsOneContainerCostingAnother proves the total
// bound holds across containers.
func TestBufferGlobalBoundStopsOneContainerCostingAnother(t *testing.T) {
	t.Parallel()
	b := NewBuffer(8192, 4096, time.Hour)
	for i := range 200 {
		b.Observe(line("orders-1", "postgres", strings.Repeat("a", 200), time.Duration(i)*time.Second))
		b.Observe(line("orders-1", "sidecar", strings.Repeat("b", 200), time.Duration(i)*time.Second))
	}
	first, _ := b.Tail("orders-1", "postgres", base.Add(time.Hour))
	second, _ := b.Tail("orders-1", "sidecar", base.Add(time.Hour))
	if total := totalBytes(first) + totalBytes(second); total > 4096 {
		t.Errorf("retained %d bytes across containers, want the total bound of 4096", total)
	}
}

// TestBufferKeepsGapsVisible proves the buffer never joins across a
// break. Two adjacent retained lines that were not adjacent in the
// container would be the console inventing continuity.
func TestBufferKeepsGapsVisible(t *testing.T) {
	t.Parallel()
	b := NewBuffer(4096, 8192, time.Hour)
	b.Observe(line("orders-1", "postgres", "before", 0))
	b.Gap("orders-1", "postgres", base.Add(time.Second), "stream reconnected")
	b.Observe(line("orders-1", "postgres", "after", 2*time.Second))

	retained, _ := b.Tail("orders-1", "postgres", base.Add(time.Minute))
	if len(retained) != 3 || !retained[1].Gap {
		t.Fatalf("the gap was not retained between the lines: %+v", retained)
	}
	if !strings.Contains(retained[1].Text, "reconnected") {
		t.Errorf("gap lost its reason: %q", retained[1].Text)
	}
}

// TestBufferDropsWhatIsTooOld proves the age bound, since the value of a
// retained line decays quickly.
func TestBufferDropsWhatIsTooOld(t *testing.T) {
	t.Parallel()
	b := NewBuffer(8192, 8192, time.Minute)
	b.Observe(line("orders-1", "postgres", "ancient", 0))
	b.Observe(line("orders-1", "postgres", "recent", 90*time.Second))
	retained, _ := b.Tail("orders-1", "postgres", base.Add(100*time.Second))
	if len(retained) != 1 || retained[0].Text != "recent" {
		t.Errorf("age bound not applied: %+v", retained)
	}
}

// TestBufferDistinguishesEmptyFromDisabled proves the second return
// value carries its meaning, so a screen can tell "retention is off"
// from "nothing was logged".
func TestBufferDistinguishesEmptyFromDisabled(t *testing.T) {
	t.Parallel()
	if _, ok := NewBuffer(0, 0, time.Hour).Tail("orders-1", "postgres", base); ok {
		t.Error("a disabled buffer claimed to hold a stream")
	}
	enabled := NewBuffer(4096, 4096, time.Hour)
	if _, ok := enabled.Tail("orders-1", "postgres", base); ok {
		t.Error("an enabled but empty buffer claimed to hold a stream it never saw")
	}
	enabled.Observe(line("orders-1", "postgres", "x", 0))
	if _, ok := enabled.Tail("orders-1", "postgres", base); !ok {
		t.Error("a buffer that received a line reported no stream")
	}
}

// TestSinksAreConcurrencySafe runs the two sinks under the race detector
// the way the runner will: one goroutine per container, all writing while
// a reader polls.
func TestSinksAreConcurrencySafe(t *testing.T) {
	t.Parallel()
	m := NewMatcher([]Rule{archiveRule}, 0, nil)
	b := NewBuffer(4096, 16384, time.Hour)
	sinks := Sinks{m, b}

	var wg sync.WaitGroup
	for c := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			container := string(rune('a' + c))
			for i := range 200 {
				sinks.Observe(line("orders-1", container,
					"WAL archive check failed: Expected empty archive", time.Duration(i)*time.Second))
			}
			sinks.Gap("orders-1", container, base, "reconnected")
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 200 {
			_ = m.Observations()
			_, _ = b.Tail("orders-1", "a", base)
		}
	}()
	wg.Wait()

	if got := len(m.Observations()); got != 4 {
		t.Errorf("observations = %d, want one per container", got)
	}
}

func totalBytes(retained []Retained) int {
	total := 0
	for _, r := range retained {
		total += len(r.Text) + 48
	}
	return total
}
