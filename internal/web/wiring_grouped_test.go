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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/observe"
)

// groupedPage builds a page with everything the grouped drawing can
// show: two poolers, two services, a primary with two replicas, one
// claim per instance, a backup schedule and a backup path.
func groupedPage(t *testing.T) Page {
	t.Helper()
	src := wiringSources()
	port := int32(5432)
	return buildPage(context.Background(), "orders", "payments", snapshots{
		window: time.Hour, cluster: src.snap, ok: true,
		pods: src.pods, podsOK: true,
		quorum: src.quorum, quorumOK: true,
		backups: observe.BackupsSnapshot{
			Generation: 6, ObservedAt: testNow.Add(-2 * time.Second),
			// The operator served this backup from a replica, which is
			// CloudNativePG's default target — the case the drawing must
			// not redraw as if it had left the primary.
			Backups: []observe.BackupFacts{{
				Name: "orders-first", UID: "b1", Phase: "completed", Method: "plugin",
				PluginName: "barman-cloud.cloudnative-pg.io", SourceInstance: "orders-2",
			}},
			ScheduledBackups: []observe.ScheduledBackupFacts{
				{Name: "daily", UID: "sb1", Method: "plugin", Schedule: "0 0 2 * * *", Suspended: boolp(false)},
			},
		},
		backupsOK: true,
		poolers: observe.PoolersSnapshot{
			Generation: 4, ObservedAt: testNow.Add(-time.Second),
			Poolers: []observe.PoolerFacts{
				{Name: "orders-pool-ro", UID: "p2", Type: "ro", PoolMode: "session", Phase: "active"},
				{Name: "orders-pool-rw", UID: "p1", Type: "rw", PoolMode: "transaction", Phase: "active"},
			},
		},
		poolersOK: true,
		infra: observe.InfrastructureSnapshot{
			Generation: 3, ObservedAt: testNow.Add(-time.Second),
			Services: []observe.ServiceFacts{
				{Name: "orders-rw", UID: "s1", Role: "read-write", Type: "ClusterIP", ClusterIP: "10.0.0.1", Port: &port},
				{Name: "orders-ro", UID: "s2", Role: "read-only", Type: "ClusterIP", ClusterIP: "10.0.0.2", Port: &port},
			},
			Volumes: []observe.VolumeFacts{
				{Name: "orders-1", UID: "v1", Instance: "orders-1", Role: "PG_DATA", Phase: "Bound", Capacity: "1Gi", StorageClass: "standard"},
				{Name: "orders-2", UID: "v2", Instance: "orders-2", Role: "PG_DATA", Phase: "Bound", Capacity: "1Gi", StorageClass: "standard"},
				{Name: "orders-3", UID: "v3", Instance: "orders-3", Role: "PG_DATA", Phase: "Bound", Capacity: "1Gi", StorageClass: "standard"},
			},
		},
		infraOK: true,
	}, testNow, Links{})
}

// The grouped drawing's placement is a set of stated rules, so each one
// is asserted as geometry: rw above ro, the primary left of its
// replicas, the claims right of the instances, the object store and
// snapshots on top of the storage column.
func TestGroupedWiringPinsEveryStatedPlacement(t *testing.T) {
	t.Parallel()
	page := groupedPage(t)
	view := buildGroupedWiring(&page)
	if view == nil {
		t.Fatal("no grouped drawing was built")
	}

	byID := map[string]*TopoNode{}
	name := func(n *TopoNode) string {
		for _, l := range n.Lines {
			if l.Class == "label" {
				return l.Text
			}
		}
		return ""
	}
	byName := map[string]*TopoNode{}
	for i := range view.Nodes {
		n := &view.Nodes[i]
		byID[n.ID] = n
		byName[name(n)] = n
	}

	above := func(top, bottom string) {
		t.Helper()
		a, b := byName[top], byName[bottom]
		if a == nil || b == nil {
			t.Fatalf("%q or %q is not drawn", top, bottom)
		}
		if a.Y >= b.Y {
			t.Errorf("%s (y=%d) is not above %s (y=%d)", top, a.Y, bottom, b.Y)
		}
	}
	leftOf := func(l, r string) {
		t.Helper()
		a, b := byName[l], byName[r]
		if a == nil || b == nil {
			t.Fatalf("%q or %q is not drawn", l, r)
		}
		if a.X+a.W > b.X {
			t.Errorf("%s does not stand left of %s", l, r)
		}
	}

	// rw above ro, for poolers and services alike.
	above("orders-pool-rw", "orders-pool-ro")
	above("orders-rw", "orders-ro")
	// The primary left of its replicas, replicas left of their claims.
	leftOf("orders-1 — primary", "orders-2 — replica")
	leftOf("orders-2 — replica", "Cloud backup")
	// The top band: the schedule above the primary and centred on it,
	// the object store above every claim.
	above("daily", "orders-1 — primary")
	above("Cloud backup", "orders-1")
	if sched, prim := byName["daily"], byName["orders-1 — primary"]; sched == nil || prim == nil {
		t.Fatal("the schedule or the primary is not drawn")
	} else if sc, pc := sched.X+sched.W/2, prim.X+prim.W/2; sc != pc {
		t.Errorf("the schedule (centre %d) is not centred on the primary (centre %d)", sc, pc)
	}
	// Each claim is level with its instance, so the volume wire is
	// straight: same centre, and the edge path carries no corner.
	for _, pair := range [][2]string{
		{"orders-1 — primary", "orders-1"},
		{"orders-2 — replica", "orders-2"},
		{"orders-3 — replica", "orders-3"},
	} {
		inst, claim := byName[pair[0]], byName[pair[1]]
		if inst == nil || claim == nil {
			t.Fatalf("%q or %q is not drawn", pair[0], pair[1])
		}
		if instC, claimC := inst.Y+inst.H/2, claim.Y+claim.H/2; instC != claimC {
			t.Errorf("%s (centre %d) is not level with its claim (centre %d)", pair[0], instC, claimC)
		}
	}
	// Consecutive instances stagger their claims over two columns, and
	// the columns alternate: first and third left, second right.
	if c1, c2, c3 := byName["orders-1"], byName["orders-2"], byName["orders-3"]; c1.X >= c2.X {
		t.Errorf("the second claim column (x=%d) does not stand right of the first (x=%d)", c2.X, c1.X)
	} else if c1.X != c3.X {
		t.Errorf("the third claim (x=%d) does not return to the first column (x=%d)", c3.X, c1.X)
	}

	// Five dotted frames, each carrying its domain kind.
	var labels []string
	kinds := map[string]string{}
	notes := map[string]string{}
	for _, f := range view.Frames {
		labels = append(labels, f.Label)
		kinds[f.Label] = f.Kind
		notes[f.Label] = f.Note
	}
	if got, want := strings.Join(labels, ","), "Poolers,Cluster,Backups,Kubernetes storage,Object storage"; got != want {
		t.Errorf("frames = %s, want %s", got, want)
	}
	for label, kind := range map[string]string{
		"Poolers": "pool", "Cluster": "cluster", "Backups": "backup",
		"Kubernetes storage": "store", "Object storage": "store",
	} {
		if kinds[label] != kind {
			t.Errorf("frame %s carries kind %q, want %q", label, kinds[label], kind)
		}
	}
	// No endpoint was observed, so the object frame carries no note.
	if notes["Object storage"] != "" {
		t.Errorf("object frame note = %q with no endpoint observed", notes["Object storage"])
	}
	// Frames do not overlap each other.
	for i := 0; i < len(view.Frames); i++ {
		for j := i + 1; j < len(view.Frames); j++ {
			a, b := view.Frames[i], view.Frames[j]
			if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
				t.Errorf("frame %s overlaps frame %s", a.Label, b.Label)
			}
		}
	}

	// The drawing is taller than the ungrouped one's 3.6:1 shape, even
	// in this fixture's snapshotless case; snapshots and extra replicas
	// only push it taller.
	if ratio := float64(view.Width) / float64(view.Height); ratio > 3.3 {
		t.Errorf("aspect ratio %.2f is flatter than the stated 3.3", ratio)
	}
}

// A fanned flow leaves its box once: every replication branch shares
// the primary's single exit, every disk wire is straight, and each tee
// carries a dot in the flow's own style.
func TestGroupedWiringTrunksItsFans(t *testing.T) {
	t.Parallel()
	page := groupedPage(t)
	view := buildGroupedWiring(&page)
	if view == nil {
		t.Fatal("no grouped drawing was built")
	}

	kinds := map[string][]TopoEdge{}
	for i, e := range view.Edges {
		kinds[e.Kind] = append(kinds[e.Kind], e)
		if strings.Contains(e.Path, "C") {
			t.Errorf("edge %d is a cubic curve rather than an orthogonal run", i)
		}
	}

	// Two replicas, two replication branches, one shared exit: the
	// trunk prefix of both paths is byte-identical.
	repl := kinds["replicate"]
	if len(repl) != 2 {
		t.Fatalf("%d replication branches, want 2", len(repl))
	}
	prefix := func(path string) string {
		return path[:strings.Index(path, " L")]
	}
	if prefix(repl[0].Path) != prefix(repl[1].Path) {
		t.Errorf("replication branches leave from different points: %q vs %q",
			prefix(repl[0].Path), prefix(repl[1].Path))
	}
	if repl[0].Label != "replication" && repl[1].Label != "replication" {
		t.Error("no replication branch carries the label")
	}

	// The read fan shares its exit the same way.
	reads := kinds["read"]
	if len(reads) < 3 { // one pooler wire, two replica branches
		t.Fatalf("%d read wires, want at least 3", len(reads))
	}

	// Volume wires are straight lines: two points, no corner.
	disks := kinds["disk"]
	if len(disks) != 3 {
		t.Fatalf("%d volume wires, want 3", len(disks))
	}
	for _, d := range disks {
		if strings.Contains(d.Path, "Q") {
			t.Errorf("a volume wire turns a corner: %q", d.Path)
		}
	}

	// The backup path is two archive wires — the schedule into the
	// cluster, the attributed instance into the object store — and the
	// lone schedule's drop collapses to one straight vertical line.
	archives := kinds["archive"]
	if len(archives) != 2 {
		t.Fatalf("%d archive wires, want 2", len(archives))
	}
	straight := 0
	for _, a := range archives {
		if !strings.Contains(a.Path, "Q") {
			straight++
		}
	}
	if straight != 1 {
		t.Errorf("%d straight archive wires, want exactly the schedule drop", straight)
	}

	// WAL shipping and base backups are separate flows that happen to
	// share a destination, so the store is reached by one wire of each
	// kind — and they leave different instances, because the operator
	// served the backup from a replica.
	if got := len(kinds["wal"]); got != 1 {
		t.Fatalf("%d WAL wires, want 1", got)
	}
	source := map[string]string{}
	for _, l := range view.Graph.Links {
		if l.Target == "store" {
			source[l.Kind] = l.Source
		}
	}
	if source["wal"] == "" || source["archive"] == "" {
		t.Fatalf("the object store is not reached by both flows: %v", source)
	}
	if source["wal"] == source["archive"] {
		t.Errorf("both object-store flows leave %q; the backup was served by a replica, "+
			"so only WAL should leave the primary", source["wal"])
	}
	labels := map[string]string{}
	for _, n := range view.Graph.Nodes {
		for _, l := range n.Lines {
			if l.C == "label" {
				labels[n.ID] = l.T
				break
			}
		}
	}
	if !strings.HasPrefix(labels[source["archive"]], "orders-2") {
		t.Errorf("the base-backup wire leaves %q, want the attributed instance orders-2",
			labels[source["archive"]])
	}

	// Both object-store wires carry their word, and the base one names
	// the mechanism the Backup reported rather than one it assumed.
	wireLabel := func(kind string) string {
		for _, e := range view.Edges {
			if e.Kind == kind && e.Label != "" {
				return e.Label
			}
		}
		return ""
	}
	if got := wireLabel("wal"); got != "WAL streaming" {
		t.Errorf("WAL wire label = %q, want %q", got, "WAL streaming")
	}
	if got := wireLabel("archive"); got != "base backup · barman-cloud" {
		t.Errorf("base wire label = %q, want the reported plugin named", got)
	}

	// The schedule drops onto the cluster frame's top line, not into the
	// primary's box: a ScheduledBackup names the Cluster, and the
	// operator picks the instance per run.
	var clusterTop, primaryTop int
	for _, f := range view.Frames {
		if f.Label == "Cluster" {
			clusterTop = f.Y
		}
	}
	for _, n := range view.Nodes {
		if n.Kind == "primary" {
			primaryTop = n.Y
		}
	}
	drop := ""
	for _, a := range archives {
		if !strings.Contains(a.Path, "Q") {
			drop = a.Path
		}
	}
	if want := fmt.Sprintf(" %d", clusterTop); !strings.HasSuffix(drop, want) {
		t.Errorf("the schedule drop ends %q, want it to stop on the cluster frame at y=%d", drop, clusterTop)
	}
	if clusterTop >= primaryTop {
		t.Fatalf("cluster frame top %d is not above the primary box %d", clusterTop, primaryTop)
	}
	if strings.HasSuffix(drop, fmt.Sprintf(" %d", primaryTop)) {
		t.Error("the schedule drop still reaches into the primary's box")
	}

	// Tees carry dots in the styles of the flows that split.
	dotKinds := map[string]int{}
	for _, d := range view.Dots {
		dotKinds[d.Kind]++
	}
	if dotKinds["replicate"] == 0 {
		t.Error("the replication trunk splits with no tee dot")
	}
	if dotKinds["read"] == 0 {
		t.Error("the read trunk splits with no tee dot")
	}

	// The legend keys every drawn style, including the volume wire.
	var legend []string
	for _, l := range view.Legend {
		legend = append(legend, l.Kind)
	}
	if got, want := strings.Join(legend, ","), "write,read,replicate,wal,archive,disk"; got != want {
		t.Errorf("legend = %s, want %s", got, want)
	}
}

// The grouped drawing shrinks honestly: no poolers means no Poolers
// frame, no storage evidence means no Storage frame, and the page
// still renders.
func TestGroupedWiringDropsAbsentGroups(t *testing.T) {
	t.Parallel()
	src := wiringSources()
	page := buildPage(context.Background(), "orders", "payments", snapshots{
		window: time.Hour, cluster: src.snap, ok: true,
		pods: src.pods, podsOK: true,
	}, testNow, Links{})
	view := buildGroupedWiring(&page)
	if view == nil {
		t.Fatal("no grouped drawing was built")
	}
	for _, f := range view.Frames {
		if f.Label != "Cluster" {
			t.Errorf("frame %s drawn with nothing observed for it", f.Label)
		}
	}
	if len(view.Frames) != 1 {
		t.Errorf("%d frames, want the cluster frame alone", len(view.Frames))
	}
}

// A Backup the operator never attributed to an instance buys no
// base-backup wire. The console has no word on where that flow began,
// and drawing it from the primary would restate the assumption the
// split exists to retire — CloudNativePG serves base backups from a
// standby by default. WAL shipping is unaffected: that one is the
// primary's duty whatever the backup target says.
func TestGroupedWiringDrawsNoBaseWireWithoutAnAttributedInstance(t *testing.T) {
	t.Parallel()
	src := wiringSources()
	page := buildPage(context.Background(), "orders", "payments", snapshots{
		window: time.Hour, cluster: src.snap, ok: true,
		pods: src.pods, podsOK: true,
		backups: observe.BackupsSnapshot{
			Generation: 6, ObservedAt: testNow.Add(-2 * time.Second),
			Backups: []observe.BackupFacts{
				{Name: "orders-first", UID: "b1", Phase: "completed", Method: "plugin"},
			},
		},
		backupsOK: true,
	}, testNow, Links{})
	view := buildGroupedWiring(&page)
	if view == nil {
		t.Fatal("no grouped drawing was built")
	}
	for _, l := range view.Graph.Links {
		if l.Kind == "archive" && l.Target == "store" {
			t.Errorf("a base-backup wire reaches the store from %q with no instance attributed", l.Source)
		}
	}
	wal := 0
	for _, l := range view.Graph.Links {
		if l.Kind == "wal" && l.Target == "store" {
			wal++
		}
	}
	if wal != 1 {
		t.Errorf("%d WAL wires into the store, want 1 regardless of backup attribution", wal)
	}
}
