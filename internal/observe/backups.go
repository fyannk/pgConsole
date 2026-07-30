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
	"sort"
	"sync"
	"time"

	"github.com/fyannk/pgConsole/internal/redact"
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
	backupCopy := append([]BackupFacts(nil), backups...)
	sort.Slice(backupCopy, func(i, j int) bool {
		if !backupCopy[i].CreatedAt.Equal(backupCopy[j].CreatedAt) {
			return backupCopy[i].CreatedAt.After(backupCopy[j].CreatedAt)
		}
		return backupCopy[i].Name < backupCopy[j].Name
	})
	backupsTruncated := sourceBackupsTruncated || len(backupCopy) > MaxBackups
	if backupsTruncated {
		backupCopy = backupCopy[:MaxBackups]
	}

	scheduleCopy := append([]ScheduledBackupFacts(nil), schedules...)
	sort.Slice(scheduleCopy, func(i, j int) bool { return scheduleCopy[i].Name < scheduleCopy[j].Name })
	schedulesTruncated := sourceSchedulesTruncated || len(scheduleCopy) > MaxScheduledBackups
	if schedulesTruncated {
		scheduleCopy = scheduleCopy[:MaxScheduledBackups]
	}

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

// BackupCollector maintains a bounded catalog using seed, merged watch,
// immutable publication, stale retention, and bounded retry backoff.
type BackupCollector struct {
	source             BackupSource
	store              *BackupStore
	clock              Clock
	logger             *slog.Logger
	backoff            time.Duration
	backups            map[string]BackupFacts
	schedules          map[string]ScheduledBackupFacts
	objectStore        ObjectStoreReference
	backupsTruncated   bool
	schedulesTruncated bool
}

// NewBackupCollector wires a backup collector onto a store.
func NewBackupCollector(source BackupSource, store *BackupStore, clock Clock, logger *slog.Logger) *BackupCollector {
	return &BackupCollector{source: source, store: store, clock: clock, logger: logger, backoff: backoffInitial}
}

// Run blocks until ctx is canceled, maintaining the store.
func (c *BackupCollector) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		state, err := c.source.FetchBackupCatalog(ctx)
		if err != nil {
			c.loseContact("backup catalog fetch", err)
			if c.wait(ctx) != nil {
				return nil
			}
			continue
		}
		c.seed(state)
		c.publish()

		w, err := c.source.WatchBackupCatalog(ctx, state)
		if err != nil {
			c.loseContact("backup catalog watch start", err)
			if c.wait(ctx) != nil {
				return nil
			}
			continue
		}
		c.follow(ctx, w)
		if ctx.Err() != nil {
			return nil
		}
		c.loseContact("backup catalog watch", nil)
		if c.wait(ctx) != nil {
			return nil
		}
	}
}

func (c *BackupCollector) seed(state BackupCatalogState) {
	c.backupsTruncated = state.BackupsTruncated
	c.backups = make(map[string]BackupFacts, len(state.Backups))
	for _, backup := range state.Backups {
		c.retainBackup(backup)
	}
	c.schedulesTruncated = state.SchedulesTruncated
	c.schedules = make(map[string]ScheduledBackupFacts, len(state.ScheduledBackups))
	for _, schedule := range state.ScheduledBackups {
		c.retainSchedule(schedule)
	}
	c.objectStore = state.ObjectStore
}

func (c *BackupCollector) follow(ctx context.Context, w BackupWatch) {
	defer w.Stop()
	for {
		select {
		case change, ok := <-w.Changes():
			if !ok {
				return
			}
			c.apply(change)
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return
		case change, ok := <-w.Changes():
			if !ok {
				return
			}
			c.apply(change)
		}
	}
}

func (c *BackupCollector) apply(change BackupChange) {
	switch {
	case change.PutBackup != nil:
		c.retainBackup(*change.PutBackup)
	case change.DeleteBackup != nil:
		if current, ok := c.backups[change.DeleteBackup.Name]; ok && current.UID == change.DeleteBackup.UID {
			delete(c.backups, change.DeleteBackup.Name)
		}
	case change.PutScheduledBackup != nil:
		c.retainSchedule(*change.PutScheduledBackup)
	case change.DeleteScheduledBackup != nil:
		if current, ok := c.schedules[change.DeleteScheduledBackup.Name]; ok && current.UID == change.DeleteScheduledBackup.UID {
			delete(c.schedules, change.DeleteScheduledBackup.Name)
		}
	default:
		return
	}
	c.publish()
}

func (c *BackupCollector) retainBackup(backup BackupFacts) {
	const retained = MaxBackups + 1
	if _, exists := c.backups[backup.Name]; !exists && len(c.backups) >= retained {
		c.backupsTruncated = true
		oldestName := ""
		var oldest time.Time
		for name, current := range c.backups {
			if oldestName == "" || current.CreatedAt.Before(oldest) {
				oldestName, oldest = name, current.CreatedAt
			}
		}
		delete(c.backups, oldestName)
	}
	c.backups[backup.Name] = backup
}

func (c *BackupCollector) retainSchedule(schedule ScheduledBackupFacts) {
	const retained = MaxScheduledBackups + 1
	if _, exists := c.schedules[schedule.Name]; !exists && len(c.schedules) >= retained {
		c.schedulesTruncated = true
		largest := ""
		for name := range c.schedules {
			if name > largest {
				largest = name
			}
		}
		delete(c.schedules, largest)
	}
	c.schedules[schedule.Name] = schedule
}

func (c *BackupCollector) publish() {
	backups := make([]BackupFacts, 0, len(c.backups))
	for _, backup := range c.backups {
		backups = append(backups, backup)
	}
	schedules := make([]ScheduledBackupFacts, 0, len(c.schedules))
	for _, schedule := range c.schedules {
		schedules = append(schedules, schedule)
	}
	c.store.publish(backups, schedules, c.objectStore, c.clock.Now(), c.backupsTruncated, c.schedulesTruncated)
	c.backoff = backoffInitial
}

func (c *BackupCollector) loseContact(op string, err error) {
	c.store.markStale()
	attrs := []any{slog.String("op", op)}
	if err != nil {
		attrs = append(attrs, slog.String("category", redact.Safe(err)))
	}
	c.logger.Info("contact lost", attrs...)
}

func (c *BackupCollector) wait(ctx context.Context) error {
	d := c.backoff
	c.backoff *= 2
	if c.backoff > backoffMax {
		c.backoff = backoffMax
	}
	return c.clock.Wait(ctx, d)
}
