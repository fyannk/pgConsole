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

import "github.com/fyannk/pgConsole/internal/diagnose"

// metricRules cover the exporter flags whose non-zero value is a
// standing operator state — states that are otherwise only visible in
// annotations the console does not read.
func metricRules() []diagnose.Rule {
	return []diagnose.Rule{
		{
			ID:        "cnpg-instance-fenced",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityWarning,
			Describes: "an instance the operator has fenced",
			Summary:   "An instance is fenced: PostgreSQL is deliberately stopped there and the operator will not restart it.",
			Detail: "Fencing is intentional and meant to be temporary. A fenced replica is " +
				"one fewer member than the cluster's count suggests; a fenced primary " +
				"is an outage.",
			When:      diagnose.InstantNonZero{Key: "fencing-on"},
			Pinned:    []string{"fencing_on"},
			Link:      "/cluster/metrics",
			LinkLabel: "Metrics",
		},
		{
			ID:        "cnpg-switchover-required",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityWarning,
			Describes: "an instance reporting that a manual switchover is required",
			Summary:   "An instance reports that a manual switchover is required before pending work resumes.",
			Detail: "The exporter's own word for the supervised-strategy wait, from inside " +
				"the instance — the operator-side view of the same state is the " +
				"waiting-for-user phase check.",
			When:      diagnose.InstantNonZero{Key: "switchover-required"},
			Pinned:    []string{"switchover_required"},
			Link:      "/cluster/metrics",
			LinkLabel: "Metrics",
		},
		{
			// The one rule that compares two sources rather than reading
			// one: the operator's currentPrimary against each instance's
			// own pg_is_in_recovery(), which the exporter publishes under
			// the pinned metric name. Neither side is presumed right — the
			// finding is the contradiction itself. A primary move in flight
			// is excluded, because then the operator is the one saying
			// roles are changing.
			ID:        "cnpg-primary-disagreement",
			Component: diagnose.ComponentCNPG,
			Requires:  pin(since128),
			Severity:  diagnose.SeverityCritical,
			Describes: "an instance whose own recovery state contradicts the operator's current primary",
			Summary:   "An instance and the operator disagree about which instance is the primary.",
			Detail: "PostgreSQL's own answer to pg_is_in_recovery() on the instance does not " +
				"match the role the Cluster status assigns it, with no primary move in " +
				"flight to explain the lag. Two instances accepting writes is a split " +
				"brain; a named primary that is in recovery is a cluster with no " +
				"primary at all. Both quoted claims are current readings; the " +
				"disagreement, not either side, is the finding.",
			When:   diagnose.PrimaryDisagreement{},
			Pinned: []string{"in_recovery"},
			NextSteps: "Read the instance's log and the operator's log before touching " +
				"anything: a stale exporter reading clears on the next scrape, while a " +
				"real split brain needs the extra writer fenced. Do not promote or " +
				"demote by hand while the two sources still disagree.",
			Link:      "/cluster/metrics",
			LinkLabel: "Metrics",
		},
	}
}
