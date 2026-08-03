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
	"fmt"
	"sort"
	"strings"
)

// The wiring diagrams route their edges orthogonally: fixed tiers, one
// port per arrow, rounded corners, and vertical runs staggered through
// the gap between tiers so parallel flows never overlap. Labels come
// off the wires — the boxes name themselves and a legend keys the line
// styles — with one exception: "replication" stays beside its arrow,
// because nothing else on the drawing says what a dashed green line
// between two servers is.
//
// The enhancement layer (topology-force.js) implements this same
// router for the re-layout it draws from the data-topo graph; the two
// must stay in step or the enhanced drawing would not match the served
// one.
const (
	// topoCornerRadius rounds each elbow.
	topoCornerRadius = 6
	// topoLaneOffset is where the bypass lane for far same-tier links
	// starts, right of the column.
	topoLaneOffset = 20
	// topoLaneGap separates parallel bypass lanes.
	topoLaneGap = 16
	// topoDrop is the short fall below a box before a bypass lane turns.
	topoDrop = 14
)

// topoPoint is one waypoint.
type topoPoint struct{ x, y float64 }

// legendNames maps edge kinds to the legend's plain words, in display
// order.
var legendOrder = []struct{ Kind, Label string }{
	{"write", "writes"},
	{"read", "reads"},
	{"replicate", "replication"},
	{"archive", "backup"},
}

// LegendItem keys one line style below the diagram.
type LegendItem struct {
	Kind  string
	Label string
}

// topoLegend lists the legend entries for the kinds actually drawn.
func topoLegend(links []TopoGraphLink) []LegendItem {
	present := map[string]bool{}
	for _, l := range links {
		present[l.Kind] = true
	}
	var items []LegendItem
	for _, entry := range legendOrder {
		if present[entry.Kind] {
			items = append(items, LegendItem{Kind: entry.Kind, Label: entry.Label})
		}
	}
	return items
}

// routeEdges resolves every link to an orthogonal path. Nodes must
// carry their final geometry; links resolve by node ID.
func routeEdges(nodes []TopoNode, links []TopoGraphLink) []TopoEdge {
	byID := map[string]*TopoNode{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}

	type flow struct {
		link     TopoGraphLink
		src, dst *TopoNode
		sameTier bool
		// direct marks the same-tier flow that falls straight onto the
		// box below; the rest take the bypass lane.
		direct bool
		lane   int
		// port slots, filled by the allocation passes.
		sx, sy, tx, ty float64
		gapX           float64
	}

	flows := make([]*flow, 0, len(links))
	for _, l := range links {
		src, dst := byID[l.Source], byID[l.Target]
		if src == nil || dst == nil {
			continue
		}
		flows = append(flows, &flow{link: l, src: src, dst: dst, sameTier: src.Layer == dst.Layer})
	}

	// Same-tier flows: per source, the nearest target below gets the
	// straight drop, the rest take bypass lanes ordered by distance.
	bySource := map[string][]*flow{}
	for _, f := range flows {
		if f.sameTier {
			bySource[f.src.ID] = append(bySource[f.src.ID], f)
		}
	}
	for _, group := range bySource {
		sort.Slice(group, func(a, b int) bool { return group[a].dst.Y < group[b].dst.Y })
		lane := 0
		for i, f := range group {
			if i == 0 && f.dst.Y > f.src.Y {
				f.direct = true
				continue
			}
			f.lane = lane
			lane++
		}
	}

	// Port allocation: every (node, side) distributes its flows evenly
	// along the side, ordered by the far end so lines do not cross.
	type sideKey struct {
		id   string
		side string
	}
	sides := map[sideKey][]*flow{}
	add := func(id, side string, f *flow) { sides[sideKey{id, side}] = append(sides[sideKey{id, side}], f) }
	for _, f := range flows {
		switch {
		case f.sameTier && f.direct:
			add(f.src.ID, "bottom", f)
			add(f.dst.ID, "top", f)
		case f.sameTier:
			add(f.src.ID, "bottom", f)
			add(f.dst.ID, "rightin", f)
		default:
			add(f.src.ID, "right", f)
			add(f.dst.ID, "left", f)
		}
	}
	for key, group := range sides {
		node := byID[key.id]
		out := key.side == "right" || key.side == "bottom"
		sort.SliceStable(group, func(a, b int) bool {
			fa, fb := group[a], group[b]
			ca, cb := fa.dst, fb.dst
			if !out {
				ca, cb = fa.src, fb.src
			}
			if ca.CenterYF() != cb.CenterYF() {
				return ca.CenterYF() < cb.CenterYF()
			}
			return ca.X < cb.X
		})
		for i, f := range group {
			slot := float64(i+1) / float64(len(group)+1)
			switch key.side {
			case "right":
				f.sx, f.sy = float64(node.X+node.W), float64(node.Y)+float64(node.H)*slot
			case "left":
				f.tx, f.ty = float64(node.X), float64(node.Y)+float64(node.H)*slot
			case "bottom":
				f.sx, f.sy = float64(node.X)+float64(node.W)*slot, float64(node.Y+node.H)
			case "top":
				f.tx, f.ty = float64(node.X)+float64(node.W)*slot, float64(node.Y)
			case "rightin":
				f.tx, f.ty = float64(node.X+node.W), float64(node.Y)+float64(node.H)*slot
			}
		}
	}

	// Vertical runs stagger through each tier gap, ordered by entry
	// height so the runs mirror the flows they carry.
	byGap := map[int][]*flow{}
	for _, f := range flows {
		if !f.sameTier {
			byGap[f.src.Layer] = append(byGap[f.src.Layer], f)
		}
	}
	for _, group := range byGap {
		sort.SliceStable(group, func(a, b int) bool { return group[a].ty < group[b].ty })
		var left, right float64
		for i, f := range group {
			if i == 0 || f.sx > left {
				left = f.sx
			}
			if i == 0 || f.tx < right {
				right = f.tx
			}
		}
		for i, f := range group {
			f.gapX = left + (right-left)*float64(i+1)/float64(len(group)+1)
		}
	}

	// Bypass lanes hang right of the source column.
	columnRight := map[int]float64{}
	for i := range nodes {
		if edge := float64(nodes[i].X + nodes[i].W); edge > columnRight[nodes[i].Layer] {
			columnRight[nodes[i].Layer] = edge
		}
	}

	edges := make([]TopoEdge, 0, len(flows))
	labeled := false
	for _, f := range flows {
		var pts []topoPoint
		switch {
		case f.sameTier && f.direct:
			pts = []topoPoint{{f.sx, f.sy}, {f.sx, f.ty}}
			// The drop is drawn at the source's bottom port; the target
			// entry inherits that x so the line stays vertical.
			pts[1].x = f.sx
		case f.sameTier:
			lane := columnRight[f.src.Layer] + topoLaneOffset + float64(f.lane)*topoLaneGap
			turn := f.sy + topoDrop
			pts = []topoPoint{{f.sx, f.sy}, {f.sx, turn}, {lane, turn}, {lane, f.ty}, {f.tx, f.ty}}
		default:
			if f.sy == f.ty {
				pts = []topoPoint{{f.sx, f.sy}, {f.tx, f.ty}}
			} else {
				pts = []topoPoint{{f.sx, f.sy}, {f.gapX, f.sy}, {f.gapX, f.ty}, {f.tx, f.ty}}
			}
		}
		edge := TopoEdge{Kind: f.link.Kind, Path: orthogonalPath(pts)}
		// The one label kept on the wire: nothing else says what a
		// dashed line between two servers is.
		if f.link.Kind == "replicate" && !labeled {
			labeled = true
			edge.Label = "replication"
			edge.LabelX = int(pts[0].x) + 8
			edge.LabelY = int((pts[0].y+pts[len(pts)-1].y)/2) + 4
		}
		edges = append(edges, edge)
	}
	return edges
}

// CenterYF is the vertical middle as a float, for port ordering.
func (n TopoNode) CenterYF() float64 { return float64(n.Y) + float64(n.H)/2 }

// orthogonalPath renders axis-aligned waypoints with rounded corners.
func orthogonalPath(pts []topoPoint) string {
	var b strings.Builder
	fmt.Fprintf(&b, "M%s %s", trimFloat(pts[0].x), trimFloat(pts[0].y))
	for i := 1; i < len(pts); i++ {
		p, prev := pts[i], pts[i-1]
		if i < len(pts)-1 {
			next := pts[i+1]
			dx1, dy1 := sign(p.x-prev.x), sign(p.y-prev.y)
			dx2, dy2 := sign(next.x-p.x), sign(next.y-p.y)
			fmt.Fprintf(&b, " L%s %s", trimFloat(p.x-dx1*topoCornerRadius), trimFloat(p.y-dy1*topoCornerRadius))
			fmt.Fprintf(&b, " Q%s %s %s %s", trimFloat(p.x), trimFloat(p.y),
				trimFloat(p.x+dx2*topoCornerRadius), trimFloat(p.y+dy2*topoCornerRadius))
		} else {
			fmt.Fprintf(&b, " L%s %s", trimFloat(p.x), trimFloat(p.y))
		}
	}
	return b.String()
}

func sign(v float64) float64 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}

// trimFloat renders a coordinate without a trailing ".0".
func trimFloat(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	return strings.TrimSuffix(s, ".0")
}
