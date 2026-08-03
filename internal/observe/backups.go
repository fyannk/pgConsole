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
	"sync"
	"time"
)

// Backup catalog bounds. One extra item is retained internally as a
// truncation sentinel; neither memory nor the rendered page grows with a
// namespace-wide flood.
const (
	// MaxBackups bounds retained and rendered Backup resources.
	MaxBackups = 500
	// MaxScheduledBackups bounds retained and rendered ScheduledBackup resources.
	MaxScheduledBackups = 200
)

// ObjectStoreState is the outcome of the optional, metadata-only
// ObjectStore lookup.
type ObjectStoreState string

const (
	// ObjectStoreNotReferenced means the target Cluster has no enabled
	// Barman Cloud plugin ObjectStore reference.
	ObjectStoreNotReferenced ObjectStoreState = "not-referenced"
	// ObjectStorePresent means the referenced object was observed.
	ObjectStorePresent ObjectStoreState = "present"
	// ObjectStoreUnknown means the reference or object could not be
	// observed. Permission and CRD absence deliberately land here.
	ObjectStoreUnknown ObjectStoreState = "unknown"
)

// BackupFacts is the bounded operator-reported state of one Backup.
type BackupFacts struct {
	// Name is the resource name.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// Phase is the phase reported in status.
	Phase string
	// Method is the status method, falling back to the spec method.
	Method string
	// BackupID is the operator-reported repository backup identity
	// (status.backupId) — the only cross-source correlation key the
	// evidence contract accepts. Empty when the operator has not
	// reported one.
	BackupID string
	// PluginName is the configured backup plugin name for the plugin
	// method, empty otherwise. Correlation eligibility requires the
	// accepted Barman Cloud plugin.
	PluginName string
	// CreatedAt is the resource creation time.
	CreatedAt time.Time
	// StartedAt is the reported backup-tool start time.
	StartedAt *time.Time
	// StoppedAt is the reported backup-tool termination time.
	StoppedAt *time.Time
}

// ScheduledBackupFacts is the bounded operator-reported state of one
// ScheduledBackup. Schedule is a six-field cron expression reported by the
// resource; pgConsole does not execute or reinterpret it.
type ScheduledBackupFacts struct {
	// Name is the resource name.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// Method is the configured backup method.
	Method string
	// Schedule is the operator's six-field cron expression.
	Schedule string
	// Suspended is nil when the resource did not report the setting.
	Suspended *bool
	// CreatedAt is the resource creation time.
	CreatedAt time.Time
	// LastScheduleTime is the last successfully scheduled time.
	LastScheduleTime *time.Time
	// NextScheduleTime is the next time reported by the operator.
	NextScheduleTime *time.Time
}

// ObjectStoreReference carries only the safe metadata needed to identify the
// configured repository object. No ObjectStore spec, Secret reference, URL,
// or status content crosses the Kubernetes adapter boundary.
type ObjectStoreReference struct {
	// Name is the referenced ObjectStore name, if observable.
	Name string
	// State is the metadata-only lookup outcome.
	State ObjectStoreState
	// Destination is the reported destination path, such as an s3:// or
	// azure:// URL. Empty when the store reports none.
	Destination string
	// Endpoint is the reported endpoint URL, empty when the store uses
	// the provider default.
	Endpoint string
	// RetentionPolicy is the reported retention, empty when none is set.
	RetentionPolicy string
}

// BackupCatalogState is one complete seed and the resource versions from
// which both watches resume.
type BackupCatalogState struct {
	// Backups is the bounded selected Backup seed.
	Backups []BackupFacts
	// ScheduledBackups is the bounded selected ScheduledBackup seed.
	ScheduledBackups []ScheduledBackupFacts
	// ObjectStore is the independently degrading optional reference.
	ObjectStore ObjectStoreReference
	// BackupsTruncated reports a source safety ceiling.
	BackupsTruncated bool
	// SchedulesTruncated reports a source safety ceiling.
	SchedulesTruncated bool
	// BackupResourceVersion starts the Backup watch.
	BackupResourceVersion string
	// ScheduledResourceVersion starts the ScheduledBackup watch.
	ScheduledResourceVersion string
}

// BackupDeletion identifies a removed Backup incarnation.
type BackupDeletion struct {
	// Name is the removed resource name.
	Name string
	// UID is the removed incarnation.
	UID string
}

// ScheduledBackupDeletion identifies a removed ScheduledBackup incarnation.
type ScheduledBackupDeletion struct {
	// Name is the removed resource name.
	Name string
	// UID is the removed incarnation.
	UID string
}

// BackupChange is one change from either catalog watch. Exactly one field is
// set.
type BackupChange struct {
	// PutBackup upserts one Backup.
	PutBackup *BackupFacts
	// DeleteBackup removes one Backup incarnation.
	DeleteBackup *BackupDeletion
	// PutScheduledBackup upserts one ScheduledBackup.
	PutScheduledBackup *ScheduledBackupFacts
	// DeleteScheduledBackup removes one ScheduledBackup incarnation.
	DeleteScheduledBackup *ScheduledBackupDeletion
}

// BackupWatch is the merged Backup and ScheduledBackup watch.
type BackupWatch interface {
	// Changes streams changes until either underlying watch ends.
	Changes() <-chan BackupChange
	// Stop releases both underlying watches.
	Stop()
}

// BackupSource produces the target cluster's Backup and ScheduledBackup
// resources. The Kubernetes adapter performs namespace-scoped listing and
// exact app-side cluster selection.
type BackupSource interface {
	// FetchBackupCatalog returns a complete bounded seed.
	FetchBackupCatalog(ctx context.Context) (BackupCatalogState, error)
	// WatchBackupCatalog follows both resource kinds from the seed versions.
	WatchBackupCatalog(ctx context.Context, state BackupCatalogState) (BackupWatch, error)
}

// BackupsSnapshot is an immutable, independently stale backup catalog.
type BackupsSnapshot struct {
	// Generation increases on every complete publication.
	Generation uint64
	// ObservedAt is the last successful source-contact time.
	ObservedAt time.Time
	// Stale reports lost contact while retaining the last-good catalog.
	Stale bool
	// BackupsTruncated reports a source or display safety ceiling.
	BackupsTruncated bool
	// SchedulesTruncated reports a source or display safety ceiling.
	SchedulesTruncated bool
	// Backups is newest-first and bounded by MaxBackups.
	Backups []BackupFacts
	// ScheduledBackups is name-sorted and bounded by MaxScheduledBackups.
	ScheduledBackups []ScheduledBackupFacts
	// ObjectStore is the optional metadata-only reference result.
	ObjectStore ObjectStoreReference
}

// BackupStore holds the current backup snapshot for concurrent readers.
type BackupStore struct {
	mu   sync.RWMutex
	snap BackupsSnapshot
	has  bool
}

// NewBackupStore returns an empty store.
func NewBackupStore() *BackupStore { return &BackupStore{} }

// CurrentBackups returns the snapshot and whether one exists.
func (s *BackupStore) CurrentBackups() (BackupsSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.has
}

func (s *BackupStore) publish(backups []BackupFacts, schedules []ScheduledBackupFacts, objectStore ObjectStoreReference, observedAt time.Time, sourceBackupsTruncated, sourceSchedulesTruncated bool) {
	// Both cuts are decided by the length, never by the flag. The source
	// flags are sticky for the life of a seed, so a set that was once
	// over its bound and has since shrunk below it still arrives here
	// flagged; cutting on the flag sliced past the end and panicked.
	backupCopy, backupsCut := bounded(backups, lessBackupRecency, MaxBackups)
	backupsTruncated := sourceBackupsTruncated || backupsCut

	scheduleCopy, schedulesCut := bounded(schedules, lessScheduleName, MaxScheduledBackups)
	schedulesTruncated := sourceSchedulesTruncated || schedulesCut

	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = BackupsSnapshot{
		Generation:         s.snap.Generation + 1,
		ObservedAt:         observedAt,
		BackupsTruncated:   backupsTruncated,
		SchedulesTruncated: schedulesTruncated,
		Backups:            backupCopy,
		ScheduledBackups:   scheduleCopy,
		ObjectStore:        objectStore,
	}
	s.has = true
}

func (s *BackupStore) markStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.has {
		s.snap.Stale = true
	}
}

// lessBackupRecency orders the rendered catalog newest first: a backup's
// relevance is its recency, and the question the screen answers is when
// the last one ran. Name breaks ties so the order is total.
func lessBackupRecency(a, b BackupFacts) bool {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.Name < b.Name
}

// lessScheduleName orders schedules by name. A schedule has no
// meaningful recency — it is a standing instruction, not an event.
func lessScheduleName(a, b ScheduledBackupFacts) bool { return a.Name < b.Name }

// backupRetention identifies retained backups and bounds them at one
// above the rendered bound, the extra entry being the truncation
// sentinel. The oldest loses, which matches lessBackupRecency: an
// evicted entry is one the page would have cut anyway.
var backupRetention = retention[BackupFacts]{
	Name:      func(b BackupFacts) string { return b.Name },
	UID:       func(b BackupFacts) string { return b.UID },
	Limit:     MaxBackups + 1,
	Evictable: func(a, b BackupFacts) bool { return a.CreatedAt.Before(b.CreatedAt) },
}

// scheduleRetention identifies retained schedules. Schedules carry no
// useful arrival order, so the lexically largest name loses: the choice
// is arbitrary but deterministic, and it matches lessScheduleName so an
// evicted entry is one the page would have cut anyway.
var scheduleRetention = retention[ScheduledBackupFacts]{
	Name:      func(s ScheduledBackupFacts) string { return s.Name },
	UID:       func(s ScheduledBackupFacts) string { return s.UID },
	Limit:     MaxScheduledBackups + 1,
	Evictable: func(a, b ScheduledBackupFacts) bool { return a.Name > b.Name },
}

// BackupCollector maintains a bounded catalog using seed, merged watch,
// immutable publication, stale retention, and bounded retry backoff.
// That contract is the shared loop in loop.go; what follows is only the
// catalog-specific part of it.
//
// This is the two-set shape: the catalog merges two watches into one
// change stream, and each set keeps its own truncation flag so one
// screen never reports one set's bound as the other's.
type BackupCollector struct {
	source             BackupSource
	store              *BackupStore
	clock              Clock
	logger             *slog.Logger
	backups            keyed[BackupFacts]
	schedules          keyed[ScheduledBackupFacts]
	objectStore        ObjectStoreReference
	backupsTruncated   bool
	schedulesTruncated bool
}

// NewBackupCollector wires a backup collector onto a store.
func NewBackupCollector(source BackupSource, store *BackupStore, clock Clock, logger *slog.Logger) *BackupCollector {
	return &BackupCollector{source: source, store: store, clock: clock, logger: logger}
}

// Run blocks until ctx is canceled, maintaining the store.
func (c *BackupCollector) Run(ctx context.Context) error {
	return newLoop[BackupCatalogState, BackupChange](c, c.clock, c.logger).Run(ctx)
}

// op names this resource in contact-loss logs.
func (c *BackupCollector) op() string { return "backup catalog" }

// seed replaces both retained sets and the object-store reference. The
// cursor is the whole seed state rather than a resource version: the
// catalog resumes two watches and needs both versions.
func (c *BackupCollector) seed(ctx context.Context) (BackupCatalogState, error) {
	state, err := c.source.FetchBackupCatalog(ctx)
	if err != nil {
		return BackupCatalogState{}, err
	}
	c.backupsTruncated = state.BackupsTruncated
	c.backups = make(keyed[BackupFacts], len(state.Backups))
	for _, backup := range state.Backups {
		if c.backups.put(backup, backupRetention) {
			c.backupsTruncated = true
		}
	}
	c.schedulesTruncated = state.SchedulesTruncated
	c.schedules = make(keyed[ScheduledBackupFacts], len(state.ScheduledBackups))
	for _, schedule := range state.ScheduledBackups {
		if c.schedules.put(schedule, scheduleRetention) {
			c.schedulesTruncated = true
		}
	}
	c.objectStore = state.ObjectStore
	return state, nil
}

// follow starts the merged catalog watch from the seed state. Either
// underlying stream ending ends the merged one, so the collector
// re-seeds both kinds rather than publishing a half-current generation.
func (c *BackupCollector) follow(ctx context.Context, from BackupCatalogState) (<-chan BackupChange, func(), error) {
	w, err := c.source.WatchBackupCatalog(ctx, from)
	if err != nil {
		return nil, nil, err
	}
	return w.Changes(), w.Stop, nil
}

// apply folds one change from either watch into its set. It reports
// whether the change was recognized; a change carrying nothing is not.
func (c *BackupCollector) apply(change BackupChange) bool {
	switch {
	case change.PutBackup != nil:
		if c.backups.put(*change.PutBackup, backupRetention) {
			c.backupsTruncated = true
		}
	case change.DeleteBackup != nil:
		c.backups.remove(change.DeleteBackup.Name, change.DeleteBackup.UID, backupRetention)
	case change.PutScheduledBackup != nil:
		if c.schedules.put(*change.PutScheduledBackup, scheduleRetention) {
			c.schedulesTruncated = true
		}
	case change.DeleteScheduledBackup != nil:
		c.schedules.remove(change.DeleteScheduledBackup.Name, change.DeleteScheduledBackup.UID, scheduleRetention)
	default:
		return false
	}
	return true
}

// publish snapshots both retained sets and the object-store reference
// into the store.
func (c *BackupCollector) publish(observedAt time.Time) {
	c.store.publish(c.backups.list(), c.schedules.list(), c.objectStore, observedAt,
		c.backupsTruncated, c.schedulesTruncated)
}

// markStale marks the retained snapshot stale, if one exists.
func (c *BackupCollector) markStale() { c.store.markStale() }
