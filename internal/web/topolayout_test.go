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
	"sync"
	"testing"
)

// diagramFixture is a four-tier graph shaped like the Overview's.
func diagramFixture() ([]TopoNode, []TopoGraphLink) {
	nodes := []TopoNode{
		{ID: "apps", Layer: 0, Kind: "apps", W: 150, H: 60},
		{ID: "rw", Layer: 1, Kind: "endpoint", W: 158, H: 48},
		{ID: "ro", Layer: 1, Kind: "endpoint", W: 158, H: 48},
		{ID: "srv-0", Layer: 2, Kind: "primary", W: 176, H: 60},
		{ID: "srv-1", Layer: 2, Kind: "replica", W: 176, H: 60},
		{ID: "srv-2", Layer: 2, Kind: "replica", W: 176, H: 60},
		{ID: "store", Layer: 3, Kind: "storage", W: 184, H: 48},
	}
	links := []TopoGraphLink{
		{Source: "apps", Target: "rw", Kind: "write"},
		{Source: "apps", Target: "ro", Kind: "read"},
		{Source: "rw", Target: "srv-0", Kind: "write"},
		{Source: "ro", Target: "srv-1", Kind: "read"},
		{Source: "ro", Target: "srv-2", Kind: "read"},
		{Source: "srv-0", Target: "srv-1", Kind: "replicate"},
		{Source: "srv-0", Target: "srv-2", Kind: "replicate"},
		{Source: "srv-0", Target: "store", Kind: "archive"},
	}
	return nodes, links
}

func TestLayoutPinsTiersAndOrder(t *testing.T) {
	t.Parallel()
	nodes, links := diagramFixture()
	geo, err := layoutDiagram(context.Background(), nodes, links)
	if err != nil {
		t.Fatalf("layoutDiagram: %v", err)
	}
	if len(geo.Edges) != len(links) {
		t.Fatalf("routed %d of %d links", len(geo.Edges), len(links))
	}

	// Tiers are the console's, so every member of one shares a column
	// and the columns advance left to right.
	column := map[int]float64{}
	for _, n := range nodes {
		centre, ok := geo.Centres[n.ID]
		if !ok {
			t.Fatalf("%s was not placed", n.ID)
		}
		if x, seen := column[n.Layer]; seen {
			if x != centre.x {
				t.Errorf("tier %d is not one column: %s sits at %v, not %v", n.Layer, n.ID, centre.x, x)
			}
			continue
		}
		column[n.Layer] = centre.x
	}
	for tier := 1; tier < len(column); tier++ {
		if column[tier] <= column[tier-1] {
			t.Errorf("tier %d is not right of tier %d", tier, tier-1)
		}
	}

	// Order within a tier is the console's too: the primary leads its
	// replicas, which is what the invisible ordering edges hold.
	if geo.Centres["srv-0"].y >= geo.Centres["srv-1"].y ||
		geo.Centres["srv-1"].y >= geo.Centres["srv-2"].y {
		t.Errorf("instances are out of order: %v, %v, %v",
			geo.Centres["srv-0"].y, geo.Centres["srv-1"].y, geo.Centres["srv-2"].y)
	}
}

func TestLayoutRoutesOrthogonally(t *testing.T) {
	t.Parallel()
	nodes, links := diagramFixture()
	geo, err := layoutDiagram(context.Background(), nodes, links)
	if err != nil {
		t.Fatalf("layoutDiagram: %v", err)
	}
	for i, edge := range geo.Edges {
		if edge.Kind != links[i].Kind {
			t.Errorf("route %d is a %s flow, the link is %s", i, edge.Kind, links[i].Kind)
		}
		if edge.Path == "" {
			t.Fatalf("route %d has no path", i)
		}
		// Orthogonal runs and quadratic corners only: a cubic segment
		// would mean the route came from somewhere else.
		if strings.Contains(edge.Path, "C") {
			t.Errorf("route %d is a cubic curve: %q", i, edge.Path)
		}
	}
	// One label stays on the wire, and only one.
	labels := 0
	for _, edge := range geo.Edges {
		if edge.Label != "" {
			labels++
			if edge.Label != "replication" || edge.Kind != "replicate" {
				t.Errorf("unexpected wire label %q on a %s flow", edge.Label, edge.Kind)
			}
		}
	}
	if labels != 1 {
		t.Errorf("wire labels = %d, want exactly the replication one", labels)
	}
}

// TestLayoutIsDeterministic proves a screenshot of one page is a
// screenshot of every page: the same graph must settle the same way, or
// the visual checks would flap.
func TestLayoutIsDeterministic(t *testing.T) {
	t.Parallel()
	nodes, links := diagramFixture()
	first, err := layoutDiagram(context.Background(), nodes, links)
	if err != nil {
		t.Fatalf("layoutDiagram: %v", err)
	}
	for i := 0; i < 3; i++ {
		again, err := layoutDiagram(context.Background(), nodes, links)
		if err != nil {
			t.Fatalf("layoutDiagram: %v", err)
		}
		if again.Width != first.Width || again.Height != first.Height {
			t.Fatalf("canvas moved between runs: %dx%d then %dx%d",
				first.Width, first.Height, again.Width, again.Height)
		}
		for id, centre := range first.Centres {
			if again.Centres[id] != centre {
				t.Fatalf("%s moved between runs: %v then %v", id, centre, again.Centres[id])
			}
		}
		for j, edge := range first.Edges {
			if again.Edges[j].Path != edge.Path {
				t.Fatalf("route %d changed between runs", j)
			}
		}
	}
}

// TestLayoutSurvivesConcurrentCallers proves the engine is serialised.
// Graphviz keeps process-wide state behind its WebAssembly boundary, so
// two layouts at once corrupted it and killed the process — which in
// production is two operators loading the Overview at the same time.
func TestLayoutSurvivesConcurrentCallers(t *testing.T) {
	t.Parallel()
	nodes, links := diagramFixture()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := layoutDiagram(context.Background(), nodes, links); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent layout: %v", err)
	}
}

// TestLayoutRefusesADanglingLink proves a link naming a box the diagram
// does not carry is refused rather than silently dropped: a missing flow
// in a wiring diagram is a lie about the cluster.
func TestLayoutRefusesADanglingLink(t *testing.T) {
	t.Parallel()
	nodes, _ := diagramFixture()
	_, err := layoutDiagram(context.Background(), nodes, []TopoGraphLink{{Source: "apps", Target: "ghost", Kind: "write"}})
	if err == nil {
		t.Fatal("a dangling link was accepted")
	}
}
