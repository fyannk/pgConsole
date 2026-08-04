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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/observe"
)

// wiringSources is a cluster with a primary and two replicas on
// distinct nodes, a quorum naming one replica, and a backup schedule.
func wiringSources() staticSnapshots {
	replica2 := memberPod("orders-2", "replica")
	replica2.Node = "node-b"
	replica3 := memberPod("orders-3", "replica")
	replica3.Node = "node-c"
	return staticSnapshots{
		snap:   observe.Snapshot{Generation: 7, ObservedAt: testNow.Add(-3 * time.Second), Cluster: healthyFacts()},
		ok:     true,
		pods:   podsSnapshot(false, memberPod("orders-1", "primary"), replica2, replica3),
		podsOK: true,
		quorum: observe.FailoverQuorumSnapshot{
			Generation: 5, ObservedAt: testNow.Add(-2 * time.Second),
			Quorum: observe.FailoverQuorumFacts{
				Present: true, Method: "quorum", Primary: "orders-1",
				StandbyNumber: 1, Standbys: []string{"orders-2"},
			},
		},
		quorumOK: true,
		backups: observe.BackupsSnapshot{
			Generation: 6, ObservedAt: testNow.Add(-2 * time.Second),
			ScheduledBackups: []observe.ScheduledBackupFacts{{
				Name: "nightly", Method: "barmanObjectStore", Schedule: "0 0 2 * * *",
			}},
		},
		backupsOK: true,
	}
}

func TestClusterOverviewRendersWiringPlacementAndQuorum(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, wiringSources(), kube.FakeProber{}, Links{})
	rec := get(t, h, http.MethodGet, "/cluster/overview")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	// The served drawing is real geometry with the DBA rows, not an
	// empty frame for the enhancement script to fill.
	for _, want := range []string{
		"Physical wiring", `<svg class="topo"`, `<rect x=`,
		"orders-1 — primary", "node node-a",
		"potentially synchronous (quorum)", "not in the reported standby set",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("wiring misses %q", want)
		}
	}

	// The drawing is finished server-side: every box placed, every flow
	// routed as an orthogonal run, no script involved.
	// A layout that failed to place anything would stack every box on
	// one spot; distinct positions prove it ran.
	if positions := topoRectPositions(body); len(positions) < 2 {
		t.Errorf("diagram has %d boxes", len(positions))
	} else {
		seen := map[string]bool{}
		for _, p := range positions {
			seen[p] = true
		}
		if len(seen) != len(positions) {
			t.Errorf("%d boxes share %d positions, so the layout did not place them",
				len(positions), len(seen))
		}
	}
	routes := topoPaths(body)
	if len(routes) == 0 {
		t.Error("no flow was routed")
	}
	for _, path := range routes {
		if strings.Contains(path, "C") {
			t.Errorf("route is a cubic curve rather than an orthogonal run: %q", path)
		}
	}

	// Placement, replication and the backup path are attributed panels.
	for _, want := range []string{
		"Observed placement", "No two instances share a node.",
		"source: Kubernetes-reported pod placement",
		"Failover quorum", "Standbys awaited",
		"ScheduledBackup resources", "0 0 2 * * *",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page misses %q", want)
		}
	}
}

func TestInstanceConditionReportsRestartsOnlyWhenThereAreSome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ restarts, want string }{
		{"0", "Running · ready"},
		{"", "Running · ready"},
		{unknown, "Running · ready"},
		{"1", "Running · ready · 1 restart"},
		{"4", "Running · ready · 4 restarts"},
	} {
		got := instanceCondition(PodRowView{Phase: "Running", Ready: "true", Restarts: tc.restarts})
		if got != tc.want {
			t.Errorf("restarts %q = %q, want %q", tc.restarts, got, tc.want)
		}
	}
}

func TestClusterOverviewDerivesSharedNodeFinding(t *testing.T) {
	t.Parallel()
	src := wiringSources()
	// Both replicas land on the primary's node.
	src.pods = podsSnapshot(false, memberPod("orders-1", "primary"), memberPod("orders-2", "replica"))
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/cluster/overview").Body.String()
	if !strings.Contains(body, "orders-1 and orders-2 share node node-a") {
		t.Fatal("shared-node placement did not surface as a finding")
	}
}

func TestClusterOverviewStatesAbsenceWithoutSnapshot(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, EmptySnapshots{}, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/cluster/overview").Body.String()
	if !strings.Contains(body, "No cluster snapshot yet") {
		t.Fatal("empty state is not stated")
	}
	if strings.Contains(body, "Physical wiring") {
		t.Fatal("a wiring diagram rendered with nothing observed")
	}
}
