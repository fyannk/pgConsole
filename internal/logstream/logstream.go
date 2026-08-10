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

// Package logstream follows the member pods' container logs continuously
// and offers each line to sinks that decide what, if anything, to keep.
//
// It exists because two things the console needs are only in the logs:
// the operator writes some refusals nowhere else, and a container that
// has died takes its explanation with it.
//
// The design point is that the two consumers keep very different amounts:
//
//   - The matcher analyses each line once and keeps only what matched —
//     a bounded set of observations. Its memory does not grow with log
//     volume, so it is on whenever streaming is.
//   - The buffer keeps recent lines verbatim so a screen can show them.
//     That is a standing corpus of log text, which for PostgreSQL can
//     include statements and their literal values, so it is off by
//     default and bounded in bytes rather than lines.
//
// Everything here is best effort and says so. A follow stream drops
// lines across reconnects, rotation, and container restarts, and
// Kubernetes offers no way to detect what was missed. A gap is therefore
// recorded as an explicit marker rather than papered over, and nothing
// in this package reports a count as though it were complete.
package logstream

import (
	"strings"
	"sync"
	"time"
)

// Line is one observed log line.
type Line struct {
	// Pod and Container name where it came from.
	Pod       string
	Container string
	// Text is the line without its trailing newline, bounded by the
	// reader before it reaches any sink.
	Text string
	// At is when the console observed the line, not when it was written.
	// The console's clock is the only one it can vouch for; a timestamp
	// inside the text is the database's claim and stays part of Text.
	At time.Time
}

// Sink consumes lines as they arrive. Implementations must be safe for
// concurrent use: one goroutine per container feeds them.
type Sink interface {
	// Observe takes one line. It must not block for long: the caller is
	// the reader of a live stream, and a slow sink loses lines rather
	// than delaying them.
	Observe(Line)
	// Gap records that lines were missed between two observations, with
	// the reason as far as it is known. Sinks that present content to a
	// reader must surface this rather than joining across it.
	Gap(pod, container string, at time.Time, reason string)
}

// Sinks fans one stream out to several sinks in order.
type Sinks []Sink

// Observe passes the line to each sink.
func (s Sinks) Observe(line Line) {
	for _, sink := range s {
		sink.Observe(line)
	}
}

// Gap passes the marker to each sink.
func (s Sinks) Gap(pod, container string, at time.Time, reason string) {
	for _, sink := range s {
		sink.Gap(pod, container, at, reason)
	}
}

// Rule is one thing worth noticing in a log line.
//
// Matching is by substring, deliberately, not by regular expression. The
// lines this looks for are fixed strings the operator emits, a substring
// test cannot be made pathological by a hostile log line, and a rule
// that cannot express something clever is a rule that cannot quietly
// match the wrong thing.
type Rule struct {
	// ID is the stable identifier a finding is reported under.
	ID string
	// Contains are the substrings that must all appear in a line for it
	// to match. Several are allowed so a rule can be specific without a
	// pattern language.
	Contains []string
	// Summary states what the match means, in plain language.
	Summary string
}

// matches reports whether the line satisfies every substring.
func (r Rule) matches(text string) bool {
	if len(r.Contains) == 0 {
		return false
	}
	for _, needle := range r.Contains {
		if !strings.Contains(text, needle) {
			return false
		}
	}
	return true
}

// Observation is one rule's account of what it has seen on one
// container. It is what "analyse a line once" leaves behind: the line
// that matched, kept once, plus how often the rule has fired since.
type Observation struct {
	// RuleID identifies the rule that matched.
	RuleID string
	// Summary is the rule's plain-language statement.
	Summary string
	// Pod and Container locate it.
	Pod       string
	Container string
	// Line is the first matching line, bounded. The first rather than
	// the most recent: the earliest occurrence is the one nearest the
	// cause, and a later one is usually the same failure repeating.
	Line string
	// FirstSeen and LastSeen bracket the occurrences.
	FirstSeen time.Time
	LastSeen  time.Time
	// Count is how many lines matched. It is a floor, never a total:
	// the stream is best effort, so lines were possibly missed.
	Count int
}

// maxObservedLine bounds a retained matching line. Long enough for the
// operator's messages, short enough that a hostile line cannot make one
// observation large.
const maxObservedLine = 2048

// Matcher analyses each line once against a closed rule set and retains
// only what matched.
//
// Its memory is bounded by the number of rules times the number of
// containers, independent of how much is logged, which is what makes it
// safe to run continuously.
type Matcher struct {
	rules  []Rule
	maxAge time.Duration
	now    func() time.Time

	mu       sync.RWMutex
	observed map[observationKey]*Observation
}

type observationKey struct{ rule, pod, container string }

// NewMatcher builds a matcher over a closed rule set. Observations
// expire maxAge after their last matching line: a finding is a claim
// about what the logs say now, and a line nothing has repeated since
// yesterday stops being that. A maxAge of zero, or a nil clock, retains
// observations until the container is forgotten.
func NewMatcher(rules []Rule, maxAge time.Duration, now func() time.Time) *Matcher {
	return &Matcher{rules: rules, maxAge: maxAge, now: now,
		observed: map[observationKey]*Observation{}}
}

// Observe analyses one line and keeps only a match.
func (m *Matcher) Observe(line Line) {
	for _, rule := range m.rules {
		if !rule.matches(line.Text) {
			continue
		}
		key := observationKey{rule.ID, line.Pod, line.Container}
		m.mu.Lock()
		if existing, ok := m.observed[key]; ok {
			existing.Count++
			existing.LastSeen = line.At
		} else {
			text := line.Text
			if len(text) > maxObservedLine {
				text = text[:maxObservedLine]
			}
			m.observed[key] = &Observation{
				RuleID: rule.ID, Summary: rule.Summary,
				Pod: line.Pod, Container: line.Container, Line: text,
				FirstSeen: line.At, LastSeen: line.At, Count: 1,
			}
		}
		m.mu.Unlock()
	}
}

// Gap is a no-op for the matcher. A missed line cannot be analysed, and
// recording that some unknown line went unanalysed would add noise to
// every observation without making any of them more true. The screen
// states the stream's best-effort nature once, globally, instead.
func (m *Matcher) Gap(string, string, time.Time, string) {}

// Observations returns a stable copy, ordered by rule then pod then
// container so a refresh that changes nothing does not reorder them.
func (m *Matcher) Observations() []Observation {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Expiry happens at read time, under the write lock, so the map
	// never accumulates dead entries past the next read. An expired
	// observation is dropped entirely: a stale finding that cannot
	// clear teaches a reader to ignore the screen.
	if m.maxAge > 0 && m.now != nil {
		horizon := m.now().Add(-m.maxAge)
		for key, observation := range m.observed {
			if observation.LastSeen.Before(horizon) {
				delete(m.observed, key)
			}
		}
	}
	out := make([]Observation, 0, len(m.observed))
	for _, observation := range m.observed {
		out = append(out, *observation)
	}
	sortObservations(out)
	return out
}

// Forget drops observations for a container that no longer exists, so a
// replaced pod does not leave its findings behind forever.
func (m *Matcher) Forget(pod string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.observed {
		if key.pod == pod {
			delete(m.observed, key)
		}
	}
}
