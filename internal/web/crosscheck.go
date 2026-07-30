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

package web

import (
	"sort"

	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/observe"
)

// Correlation contract constants.
const (
	// acceptedBarmanPlugin is the one plugin name the evidence contract
	// accepts as a repository-backed method.
	acceptedBarmanPlugin = "barman-cloud.cloudnative-pg.io"
	// methodBarmanObjectStore is the supported in-tree method.
	methodBarmanObjectStore = "barmanObjectStore"
	// methodPlugin is the plugin method; it is repository-backed only
	// with the accepted plugin name.
	methodPlugin = "plugin"
	// completedPhase is the operator's completed backup claim.
	completedPhase = "completed"
	// MaxOrphanRows bounds rendered repository-orphan findings; more
	// orphans set a visible truncation state.
	MaxOrphanRows = 50
)

// Outcome is the typed correlation result of one cross-checked pair.
// The set is closed: the four contract outcomes plus the two honest
// refusals (ambiguous and unknown).
type Outcome string

// The closed outcome set.
const (
	// OutcomeAgreement is the strongest claim this view may make: the
	// operator claim plus structural repository evidence — never
	// restore fitness.
	OutcomeAgreement Outcome = "agreement"
	// OutcomeOperatorOnly is an operator claim with no repository view
	// expected: not a repository-backed method, not completed, or no
	// reported backup ID.
	OutcomeOperatorOnly Outcome = "operator-claim-only"
	// OutcomeDiscrepancy is a first-class finding: the two observers
	// contradict each other.
	OutcomeDiscrepancy Outcome = "discrepancy"
	// OutcomeOrphan is a repository backup no resource accounts for —
	// a finding, not an error.
	OutcomeOrphan Outcome = "repository-orphan"
	// OutcomeAmbiguous is a correlation key matched by more than one
	// object on either side; it is reported, never guessed through.
	OutcomeAmbiguous Outcome = "ambiguous"
	// OutcomeUnknown is a correlation the preconditions do not
	// currently support, with the failing side identified.
	OutcomeUnknown Outcome = "unknown"
)

// CrossCheckRowView is one CNPG Backup beside the repository's
// independent evidence. Both claims keep their origins.
type CrossCheckRowView struct {
	// Name is the Backup resource name.
	Name string
	// OperatorClaim is the attributed operator phase display.
	OperatorClaim string
	// BackupID is the correlation key or "unknown".
	BackupID string
	// RepositoryClaim is the repository's independent state display.
	RepositoryClaim string
	// Outcome is the typed correlation outcome.
	Outcome Outcome
	// Detail explains the outcome in one short clause.
	Detail string
	// Finding marks the row as a first-class finding.
	Finding bool
}

// OrphanRowView is one repository backup no CNPG resource accounts
// for.
type OrphanRowView struct {
	// BackupID is the repository backup identity.
	BackupID string
	// RepositoryClaim is the repository's state display.
	RepositoryClaim string
	// Completed is the repository end time or unknown.
	Completed string
}

// CrossCheckView is the composed backup cross-check section: each
// operator claim beside the repository's independent evidence, with
// discrepancies and orphans as first-class findings.
type CrossCheckView struct {
	// OperatorOrigin attributes the claim column.
	OperatorOrigin Origin
	// RepositoryOrigin attributes the evidence column.
	RepositoryOrigin Origin
	// Degraded is the precondition failure explanation; empty when
	// correlation is current. It always names the failing side.
	Degraded string
	// Rows are the cross-checked CNPG backups, newest first.
	Rows []CrossCheckRowView
	// Orphans are repository backups no resource accounts for.
	Orphans []OrphanRowView
	// OrphansTruncated reports the orphan display bound was reached.
	OrphansTruncated bool
	// OrphansUnknown explains why orphan detection is suppressed;
	// empty when the orphan list is authoritative.
	OrphansUnknown string
	// FindingCount counts discrepancies plus orphans.
	FindingCount int
}

// crossCheckInputs bundles what the correlation reads.
type crossCheckInputs struct {
	backups   observe.BackupsSnapshot
	evidence  evidence.Status
	cluster   observe.Snapshot
	clusterOK bool
}

// buildCrossCheckView correlates the operator's Backup catalog with
// the repository's backup evidence under the contract's correlation
// rules: the observed-UID identity, the operator-supplied mapping
// already enforced at the evidence boundary, and exact backup-ID
// equality — never name similarity, never guessed. Any failed
// precondition degrades every correlation to unknown with the failing
// side identified; both columns keep rendering what each observer
// reports.
func buildCrossCheckView(in crossCheckInputs) *CrossCheckView {
	view := &CrossCheckView{
		OperatorOrigin:   OriginOperator,
		RepositoryOrigin: OriginRepository,
	}
	identityCurrent := clusterIdentityMatch(in.evidence.Snapshot.Report.ClusterUID, in.cluster, in.clusterOK)
	view.Degraded = degradedReason(in, identityCurrent)

	repoByID, repoDuplicates := indexRepoBackups(in.evidence.Snapshot.Backups)
	operatorIDCounts := map[string]int{}
	for _, backup := range in.backups.Backups {
		if backup.BackupID != "" {
			operatorIDCounts[backup.BackupID]++
		}
	}

	accounted := map[string]bool{}
	for _, backup := range in.backups.Backups {
		row := correlateRow(backup, repoByID, repoDuplicates, operatorIDCounts, view.Degraded,
			in.evidence.Snapshot.BackupsTruncated)
		if backup.BackupID != "" {
			accounted[backup.BackupID] = true
		}
		if row.Finding {
			view.FindingCount++
		}
		view.Rows = append(view.Rows, row)
	}

	view.buildOrphans(in, accounted)
	return view
}

// degradedReason names the first failing correlation precondition, or
// returns empty when correlation is current. Staleness of either
// source and a non-current identity each block current conclusions.
func degradedReason(in crossCheckInputs, identity identityMatch) string {
	switch {
	case !in.evidence.HasReport:
		return "repository evidence unavailable — no successful sidecar contact"
	case in.evidence.Snapshot.Report.EvidenceGeneration == 0:
		return "no completed repository scan"
	case identity == identityMismatch:
		return "cluster identity mismatch — the evidence is bound to a different cluster incarnation"
	case identity == identityUnknown:
		return "cluster identity unknown — no observed cluster UID to compare against"
	case in.backups.Stale && (in.evidence.Snapshot.Stale || in.evidence.Snapshot.Report.SourceStale):
		return "both sources stale — operator catalog and repository evidence"
	case in.backups.Stale:
		return "operator catalog stale"
	case in.evidence.Snapshot.Stale:
		return "repository evidence stale (console contact lost)"
	case in.evidence.Snapshot.Report.SourceStale:
		return "repository evidence stale (sidecar-reported)"
	case identity == identityMatchStale:
		return "cluster observation stale — matches are historical, not current agreement"
	default:
		return ""
	}
}

// indexRepoBackups indexes the assembled repository backups by their
// exact backup ID and records duplicated identities.
func indexRepoBackups(items []evidence.RepoBackup) (map[string]evidence.RepoBackup, map[string]bool) {
	byID := make(map[string]evidence.RepoBackup, len(items))
	duplicates := map[string]bool{}
	for _, item := range items {
		if _, exists := byID[item.BackupID]; exists {
			duplicates[item.BackupID] = true
			continue
		}
		byID[item.BackupID] = item
	}
	return byID, duplicates
}

// correlateRow decides one CNPG backup's outcome. Precondition
// failures fix the outcome to unknown while both claims still render.
func correlateRow(backup observe.BackupFacts, repoByID map[string]evidence.RepoBackup, repoDuplicates map[string]bool, operatorIDCounts map[string]int, degraded string, repoTruncated bool) CrossCheckRowView {
	row := CrossCheckRowView{
		Name:            orUnknown(backup.Name),
		OperatorClaim:   orUnknown(backup.Phase),
		BackupID:        orUnknown(backup.BackupID),
		RepositoryClaim: "not observed",
	}
	if item, ok := repoByID[backup.BackupID]; ok && backup.BackupID != "" {
		row.RepositoryClaim = StateLineView{State: item.Result.State, Code: item.Result.Code}.Display()
	}

	repositoryBacked := backup.Method == methodBarmanObjectStore ||
		(backup.Method == methodPlugin && backup.PluginName == acceptedBarmanPlugin)
	switch {
	case degraded != "":
		row.Outcome, row.Detail = OutcomeUnknown, degraded
	case !repositoryBacked:
		row.Outcome, row.Detail = OutcomeOperatorOnly, "not a repository-backed method"
	case backup.Phase != completedPhase:
		row.Outcome, row.Detail = OutcomeOperatorOnly, "not completed — no repository counterpart expected yet"
	case backup.BackupID == "":
		row.Outcome, row.Detail = OutcomeOperatorOnly, "no backup ID reported — nothing to correlate by"
	case operatorIDCounts[backup.BackupID] > 1:
		row.Outcome, row.Detail = OutcomeAmbiguous, "more than one Backup resource carries this backup ID"
	case repoDuplicates[backup.BackupID]:
		row.Outcome, row.Detail = OutcomeAmbiguous, "more than one repository item carries this backup ID"
	default:
		row.Outcome, row.Detail, row.Finding = matchOutcome(backup.BackupID, repoByID, repoTruncated)
	}
	return row
}

// matchOutcome decides the outcome of an unambiguous, current,
// repository-backed completed claim. Absence is a discrepancy only
// when the assembled collection is complete; a truncated collection
// cannot prove absence.
func matchOutcome(backupID string, repoByID map[string]evidence.RepoBackup, repoTruncated bool) (Outcome, string, bool) {
	item, ok := repoByID[backupID]
	if !ok {
		if repoTruncated {
			return OutcomeUnknown, "repository collection truncated — absence cannot be concluded", false
		}
		return OutcomeDiscrepancy, "completed claim without repository artifacts", true
	}
	switch item.Result.State {
	case "healthy", "warning":
		return OutcomeAgreement, "operator claim plus structural repository evidence", false
	case "unhealthy":
		return OutcomeDiscrepancy, "repository evidence contradicts the completed claim", true
	default:
		return OutcomeUnknown, "repository evidence state unknown", false
	}
}

// buildOrphans lists repository backups no CNPG resource accounts
// for. Absence conclusions require both catalogs complete and the
// correlation current; otherwise the orphan list is suppressed with
// the reason stated.
func (v *CrossCheckView) buildOrphans(in crossCheckInputs, accounted map[string]bool) {
	switch {
	case v.Degraded != "":
		v.OrphansUnknown = v.Degraded
		return
	case in.backups.BackupsTruncated:
		v.OrphansUnknown = "operator catalog truncated — absence cannot be concluded"
		return
	case in.evidence.Snapshot.BackupsTruncated:
		v.OrphansUnknown = "repository collection truncated — absence cannot be concluded"
		return
	}
	var orphans []evidence.RepoBackup
	for _, item := range in.evidence.Snapshot.Backups {
		if !accounted[item.BackupID] {
			orphans = append(orphans, item)
		}
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].BackupID > orphans[j].BackupID })
	for _, item := range orphans {
		if len(v.Orphans) >= MaxOrphanRows {
			v.OrphansTruncated = true
			break
		}
		v.Orphans = append(v.Orphans, OrphanRowView{
			BackupID:        item.BackupID,
			RepositoryClaim: StateLineView{State: item.Result.State, Code: item.Result.Code}.Display(),
			Completed:       formatTime(item.EndAt),
		})
	}
	v.FindingCount += len(orphans)
}
