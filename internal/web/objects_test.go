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

// objectsPage builds a page with one object of every grouped kind, each
// watch carrying a deliberately different generation so a freshness
// borrowed from the wrong source is visible as a wrong number.
func objectsPage(t *testing.T) Page {
	t.Helper()
	src := wiringSources()
	port := int32(5432)
	return buildPage(context.Background(), "orders", "payments", snapshots{
		window: time.Hour, cluster: src.snap, ok: true,
		pods: src.pods, podsOK: true,
		backups: observe.BackupsSnapshot{
			Generation: 60, ObservedAt: testNow.Add(-time.Second),
			Backups:          []observe.BackupFacts{{Name: "orders-first", UID: "b1", Phase: "completed", Method: "plugin"}},
			ScheduledBackups: []observe.ScheduledBackupFacts{{Name: "daily", UID: "sb1", Method: "plugin", Schedule: "0 0 2 * * *"}},
		},
		backupsOK: true,
		poolers: observe.PoolersSnapshot{
			Generation: 70, ObservedAt: testNow.Add(-time.Second),
			Poolers: []observe.PoolerFacts{{Name: "orders-pool-rw", UID: "p1", Type: "rw", PoolMode: "transaction", Phase: "active"}},
		},
		poolersOK: true,
		declared: observe.DatabaseObjectsSnapshot{
			Generation: 80, ObservedAt: testNow.Add(-time.Second),
			Databases: []observe.DatabaseFacts{{Name: "orders-db", UID: "d1", Database: "orders", Owner: "app"}},
		},
		declaredOK: true,
		infra: observe.InfrastructureSnapshot{
			Generation: 90, ObservedAt: testNow.Add(-time.Second),
			Services: []observe.ServiceFacts{
				{Name: "orders-rw", UID: "s1", Role: "read-write", Type: "ClusterIP", ClusterIP: "10.0.0.1", Port: &port},
			},
			Volumes: []observe.VolumeFacts{
				{Name: "orders-1", UID: "v1", Instance: "orders-1", Role: "PG_DATA", Phase: "Bound", Capacity: "1Gi"},
			},
			// Deliberately not observable, so the screen must say it has
			// no word rather than render an empty list.
			SnapshotsObservable: false,
			ChildrenUnobserved:  []string{"Secret"},
		},
		infraOK: true,
	}, testNow, Links{})
}

// The inventory groups by the resource an object belongs to, in the
// reading order an operator reasons about.
func TestObjectsGroupsByOwningResource(t *testing.T) {
	t.Parallel()
	page := objectsPage(t)
	if page.Objects == nil {
		t.Fatal("no inventory was built")
	}
	var parents []string
	for _, g := range page.Objects.Groups {
		parents = append(parents, g.Parent)
	}
	want := "Cluster,Backup / ScheduledBackup,Pooler,Database"
	if got := strings.Join(parents, ","); got != want {
		t.Errorf("groups = %s, want %s", got, want)
	}
}

// Each kind states the freshness of the watch that reported it. One
// number over the whole screen would be a claim no single source
// supports, so a kind must never show a neighbour's generation.
func TestObjectsStatesEachWatchesOwnFreshness(t *testing.T) {
	t.Parallel()
	page := objectsPage(t)
	gen := map[string]string{}
	for _, g := range page.Objects.Groups {
		for _, k := range g.Kinds {
			gen[k.Kind] = k.Meta.Generation
		}
	}
	for kind, want := range map[string]string{
		"Backups":       "60",
		"Poolers":       "70",
		"Databases":     "80",
		"Services":      "90",
		"Volume claims": "90",
	} {
		if got := gen[kind]; got != want {
			t.Errorf("%s reports generation %q, want its own watch's %q", kind, got, want)
		}
	}
	// The cluster watch keeps its own freshness too — the claim the
	// topbar used to carry, now stated beside the sections it must not
	// be confused with.
	if gen["Cluster"] == "" {
		t.Error("the cluster watch's freshness is stated nowhere on the inventory")
	}
}

// A kind the console could not read is not an empty kind. Rendering the
// two the same way would turn "no permission" into "none exist".
func TestObjectsSeparatesUnreadableFromEmpty(t *testing.T) {
	t.Parallel()
	page := objectsPage(t)
	byKind := map[string]ObjectKindView{}
	for _, g := range page.Objects.Groups {
		for _, k := range g.Kinds {
			byKind[k.Kind] = k
		}
	}

	secret, ok := byKind["Secret"]
	if !ok {
		t.Fatal("an unreadable kind vanished from the inventory instead of being named")
	}
	if secret.Observed {
		t.Error("an unreadable kind claims to have been observed")
	}
	if secret.Note == "" {
		t.Error("an unreadable kind offers no explanation")
	}

	snaps, ok := byKind["Volume snapshots"]
	if !ok {
		t.Fatal("volume snapshots vanished from the inventory")
	}
	if snaps.Observed {
		t.Error("volume snapshots claim observation while the resource is unobservable")
	}

	// An observed-but-empty kind is the other half of the distinction:
	// it is observed, and it says plainly that it holds nothing.
	pubs := byKind["Publications"]
	if !pubs.Observed {
		t.Error("an observed empty kind reads as unobserved")
	}
	if len(pubs.Rows) != 0 || pubs.Note == "" {
		t.Error("an observed empty kind does not state its emptiness")
	}
}

// The heading counts what was seen without turning an unread kind into
// a confirmed zero.
func TestObjectsSummaryDoesNotCountUnreadAsZero(t *testing.T) {
	t.Parallel()
	page := objectsPage(t)
	for _, g := range page.Objects.Groups {
		if g.Parent != "Cluster" {
			continue
		}
		if !strings.Contains(g.Summary(), "unread") {
			t.Errorf("cluster summary %q hides that a kind could not be read", g.Summary())
		}
		return
	}
	t.Fatal("no cluster group was built")
}
