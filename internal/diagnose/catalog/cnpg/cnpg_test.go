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
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/diagnose"
	"github.com/fyannk/pgConsole/internal/observe"
)

var now = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

// inputOn is a cluster whose pods carry the given operator bootstrap
// image tag, which is what decides the catalog's applicability.
func inputOn(operatorVersion string) diagnose.Input {
	major := 17
	return diagnose.Input{
		Now:        now,
		HasCluster: true,
		Cluster: observe.Snapshot{Cluster: observe.ClusterFacts{
			Present:              true,
			PostgresMajorVersion: &major,
		}},
		HasPods: true,
		Pods: observe.PodsSnapshot{Pods: []observe.PodFacts{{
			Name: "orders-1",
			Containers: []observe.ContainerFacts{
				{Name: "bootstrap-controller", Init: true,
					Image: "ghcr.io/cloudnative-pg/cloudnative-pg:" + operatorVersion},
				{Name: "postgres", Image: "ghcr.io/cloudnative-pg/postgresql:17.5"},
			},
		}}},
	}
}

// outcomeOf finds one named check in a result.
func outcomeOf(t *testing.T, result diagnose.Result, name string) diagnose.Check {
	t.Helper()
	for _, check := range result.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("check %q did not account for itself", name)
	return diagnose.Check{}
}

// findingByID finds one finding in a result.
func findingByID(t *testing.T, result diagnose.Result, id string) diagnose.Finding {
	t.Helper()
	for _, finding := range result.Findings {
		if finding.ID == id {
			return finding
		}
	}
	t.Fatalf("finding %q absent: %+v", id, result.Findings)
	return diagnose.Finding{}
}

// TestEveryRuleIsPinned is the catalog's versioning contract: a claim
// read out of specific operator releases must say so. A rule added
// without a CloudNativePG pin would silently assert itself against
// every release, which is exactly what the framework exists to prevent.
func TestEveryRuleIsPinned(t *testing.T) {
	t.Parallel()
	for _, rule := range Rules() {
		pinned := false
		for _, requirement := range rule.Requires {
			if requirement.Component == diagnose.ComponentCNPG {
				pinned = true
			}
		}
		if !pinned {
			t.Errorf("rule %q carries no CloudNativePG pin", rule.ID)
		}
	}
}

// TestSpansApplyPerRelease pins the applicability boundaries the
// verification established: the since128 span covers all three verified
// releases, since129 excludes 1.28, and only130 excludes both older
// ones. One representative rule per span.
func TestSpansApplyPerRelease(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rule    string
		version string
		want    diagnose.CheckOutcome
	}{
		{"cnpg-unrecoverable", "1.28.4", diagnose.CheckClear},
		{"cnpg-unrecoverable", "1.29.2", diagnose.CheckClear},
		{"cnpg-unrecoverable", "1.30.0", diagnose.CheckClear},
		{"cnpg-service-account-missing", "1.28.4", diagnose.CheckNotApplicable},
		{"cnpg-invalid-definition", "1.28.4", diagnose.CheckNotApplicable},
		{"cnpg-invalid-definition", "1.29.2", diagnose.CheckNotApplicable},
		{"cnpg-lease-preempted", "1.29.2", diagnose.CheckNotApplicable},
		{"cnpg-unrecoverable", "1.27.5", diagnose.CheckNotApplicable},
		{"cnpg-unrecoverable", "1.31.0", diagnose.CheckNotApplicable},
	}
	for _, tc := range cases {
		in := inputOn(tc.version)
		if tc.want == diagnose.CheckClear {
			// A clear phase check needs no phase set; the events- and
			// log-backed checks would be unavailable instead, which is
			// why the representative rules are phase- or event-backed
			// with their input observed.
			in.HasEvents = true
		}
		result := diagnose.Run(in, Rules()...)
		if got := outcomeOf(t, result, tc.rule).Outcome; got != tc.want {
			t.Errorf("%s on %s = %v, want %v", tc.rule, tc.version, got, tc.want)
		}
	}
}

// TestArchivingFailureFiresAcrossVerifiedReleases walks the flagship
// rule end to end on each verified release: the finding fires, quoting
// both the operator's condition and the version fact the pin rests on.
func TestArchivingFailureFiresAcrossVerifiedReleases(t *testing.T) {
	t.Parallel()
	for _, version := range []string{"1.28.4", "1.29.2", "1.30.0"} {
		in := inputOn(version)
		in.Cluster.Cluster.Conditions = []observe.Condition{{
			Type: "ContinuousArchiving", Status: "False", Reason: "ContinuousArchivingFailing",
			Message: "unexpected failure invoking barman-cloud-wal-archive: exit status 1",
		}}
		finding := findingByID(t, diagnose.Run(in, Rules()...), "cnpg-wal-archiving-failing")
		if !strings.Contains(finding.Evidence[0].Detail, "barman-cloud-wal-archive") {
			t.Errorf("%s: the archiver's own error is not quoted: %+v", version, finding.Evidence[0])
		}
		last := finding.Evidence[len(finding.Evidence)-1]
		if !strings.Contains(last.Detail, version) {
			t.Errorf("%s: the version fact the pin rests on is not quoted: %+v", version, finding.Evidence)
		}
	}
}

// TestStuckPhaseProducesTheFinding covers the phase family with the
// unrecoverable case, whose reason text is the only place the specific
// cause is written.
func TestStuckPhaseProducesTheFinding(t *testing.T) {
	t.Parallel()
	in := inputOn("1.28.4")
	in.Cluster.Cluster.Phase = "Cluster is unrecoverable and needs manual intervention"
	in.Cluster.Cluster.PhaseReason = "Instance creation failed for the following jobs: orders-2-join"

	finding := findingByID(t, diagnose.Run(in, Rules()...), "cnpg-unrecoverable")
	if !strings.Contains(finding.Evidence[0].Detail, "orders-2-join") {
		t.Errorf("the phase reason is not quoted: %+v", finding.Evidence[0])
	}
}
