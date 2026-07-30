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

// Package observe owns the collector and the immutable snapshot the
// console renders. Snapshots carry a monotonic generation, the time of
// the last successful contact, and an explicit staleness mark; a broken
// watch retains the last complete snapshot and marks it stale, so the
// page can never present a broken watch as a healthy, current cluster.
// The package is pure domain: it imports neither client-go nor the
// CloudNativePG API types.
package observe

import (
	"context"
	"time"
)

// Condition is one operator-reported cluster condition.
type Condition struct {
	// Type is the condition type, such as "Ready".
	Type string
	// Status is the reported status string, such as "True".
	Status string
	// Reason is the operator's machine reason, possibly empty.
	Reason string
	// Message is the operator's human message, possibly empty. It is
	// rendered as escaped, length-bounded text only.
	Message string
}

// ClusterFacts is the operator-reported state of the one target
// cluster, converted to a source-neutral shape. A string field that the
// operator did not report is empty; a numeric field that was not
// reported is nil. Both render as "unknown", never as a value.
type ClusterFacts struct {
	// Present is false when the API server confirmed the cluster does
	// not exist. The other fields are meaningful only when true.
	Present bool
	// UID is the immutable Kubernetes UID of the observed Cluster. It
	// is the console's own evidence of cluster identity: correlation
	// with external sources compares against this observed value, never
	// against injected configuration. A recreated cluster with the same
	// name carries a new UID and must never inherit correlations.
	UID string
	// Phase is the operator-reported phase.
	Phase string
	// PhaseReason is the operator-reported phase reason.
	PhaseReason string
	// CurrentPrimary is the instance currently acting as primary.
	CurrentPrimary string
	// TargetPrimary is the instance the operator is moving primary to.
	TargetPrimary string
	// DesiredInstances is the declared instance count.
	DesiredInstances *int
	// ReadyInstances is the operator-reported ready instance count.
	ReadyInstances *int
	// TimelineID is the operator-reported PostgreSQL timeline.
	TimelineID *int
	// Image is the container image the operator reports for the pods.
	Image string
	// PostgresMajorVersion is the reported PostgreSQL major version.
	PostgresMajorVersion *int
	// Conditions are the operator-reported conditions, bounded by the
	// source.
	Conditions []Condition
}

// ClusterState is one observation delivered by a Source: the facts plus
// the resource version to resume watching from. An absent cluster has
// Facts.Present false and an empty ResourceVersion.
type ClusterState struct {
	// Facts is the converted operator-reported state.
	Facts ClusterFacts
	// ResourceVersion resumes the watch after this observation.
	ResourceVersion string
}

// Watch is a running watch on the target cluster. Results is closed
// when the watch ends for any reason; the collector then re-seeds with
// a fresh Fetch. Stop releases the watch and must be safe to call after
// Results closed.
type Watch interface {
	// Results streams observations until the watch ends.
	Results() <-chan ClusterState
	// Stop releases the watch.
	Stop()
}

// Source produces observations of the one target cluster. The concrete
// implementation performs the pinned get and the name-scoped watch; the
// fake drives tests.
type Source interface {
	// Fetch returns the current state through the pinned get. An absent
	// cluster is a successful observation, not an error.
	Fetch(ctx context.Context) (ClusterState, error)
	// Watch streams changes from the given resource version.
	Watch(ctx context.Context, fromResourceVersion string) (Watch, error)
}

// Snapshot is what the console renders. It is immutable: the store
// publishes replacement values, never mutations of shared state.
type Snapshot struct {
	// Generation increases by one on every publication.
	Generation uint64
	// ObservedAt is the time of the last successful contact.
	ObservedAt time.Time
	// Stale reports that the collector has lost contact and the data
	// below is the retained last-good observation.
	Stale bool
	// Cluster is the last observed state.
	Cluster ClusterFacts
}

// LogTail is one bounded, on-demand log fetch. It is never cached and
// never persisted: the content lives for one response.
type LogTail struct {
	// Content is the escaped-at-render tail content.
	Content string
	// TruncatedByBytes reports the byte ceiling cut the tail.
	TruncatedByBytes bool
	// LineLimit is the applied line bound.
	LineLimit int
	// ByteLimit is the applied byte bound.
	ByteLimit int64
}

// Clock supplies time and interruptible waiting so the collector is
// deterministic under test.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// Wait sleeps for d or until ctx is done, returning ctx.Err in that
	// case.
	Wait(ctx context.Context, d time.Duration) error
}

// RealClock is the production Clock.
type RealClock struct{}

// Now returns the wall-clock time.
func (RealClock) Now() time.Time { return time.Now() }

// Wait sleeps for d or until ctx is done.
func (RealClock) Wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
