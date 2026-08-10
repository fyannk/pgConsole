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
	"testing"

	"github.com/fyannk/pgConsole/internal/observe"
)

// TestSatisfiesPinsTheConstraintGrammar covers each operator, the
// clause conjunction, and the closed failure mode: anything unreadable
// rules the version out rather than in.
func TestSatisfiesPinsTheConstraintGrammar(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		version, constraint string
		want                bool
	}{
		{"1.25.1", ">=1.24 <1.26", true},
		{"1.26", ">=1.24 <1.26", false},
		{"1.26.0", "<=1.26", true},
		{"17", "<14", false},
		{"13", "<14", true},
		{"17", "=17", true},
		{"17.5", "17", false},       // bare version means equality, and 17.5 != 17.0
		{"17", "17", true},          // operatorless clause reads as "="
		{"1.25", "!=1.25.0", false}, // missing fields compare as zero
		{"1.25.1", ">1.25 <2", true},
		{"anything", "", false},      // unparseable version fails closed
		{"1.2.3", "", true},          // empty constraint holds
		{"1.2.3", ">=latest", false}, // unparseable clause fails closed
	} {
		if got := satisfies(tc.version, tc.constraint); got != tc.want {
			t.Errorf("satisfies(%q, %q) = %v, want %v", tc.version, tc.constraint, got, tc.want)
		}
	}
}

// TestImageTagVersionReadsOnlyTheVersionPart proves the parser takes
// the leading numerals of a tag and nothing else: not a digest, not a
// registry port, not a build-variant suffix.
func TestImageTagVersionReadsOnlyTheVersionPart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		image, want string
		ok          bool
	}{
		{"ghcr.io/cloudnative-pg/plugin-barman-cloud-sidecar:v0.6.0", "0.6.0", true},
		{"ghcr.io/cloudnative-pg/postgresql:17.5-standard-bookworm", "17.5", true},
		{"ghcr.io/x/y:latest", "", false},
		{"ghcr.io/x/y@sha256:abcd", "", false},
		{"registry:5000/x/y", "", false}, // the colon is a port, not a tag
		{"registry:5000/x/y:1.2", "1.2", true},
		{"bare-image", "", false},
	} {
		got, ok := imageTagVersion(tc.image)
		if got != tc.want || ok != tc.ok {
			t.Errorf("imageTagVersion(%q) = %q, %v, want %q, %v", tc.image, got, ok, tc.want, tc.ok)
		}
	}
}

// TestVersionFactsAreObservedNeverAssumed proves each derivable fact
// appears only when its snapshot reports it, and carries provenance a
// finding can quote.
func TestVersionFactsAreObservedNeverAssumed(t *testing.T) {
	t.Parallel()

	if facts := versionFacts(Input{Now: now}); len(facts) != 0 {
		t.Errorf("versions derived from nothing observed: %+v", facts)
	}

	major := 17
	in := Input{
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
				{Name: "postgres", Image: "ghcr.io/cloudnative-pg/postgresql:17.5"},
				{Name: "plugin-barman-cloud", Image: "ghcr.io/cloudnative-pg/plugin-barman-cloud-sidecar:v0.6.0"},
			},
		}}},
	}
	facts := versionFacts(in)

	postgres, ok := facts[ComponentPostgreSQL]
	if !ok || postgres.Version != "17" {
		t.Fatalf("PostgreSQL version = %+v, want 17 from the operator's status", postgres)
	}
	if postgres.Origin != "operator-reported" {
		t.Errorf("PostgreSQL origin = %q", postgres.Origin)
	}

	barman, ok := facts[ComponentBarman]
	if !ok || barman.Version != "0.6.0" {
		t.Fatalf("Barman version = %+v, want 0.6.0 from the sidecar image", barman)
	}
	if barman.Detail == "" || barman.Object == "" {
		t.Errorf("Barman fact carries no provenance: %+v", barman)
	}

	// Neither the operator nor Kubernetes is observable yet; asserting a
	// version for them would gate pinned rules on an assumption.
	if _, ok := facts[ComponentCNPG]; ok {
		t.Error("CloudNativePG version asserted without a source")
	}
	if _, ok := facts[ComponentKubernetes]; ok {
		t.Error("Kubernetes version asserted without a source")
	}
}
