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
			Link:      "/cluster/metrics",
			LinkLabel: "Metrics",
		},
	}
}
