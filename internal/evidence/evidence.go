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

// Package evidence consumes the repository-evidence sidecar API of the
// evidence contract: a versioned, read-only JSON API over the
// pod-private Unix socket, authenticated by
// the mounted pod-local token. The package imports the producer's
// types-only module and nothing else from that repository, validates
// every response with the module's own invariants, and republishes a
// re-rendered, redacted projection — reason codes, states, counts, and
// times only. Free-text messages from the sidecar never leave this
// package, so a template cannot proxy them to a browser.
//
// The consumer preserves uncertainty twice over: the sidecar reports
// its own staleness against the repository, and the console tracks its
// own contact with the sidecar. Both surface independently; neither can
// mask the other, and no failure here touches console readiness.
package evidence

import "time"

// FailureKind is the closed, output-safe vocabulary of consumer-side
// poll failures. Rendering and logging key on these values; raw
// transport or response detail never leaves the package.
type FailureKind string

// The closed set of failure kinds.
const (
	// FailureNone marks a successful latest poll.
	FailureNone FailureKind = ""
	// FailureUnavailable reports that the sidecar could not be reached.
	FailureUnavailable FailureKind = "unavailable"
	// FailureTimeout reports that the request exceeded the contract's
	// five-second deadline.
	FailureTimeout FailureKind = "timeout"
	// FailureCanceled reports that the poll was canceled by shutdown.
	FailureCanceled FailureKind = "canceled"
	// FailureUnauthenticated reports that the sidecar rejected the
	// pod-local token.
	FailureUnauthenticated FailureKind = "unauthenticated"
	// FailureBusy reports the sidecar's concurrency budget was reached.
	FailureBusy FailureKind = "busy"
	// FailureIncompatible reports a version, kind, or media-type
	// disagreement: an unlisted runtime pair degrades, never guesses.
	FailureIncompatible FailureKind = "incompatible-schema"
	// FailureInvalid reports a response that violated the contract's
	// own invariants or size bounds.
	FailureInvalid FailureKind = "invalid-response"
	// FailureIdentityMismatch reports evidence whose repository
	// identity does not match the operator-configured expectation.
	FailureIdentityMismatch FailureKind = "identity-mismatch"
	// FailurePublicationChanged reports that the sidecar's publication
	// advanced during collection assembly; the partial assembly is
	// discarded and the next poll restarts from the snapshot.
	FailurePublicationChanged FailureKind = "publication-changed"
)

// StateFact is one semantic result reduced to its typed parts: the
// four-state vocabulary value and the stable reason code. The
// producer's diagnostic message is deliberately absent.
type StateFact struct {
	// State is exactly healthy, warning, unhealthy, or unknown.
	State string
	// Code is the stable lower-kebab-case reason code.
	Code string
}

// CapabilityFact is one format-neutral capability projection.
type CapabilityFact struct {
	// ID identifies the evidence operation.
	ID string
	// Support states whether the repository format proves it.
	Support string
	// State is the current evidence state.
	State string
	// Code is the stable reason code.
	Code string
}

// InventoryFacts is the provider-neutral inventory projection. Nil
// counts are unknown, never zero.
type InventoryFacts struct {
	// Known reports whether the totals below are known.
	Known bool
	// ObjectCount is the scoped object total.
	ObjectCount *uint64
	// StoredBytes is the scoped stored-byte total.
	StoredBytes *uint64
	// UnscopedObjectCount counts objects outside the configured scope.
	UnscopedObjectCount *uint64
	// PagesExamined counts listing pages of the last attempt.
	PagesExamined uint64
	// ObjectsExamined counts objects of the last attempt.
	ObjectsExamined uint64
	// LastFailureCategory is the producer's redacted failure category
	// of a failed latest attempt, empty otherwise.
	LastFailureCategory string
}

// StateCountFacts counts every normative evidence state.
type StateCountFacts struct {
	// Healthy counts healthy items.
	Healthy uint64
	// Warning counts warning items.
	Warning uint64
	// Unhealthy counts unhealthy items.
	Unhealthy uint64
	// Unknown counts unknown items.
	Unknown uint64
}

// WALCountFacts counts the Barman WAL object classes.
type WALCountFacts struct {
	// Segment counts complete segments.
	Segment uint64
	// Partial counts partial segments.
	Partial uint64
	// History counts timeline history files.
	History uint64
	// BackupHistory counts backup history files.
	BackupHistory uint64
	// Unknown counts unclassified objects.
	Unknown uint64
	// Duplicate counts duplicate objects.
	Duplicate uint64
}

// RetentionFacts is the Barman retention projection.
type RetentionFacts struct {
	// VisibleBackups counts backups the repository shows.
	VisibleBackups *uint64
	// StructurallyUsableBackups counts structurally usable backups.
	StructurallyUsableBackups *uint64
	// OldestCompletionAt is the oldest backup completion time.
	OldestCompletionAt *time.Time
	// NewestCompletionAt is the newest backup completion time.
	NewestCompletionAt *time.Time
	// MinimumConfigured reports a configured redundancy expectation.
	MinimumConfigured bool
	// MinimumRedundancy is the configured minimum, when configured.
	MinimumRedundancy *uint64
	// Result is the retention state and reason code.
	Result StateFact
}

// BarmanFacts is the format-owned summary projection of the recognized
// barman-cloud details variant.
type BarmanFacts struct {
	// BackupItems counts backup evidence items of the generation.
	BackupItems *uint64
	// WALRangeItems counts contiguous WAL ranges.
	WALRangeItems *uint64
	// WALGapItems counts candidate and confirmed WAL gaps.
	WALGapItems *uint64
	// RecoveryPathItems counts observed recovery paths.
	RecoveryPathItems *uint64
	// StructurallyUsableBackups counts structurally usable backups.
	StructurallyUsableBackups *uint64
	// BackupStates counts backups per evidence state.
	BackupStates *StateCountFacts
	// WALCounts counts WAL objects per class.
	WALCounts *WALCountFacts
	// WAL is the WAL continuity result.
	WAL StateFact
	// Timeline is the timeline traversal result.
	Timeline StateFact
	// Coverage is the observed recovery coverage result.
	Coverage StateFact
	// Retention is the retention comparison projection.
	Retention RetentionFacts
	// LatestArchiveReceiptAt is the newest WAL receipt time.
	LatestArchiveReceiptAt *time.Time
	// RangesTruncated reports a producer safety ceiling on ranges.
	RangesTruncated bool
	// DiagnosticsTruncated reports a producer diagnostics ceiling.
	DiagnosticsTruncated bool
}

// Report is the re-rendered, redacted projection of one validated
// snapshot response. Every field is either a typed enum value, a
// bounded identifier the contract validates, a count, or a time; no
// free-text producer message survives the projection.
type Report struct {
	// ProducerVersion is the emitting ObjectStoreViewer build version.
	ProducerVersion string
	// ClusterNamespace is the producer-bound cluster namespace.
	ClusterNamespace string
	// ClusterUID is the producer-bound immutable cluster UID. Current
	// agreement requires it to equal the console's own observed UID;
	// the comparison happens at rendering against the observed
	// snapshot, never against injected configuration.
	ClusterUID string
	// ClusterName is the display-only operator-injected name.
	ClusterName string
	// Provider is the repository provider, "s3" in this profile.
	Provider string
	// Format is the repository format, "barman-cloud" in this profile.
	Format string
	// ScopeKind is the format-owned scope kind, "barman-server".
	ScopeKind string
	// ScopeName is the exact Barman server name.
	ScopeName string
	// Fingerprint is the producer's redacted destination fingerprint —
	// the only repository destination identity the console shows.
	Fingerprint string
	// Revision identifies the published attempt result.
	Revision uint64
	// EvidenceGeneration identifies the last complete evidence.
	EvidenceGeneration uint64
	// StartedAt is the evidence generation's scan start time.
	StartedAt *time.Time
	// CompletedAt is the evidence generation's scan completion time.
	CompletedAt *time.Time
	// LastAttemptAt is the last refresh attempt time.
	LastAttemptAt *time.Time
	// Completeness is complete, incomplete, or no-completed-scan.
	Completeness string
	// SourceStale is the sidecar's own staleness claim against the
	// repository: the retained evidence no longer reflects the latest
	// refresh attempt.
	SourceStale bool
	// Overall is the snapshot state and reason code.
	Overall StateFact
	// Capabilities is the complete, sorted capability projection.
	Capabilities []CapabilityFact
	// Inventory is the provider-neutral inventory projection.
	Inventory InventoryFacts
	// DetailsType is the bounded tagged-union type tag.
	DetailsType string
	// Barman is the recognized barman-cloud summary; nil when the
	// variant tag is unknown to this consumer, which renders as
	// explicitly unknown, never as absent-and-healthy.
	Barman *BarmanFacts
}

// RepoBackup is one re-rendered repository backup evidence item from
// the generation-consistent collection pages: bounded identifiers,
// typed state with its stable code, counts, and times only.
type RepoBackup struct {
	// Server is the Barman server scope of the item.
	Server string
	// BackupID is the exact repository backup identity — the only
	// correlation key the contract accepts.
	BackupID string
	// Status is the repository's bounded status word, empty when not
	// reported.
	Status string
	// BackupType is the bounded backup type, empty when not reported.
	BackupType string
	// Result is the structural evidence state and reason code.
	Result StateFact
	// Timeline is the PostgreSQL timeline, nil when unknown.
	Timeline *uint64
	// BeginAt is the backup begin time, nil when unknown.
	BeginAt *time.Time
	// EndAt is the backup end time, nil when unknown.
	EndAt *time.Time
	// StoredArtifactBytes is the stored artifact size, nil when
	// unknown.
	StoredArtifactBytes *uint64
}

// Snapshot is the console-side publication: one validated report, the
// atomically assembled backup collection of the same publication
// identity, and the console's own contact bookkeeping, mirroring the
// Kubernetes snapshot vocabulary of generation, observation time, and
// staleness.
type Snapshot struct {
	// Generation increases by one on every publication.
	Generation uint64
	// ObservedAt is the time of the last successful poll.
	ObservedAt time.Time
	// Stale reports the console has lost contact with the sidecar and
	// the report below is the retained last-good projection.
	Stale bool
	// Failure is the kind of the latest failed poll; FailureNone while
	// contact holds.
	Failure FailureKind
	// Report is the last validated projection.
	Report Report
	// Backups is the assembled backup evidence of the report's
	// publication identity, sorted as the producer serves it. Empty
	// with evidence generation zero: no complete scan, no items.
	Backups []RepoBackup
	// BackupsTruncated reports the consumer's own assembly ceiling
	// cut the collection; absence conclusions are then unknown.
	BackupsTruncated bool
}

// Status is what the store reports to the renderer: the snapshot when
// one exists, and the latest failure kind either way, so a sidecar
// that has never answered still renders an attributed unknown.
type Status struct {
	// HasReport reports whether Snapshot carries a validated report.
	HasReport bool
	// Snapshot is the current publication, meaningful with HasReport.
	Snapshot Snapshot
	// Failure is the latest poll failure kind; FailureNone while
	// contact holds.
	Failure FailureKind
}
