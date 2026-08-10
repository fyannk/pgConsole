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

package diagnose

import (
	"strings"
	"testing"

	"github.com/fyannk/pgConsole/internal/observe"
)

// cnpg130Input is a cluster whose pods carry the 1.30.0 operator
// bootstrap image, which is what makes the pinned catalog applicable.
func cnpg130Input() Input {
	major := 17
	return Input{
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
				{Name: "bootstrap-controller", Init: true, Image: "ghcr.io/cloudnative-pg/cloudnative-pg:1.30.0"},
				{Name: "postgres", Image: "ghcr.io/cloudnative-pg/postgresql:17.5"},
			},
		}}},
	}
}

// TestEveryCNPGRuleIsPinned is the catalog's versioning contract: a
// claim read out of one operator release must say so. A rule added to
// the CloudNativePG catalog without a CloudNativePG pin would silently
// assert itself against every release, which is exactly what the
// framework exists to prevent.
func TestEveryCNPGRuleIsPinned(t *testing.T) {
	t.Parallel()
	for _, rule := range cnpgRules() {
		pinned := false
		for _, requirement := range rule.Requires {
			if requirement.Component == ComponentCNPG {
				pinned = true
			}
		}
		if !pinned {
			t.Errorf("rule %q carries no CloudNativePG pin", rule.ID)
		}
	}
}

// TestCNPGArchivingFailureFiresOnThePinnedRelease walks the flagship
// rule end to end: a 1.30 cluster whose instance manager reports
// archiving as failing produces the finding, quoting both the
// operator's condition and the version fact the pin rests on.
func TestCNPGArchivingFailureFiresOnThePinnedRelease(t *testing.T) {
	t.Parallel()
	in := cnpg130Input()
	in.Cluster.Cluster.Conditions = []observe.Condition{{
		Type: "ContinuousArchiving", Status: "False", Reason: "ContinuousArchivingFailing",
		Message: "unexpected failure invoking barman-cloud-wal-archive: exit status 1",
	}}

	finding := findingByID(t, Run(in), "cnpg-wal-archiving-failing")
	if finding.Severity != SeverityCritical {
		t.Errorf("severity = %v", finding.Severity)
	}
	if !strings.Contains(finding.Evidence[0].Detail, "barman-cloud-wal-archive") {
		t.Errorf("the archiver's own error is not quoted: %+v", finding.Evidence[0])
	}
	last := finding.Evidence[len(finding.Evidence)-1]
	if !strings.Contains(last.Detail, "1.30.0") {
		t.Errorf("the version fact the pin rests on is not quoted: %+v", finding.Evidence)
	}
}

// TestCNPGCatalogDoesNotApplyToOtherReleases proves the other half of
// the pin: on an operator release the catalog was not verified against,
// every CloudNativePG rule steps aside and says so, rather than
// evaluating claims nobody checked there.
func TestCNPGCatalogDoesNotApplyToOtherReleases(t *testing.T) {
	t.Parallel()
	in := cnpg130Input()
	in.Pods.Pods[0].Containers[0].Image = "ghcr.io/cloudnative-pg/cloudnative-pg:1.29.4"
	in.Cluster.Cluster.Phase = "Cluster is unrecoverable and needs manual intervention"

	result := Run(in)
	if len(result.Findings) != 0 {
		t.Fatalf("findings on an unverified release: %+v", result.Findings)
	}
	if got := outcomeOf(t, result, "cnpg-unrecoverable"); got != CheckNotApplicable {
		t.Errorf("outcome = %v on 1.29.4, want does-not-apply", got)
	}
}

// TestCNPGStuckPhaseProducesTheFinding covers the phase family with the
// unrecoverable case, whose reason text is the only place the specific
// cause is written.
func TestCNPGStuckPhaseProducesTheFinding(t *testing.T) {
	t.Parallel()
	in := cnpg130Input()
	in.Cluster.Cluster.Phase = "Cluster is unrecoverable and needs manual intervention"
	in.Cluster.Cluster.PhaseReason = "Instance creation failed for the following jobs: orders-2-join"

	finding := findingByID(t, Run(in), "cnpg-unrecoverable")
	if !strings.Contains(finding.Evidence[0].Detail, "orders-2-join") {
		t.Errorf("the phase reason is not quoted: %+v", finding.Evidence[0])
	}
}
