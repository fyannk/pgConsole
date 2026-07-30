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
	"testing"
	"time"
)

func TestStoreEmptyReportsNoReportAndNoFailure(t *testing.T) {
	t.Parallel()
	status := NewStore().CurrentEvidence()
	if status.HasReport || status.Failure != FailureNone {
		t.Errorf("empty store status = %+v", status)
	}
}

func TestStoreFailureBeforeFirstSuccessCarriesKindOnly(t *testing.T) {
	t.Parallel()
	store := NewStore()
	store.markFailed(FailureUnavailable)
	status := store.CurrentEvidence()
	if status.HasReport {
		t.Error("failure without a report claims a report")
	}
	if status.Failure != FailureUnavailable {
		t.Errorf("failure = %q", status.Failure)
	}
}

func TestStorePublishRetainStaleAndRecover(t *testing.T) {
	t.Parallel()
	store := NewStore()
	observed := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	store.publish(Report{Fingerprint: "sha256:aa"}, []RepoBackup{{Server: "orders", BackupID: "b1"}}, true, observed)

	status := store.CurrentEvidence()
	if !status.HasReport || status.Snapshot.Generation != 1 || status.Snapshot.Stale {
		t.Fatalf("published status = %+v", status)
	}

	store.markFailed(FailureTimeout)
	status = store.CurrentEvidence()
	if !status.HasReport || !status.Snapshot.Stale {
		t.Fatal("failure did not retain a stale report")
	}
	if status.Snapshot.Report.Fingerprint != "sha256:aa" {
		t.Error("stale retention lost the last-good report")
	}
	if len(status.Snapshot.Backups) != 1 || !status.Snapshot.BackupsTruncated {
		t.Error("stale retention lost the assembled collection")
	}
	if status.Failure != FailureTimeout || status.Snapshot.Failure != FailureTimeout {
		t.Errorf("failure kinds = %q %q", status.Failure, status.Snapshot.Failure)
	}

	store.publish(Report{Fingerprint: "sha256:bb"}, nil, false, observed.Add(time.Minute))
	status = store.CurrentEvidence()
	if status.Snapshot.Generation != 2 || status.Snapshot.Stale || status.Failure != FailureNone {
		t.Errorf("recovered status = %+v", status)
	}
}
