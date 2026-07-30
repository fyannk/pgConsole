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
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/fyannk/pgconsole/internal/redact"
)

type backupStep struct {
	state    BackupCatalogState
	fetchErr error
	changes  []BackupChange
}

type scriptedBackupSource struct {
	mu     sync.Mutex
	steps  []backupStep
	i      int
	cancel context.CancelFunc
}

func (s *scriptedBackupSource) FetchBackupCatalog(_ context.Context) (BackupCatalogState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.steps) {
		return BackupCatalogState{}, context.Canceled
	}
	step := s.steps[s.i]
	if step.fetchErr != nil {
		s.i++
		if s.i == len(s.steps) {
			s.cancel()
		}
	}
	return step.state, step.fetchErr
}

func (s *scriptedBackupSource) WatchBackupCatalog(_ context.Context, _ BackupCatalogState) (BackupWatch, error) {
	s.mu.Lock()
	step := s.steps[s.i]
	s.i++
	done := s.i == len(s.steps)
	s.mu.Unlock()
	changes := make(chan BackupChange, len(step.changes))
	for _, change := range step.changes {
		changes <- change
	}
	close(changes)
	if done {
		s.cancel()
	}
	return staticBackupWatch{changes}, nil
}

type staticBackupWatch struct{ changes <-chan BackupChange }

func (w staticBackupWatch) Changes() <-chan BackupChange { return w.changes }
func (staticBackupWatch) Stop()                          {}

func runBackups(t *testing.T, steps []backupStep) *BackupStore {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &scriptedBackupSource{steps: steps, cancel: cancel}
	store := NewBackupStore()
	collector := NewBackupCollector(source, store, newFakeClock(), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err := collector.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return store
}

func TestBackupCollectorPublishesBothKindsAndRetainsStale(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	store := runBackups(t, []backupStep{
		{state: BackupCatalogState{
			Backups:          []BackupFacts{{Name: "b1", UID: "u1", CreatedAt: created}},
			ScheduledBackups: []ScheduledBackupFacts{{Name: "daily", UID: "s1"}},
			ObjectStore:      ObjectStoreReference{Name: "orders-store", State: ObjectStorePresent},
		}, changes: []BackupChange{{PutBackup: &BackupFacts{Name: "b2", UID: "u2", CreatedAt: created.Add(time.Hour)}}}},
		{fetchErr: redact.NewError("backups list", redact.CategoryForbidden, nil)},
	})
	snap, ok := store.CurrentBackups()
	if !ok || !snap.Stale {
		t.Fatalf("last-good backup snapshot not retained stale: %+v, ok=%v", snap, ok)
	}
	if len(snap.Backups) != 2 || len(snap.ScheduledBackups) != 1 {
		t.Fatalf("catalog lost content: %+v", snap)
	}
	if snap.ObjectStore.State != ObjectStorePresent {
		t.Fatal("ObjectStore observation was not retained")
	}
}

func TestBackupStoreBoundsAndFlagsTruncation(t *testing.T) {
	t.Parallel()
	backups := make([]BackupFacts, MaxBackups+50)
	for i := range backups {
		backups[i] = BackupFacts{Name: fmt.Sprintf("b-%04d", i), UID: fmt.Sprintf("u-%d", i), CreatedAt: time.Unix(int64(i), 0)}
	}
	schedules := make([]ScheduledBackupFacts, MaxScheduledBackups+50)
	for i := range schedules {
		schedules[i] = ScheduledBackupFacts{Name: fmt.Sprintf("s-%04d", i), UID: fmt.Sprintf("su-%d", i)}
	}
	store := NewBackupStore()
	store.publish(backups, schedules, ObjectStoreReference{}, time.Unix(1000, 0), false, false)
	snap, _ := store.CurrentBackups()
	if len(snap.Backups) != MaxBackups || !snap.BackupsTruncated {
		t.Fatalf("backups bound not visible: len=%d truncated=%v", len(snap.Backups), snap.BackupsTruncated)
	}
	if len(snap.ScheduledBackups) != MaxScheduledBackups || !snap.SchedulesTruncated {
		t.Fatalf("schedules bound not visible: len=%d truncated=%v", len(snap.ScheduledBackups), snap.SchedulesTruncated)
	}
	if snap.Backups[0].Name != "b-0549" || snap.ScheduledBackups[0].Name != "s-0000" {
		t.Fatalf("deterministic orders not applied: first backup=%s first schedule=%s", snap.Backups[0].Name, snap.ScheduledBackups[0].Name)
	}
}

func TestBackupDeletionChecksUID(t *testing.T) {
	t.Parallel()
	collector := &BackupCollector{backups: map[string]BackupFacts{"b": {Name: "b", UID: "new"}}, schedules: map[string]ScheduledBackupFacts{}, store: NewBackupStore(), clock: newFakeClock()}
	collector.apply(BackupChange{DeleteBackup: &BackupDeletion{Name: "b", UID: "old"}})
	if _, ok := collector.backups["b"]; !ok {
		t.Fatal("deletion of an old incarnation removed the current Backup")
	}
}
