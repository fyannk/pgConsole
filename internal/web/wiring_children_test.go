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
	"strings"
	"testing"
)

// The children drawing reads in the direction its two relations point:
// the objects that name the cluster, the Cluster, then what it owns.
// Both wires then run the same way and neither climbs over the drawing
// to reach its end.
func TestChildrenDrawingReadsReferencesThenClusterThenOwned(t *testing.T) {
	t.Parallel()
	page := objectsPage(t)
	view := buildClusterChildren(&page)
	if view == nil {
		t.Fatal("no children drawing was built")
	}

	var refs, owned *TopoFrame
	for i := range view.Frames {
		switch view.Frames[i].Label {
		case "References the cluster":
			refs = &view.Frames[i]
		case "Owned by the cluster":
			owned = &view.Frames[i]
		}
	}
	if refs == nil || owned == nil {
		t.Fatal("the fixture did not produce both regions")
	}
	var cluster *TopoNode
	for i := range view.Nodes {
		if view.Nodes[i].ID == "cluster" {
			cluster = &view.Nodes[i]
		}
	}
	if cluster == nil {
		t.Fatal("no cluster box was drawn")
	}

	if !(refs.X+refs.W <= cluster.X) {
		t.Errorf("the referencing region ends at %d, not left of the cluster at %d",
			refs.X+refs.W, cluster.X)
	}
	if !(cluster.X+cluster.W <= owned.X) {
		t.Errorf("the cluster ends at %d, not left of the owned region at %d",
			cluster.X+cluster.W, owned.X)
	}

	// Both relations are straight horizontal runs now, so neither wire
	// turns a corner to get home.
	for _, e := range view.Edges {
		if e.Kind != "refs" && e.Kind != "owns" {
			continue
		}
		if strings.Contains(e.Path, "Q") {
			t.Errorf("the %s wire turns a corner: %q", e.Kind, e.Path)
		}
	}
}
