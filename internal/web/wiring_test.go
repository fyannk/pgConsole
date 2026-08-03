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
	"encoding/json"
	"html"
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

	// The graph the enhancement re-lays out carries the pinned tiers and
	// the lines[] rows, and names only boxes it defines.
	raw := body[strings.Index(body, `data-topo="`)+len(`data-topo="`):]
	raw = html.UnescapeString(raw[:strings.Index(raw, `"`)])
	var graph TopoGraph
	if err := json.Unmarshal([]byte(raw), &graph); err != nil {
		t.Fatalf("graph is not valid JSON: %v", err)
	}
	if len(graph.TierX) != 4 {
		t.Errorf("graph tierX = %v, want 4 pinned columns", graph.TierX)
	}
	ids := map[string]bool{}
	for _, n := range graph.Nodes {
		if len(n.Lines) == 0 {
			t.Errorf("graph node %s has no lines", n.ID)
		}
		ids[n.ID] = true
	}
	for _, l := range graph.Links {
		if !ids[l.Source] || !ids[l.Target] {
			t.Errorf("link %s -> %s names a box the graph does not carry", l.Source, l.Target)
		}
	}
	if got, want := strings.Count(body, `class="topo-node`), len(graph.Nodes); got != want {
		t.Errorf("the drawing has %d boxes, the graph has %d", got, want)
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
