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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/observe"
)

// sectionsPage assembles a page carrying every section the three
// drawings read: schedules and backups, poolers with owned pods, and
// the four declared kinds.
func sectionsPage(t *testing.T) Page {
	t.Helper()
	src := wiringSources()
	ready := true
	restarts := 0
	return buildPage(context.Background(), "orders", "payments", snapshots{
		window: time.Hour, cluster: src.snap, ok: true,
		pods: src.pods, podsOK: true,
		backups: observe.BackupsSnapshot{
			Generation: 6, ObservedAt: testNow.Add(-2 * time.Second),
			Backups: []observe.BackupFacts{
				{Name: "orders-first", UID: "b1", Phase: "completed", Method: "plugin"},
			},
			ScheduledBackups: []observe.ScheduledBackupFacts{
				{Name: "daily", UID: "sb1", Method: "plugin", Schedule: "0 0 2 * * *"},
			},
		},
		backupsOK: true,
		poolers: observe.PoolersSnapshot{
			Generation: 4, ObservedAt: testNow.Add(-time.Second),
			Poolers: []observe.PoolerFacts{
				{Name: "orders-pool-rw", UID: "p1", Type: "rw", PoolMode: "transaction", Phase: "active"},
			},
		},
		poolersOK: true,
		poolerPods: observe.PodsSnapshot{
			Generation: 5, ObservedAt: testNow.Add(-time.Second),
			Pods: []observe.PodFacts{
				{Name: "orders-pool-rw-abc-1", UID: "pp1", Role: "orders-pool-rw", Phase: "Running",
					Ready: &ready, Restarts: &restarts, Node: "node-a"},
			},
		},
		poolerPodsOK: true,
		declared:     declaredFixture(),
		declaredOK:   true,
	}, testNow, Links{})
}

// The backups drawing states the whole path: the schedule triggers the
// cluster, the archive reaches the object store, and the catalog frame
// records what the operator reports.
func TestBackupsDrawingStatesThePath(t *testing.T) {
	t.Parallel()
	page := sectionsPage(t)
	view := page.BackupsDrawing
	if view == nil {
		t.Fatal("no backups drawing was built")
	}
	var labels []string
	for _, f := range view.Frames {
		labels = append(labels, f.Label)
	}
	joined := strings.Join(labels, ",")
	for _, want := range []string{"Backup schedules", "Backups", "Object storage"} {
		if !strings.Contains(joined, want) {
			t.Errorf("frames = %s, missing %s", joined, want)
		}
	}
	// Every wire on this drawing is the one backup style.
	for _, e := range view.Edges {
		if e.Kind != "archive" {
			t.Errorf("edge kind %q on the backup path", e.Kind)
		}
	}
	if len(view.Edges) < 3 {
		t.Errorf("%d wires, want the trigger, the archive and the records", len(view.Edges))
	}
	// Nothing on the path means no drawing, not an empty frame set.
	empty := buildPage(context.Background(), "orders", "payments", snapshots{
		window: time.Hour, cluster: wiringSources().snap, ok: true,
		pods: wiringSources().pods, podsOK: true,
	}, testNow, Links{})
	if empty.BackupsDrawing != nil {
		t.Error("a page with no backup evidence drew a backup path")
	}
}

// The poolers drawing wires each pod to the pooler that owns it, using
// the ownership the roster already proved — and only that.
func TestPoolersDrawingWiresPodsToTheirPooler(t *testing.T) {
	t.Parallel()
	page := sectionsPage(t)
	view := page.PoolersDrawing
	if view == nil {
		t.Fatal("no poolers drawing was built")
	}
	owns := 0
	for _, l := range view.Graph.Links {
		if l.Kind == "owns" {
			owns++
		}
	}
	if owns != 1 {
		t.Errorf("%d ownership wires, want exactly the one proved pod", owns)
	}
	var labels []string
	for _, f := range view.Frames {
		labels = append(labels, f.Label)
	}
	if joined := strings.Join(labels, ","); !strings.Contains(joined, "Pooler pods") ||
		!strings.Contains(joined, "Poolers") || !strings.Contains(joined, "Cluster") {
		t.Errorf("frames = %s", joined)
	}
	// No poolers, no drawing: the screen keeps its empty state.
	empty := buildPage(context.Background(), "orders", "payments", snapshots{
		window: time.Hour, cluster: wiringSources().snap, ok: true,
		pods: wiringSources().pods, podsOK: true,
	}, testNow, Links{})
	if empty.PoolersDrawing != nil {
		t.Error("a page without poolers drew a client path")
	}
}

// The databases drawing wires publications and subscriptions to the
// database they declare against, and carries the reconciliation
// verdict as the box state.
func TestDatabasesDrawingWiresDeclarationsToTheirDatabase(t *testing.T) {
	t.Parallel()
	page := sectionsPage(t)
	view := page.DatabasesDrawing
	if view == nil {
		t.Fatal("no databases drawing was built")
	}
	kinds := map[string]int{}
	for _, l := range view.Graph.Links {
		kinds[l.Kind]++
	}
	if kinds["owns"] != 1 || kinds["replicate"] != 1 {
		t.Errorf("links = %v, want one publication and one subscription wire", kinds)
	}
	states := map[string]string{}
	for _, n := range view.Nodes {
		states[n.ID] = n.State
	}
	if states["db-0"] != "current" {
		t.Errorf("an applied database carries state %q, want current", states["db-0"])
	}
	if states["role-0"] != "degraded" {
		t.Errorf("a failed role carries state %q, want degraded", states["role-0"])
	}
	if states["sub-0"] != unknown {
		t.Errorf("an unreported subscription carries state %q, want unknown", states["sub-0"])
	}
}
