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

package cnpg

import (
	"time"

	"github.com/fyannk/pgConsole/internal/diagnose"
)

// backupRules read the Backup objects' own reported phases. The
// operator records its backup refusals as events on the Backup objects,
// which are outside the observed event window — but the phase those
// refusals leave behind is on the object, and the object is observed.
func backupRules() []diagnose.Rule {
	return []diagnose.Rule{
		{
			ID:        "cnpg-backup-failed",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityWarning,
			Describes: "a Backup object reporting the failed phase",
			Summary:   "A backup failed and will not be retried.",
			Detail: "A failed Backup object stays failed: only a new backup — scheduled or " +
				"manual — produces the next recovery point.",
			When: diagnose.BackupPhase{AnyOf: []string{"failed"}},
		},
		{
			ID:        "cnpg-backup-stuck-pending",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityWarning,
			Describes: "a Backup pending for over half an hour",
			Summary:   "A backup has been pending for over half an hour, which means the operator cannot start it.",
			Detail: "The operator parks a backup in pending and retries every 30 seconds " +
				"while its target pod is missing or not ready, or while the cluster is " +
				"not healthy enough to back up — including being hibernated. It stays " +
				"pending until the blocker clears.",
			When: diagnose.BackupPhase{AnyOf: []string{"pending"}, MinAge: 30 * time.Minute},
		},
		{
			ID:        "cnpg-backup-wal-archiving",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityCritical,
			Describes: "a Backup blocked by failing WAL archiving",
			Summary:   "A backup is blocked because WAL archiving is not working.",
			Detail: "A base backup is only restorable together with its WAL, so the " +
				"operator refuses to take one while archiving fails. Fixing archiving " +
				"unblocks it.",
			When: diagnose.BackupPhase{AnyOf: []string{"walArchivingFailing"}},
		},
	}
}
