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

package catalog

import "github.com/fyannk/pgConsole/internal/diagnose"

// repositoryRules restate what the repository-evidence sidecar reports
// about the barman-cloud repository: typed states with stable reason
// codes, nothing more. They are the only rules that read the
// repository's side of the backup story, and they keep the console's
// standing rule about it — an observation is placed beside the
// operator's claim, never merged into it, and no finding here says what
// the repository would or would not restore.
//
// They are unpinned: the sidecar's API is versioned by its own contract,
// which the evidence consumer enforces before a report reaches the
// detectors. A report of an unrecognised variant reads as could-not-run.
func repositoryRules() []diagnose.Rule {
	const restated = "The state and reason code are the sidecar's own, restated; the " +
		"sidecar's diagnostic text stays in the sidecar. Open the evidence " +
		"screen for the full report."
	return []diagnose.Rule{
		{
			ID:        "repository-wal-unhealthy",
			Component: diagnose.ComponentBarman,
			Severity:  diagnose.SeverityCritical,
			Describes: "the repository-evidence sidecar reporting WAL continuity as unhealthy",
			Summary:   "The repository-evidence sidecar reports the archived WAL sequence as unhealthy.",
			Detail: "The sidecar walked the repository's WAL objects and found the " +
				"sequence in a state it classes as unhealthy — the reason code names " +
				"which. An archive with a break in it bounds how far recovery can " +
				"replay, whatever the operator's archiving condition says now. " + restated,
			When: diagnose.RepositoryState{Aspect: diagnose.RepositoryWAL},
			// The operator's condition explains a gap that is still being
			// created; a gap from an earlier outage stands on its own.
			ConsequenceOf: []diagnose.Relation{{Cause: "cnpg-wal-archiving-failing"}},
			Link:          "/backups/evidence",
			LinkLabel:     "Repository evidence",
		},
		{
			ID:        "repository-coverage-unhealthy",
			Component: diagnose.ComponentBarman,
			Severity:  diagnose.SeverityCritical,
			Describes: "the repository-evidence sidecar reporting recovery coverage as unhealthy",
			Summary:   "The repository-evidence sidecar reports the observed recovery coverage as unhealthy.",
			Detail: "Coverage is the sidecar's traversal from each backup through the " +
				"WAL that follows it; unhealthy means it found no path it classes as " +
				"usable from the backups it examined. " + restated,
			When: diagnose.RepositoryState{Aspect: diagnose.RepositoryCoverage},
			ConsequenceOf: []diagnose.Relation{
				{Cause: "repository-wal-unhealthy"}, {Cause: "cnpg-wal-archiving-failing"}},
			Link:      "/backups/evidence",
			LinkLabel: "Repository evidence",
		},
		{
			ID:        "repository-retention-unhealthy",
			Component: diagnose.ComponentBarman,
			Severity:  diagnose.SeverityWarning,
			Describes: "the repository-evidence sidecar reporting retention as unhealthy",
			Summary:   "The repository-evidence sidecar reports retention as unhealthy against the configured expectation.",
			Detail: "The sidecar compares the backups it can see against the redundancy " +
				"the operator configured for it. Unhealthy means the repository " +
				"holds fewer structurally usable backups than expected. " + restated,
			When:      diagnose.RepositoryState{Aspect: diagnose.RepositoryRetention},
			Link:      "/backups/evidence",
			LinkLabel: "Repository evidence",
		},
	}
}
