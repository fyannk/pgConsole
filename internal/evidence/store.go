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
	"sync"
	"time"
)

// Store holds the current evidence status for concurrent readers. The
// poller is the only writer; readers receive value copies, so a
// published snapshot is never mutated. A failed poll retains the last
// validated report as stale and records the failure kind; a sidecar
// that never answered records the failure kind alone.
type Store struct {
	mu      sync.RWMutex
	snap    Snapshot
	has     bool
	failure FailureKind
}

// NewStore returns an empty store: CurrentEvidence reports no report
// and no failure until the poller speaks.
func NewStore() *Store {
	return &Store{}
}

// CurrentEvidence returns the current status.
func (s *Store) CurrentEvidence() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Status{HasReport: s.has, Snapshot: s.snap, Failure: s.failure}
}

// publish replaces the report and its atomically assembled backup
// collection with a fresh validated projection, advancing the
// generation and clearing staleness and failure.
func (s *Store) publish(report Report, backups []RepoBackup, backupsTruncated bool, observedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = Snapshot{
		Generation:       s.snap.Generation + 1,
		ObservedAt:       observedAt,
		Stale:            false,
		Failure:          FailureNone,
		Report:           report,
		Backups:          append([]RepoBackup(nil), backups...),
		BackupsTruncated: backupsTruncated,
	}
	s.has = true
	s.failure = FailureNone
}

// markFailed records a failed poll: the retained report, if any, turns
// stale, and the failure kind becomes current either way.
func (s *Store) markFailed(kind FailureKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failure = kind
	if !s.has {
		return
	}
	s.snap.Stale = true
	s.snap.Failure = kind
}
