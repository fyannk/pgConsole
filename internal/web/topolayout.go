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
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/goccy/go-graphviz"
	"github.com/goccy/go-graphviz/cgraph"

	"github.com/fyannk/pgConsole/internal/redact"
)

// The wiring diagrams' geometry comes from Graphviz, which runs inside
// this process as WebAssembly (no cgo, no subprocess, no network). It
// answers exactly one question — where do the boxes and the wires go —
// and the console keeps everything else: the tiers are pinned to our
// columns, the order within a tier is pinned to ours, and the drawing
// is our own SVG with the design system's classes and tokens.
//
// Hand-rolling this routing is what it replaces. Orthogonal edge
// routing without crossings, with ports allocated per side and lanes
// that do not overlap, is a solved problem with decades of work behind
// it; the console has no business solving it again.
//
// Layout is a pure function of the graph, so it is deterministic and
// safe to screenshot, and it costs a few milliseconds per render.
const (
	// pointsPerInch converts the console's pixel geometry to the inches
	// Graphviz sizes nodes in.
	pointsPerInch = 72.0
	// tierSeparation is the gap between tier columns, in inches.
	tierSeparation = 0.85
	// nodeSeparation is the gap between a tier's boxes, in inches.
	nodeSeparation = 0.30
	// cornerRadius rounds each elbow of a route.
	cornerRadius = 6.0
)

// layoutMu serialises the layout engine. Graphviz keeps process-wide
// state behind its WebAssembly boundary — two layouts running at once
// corrupt it and take the process down — so every call holds this lock.
// One layout costs a few milliseconds, which the console can serialise
// without anyone noticing; a crash it cannot.
var layoutMu sync.Mutex

// topoPoint is one waypoint of a route, in SVG coordinates.
type topoPoint struct{ x, y float64 }

// topoGeometry is what the layout engine settled on.
type topoGeometry struct {
	// Width and Height are the drawing's extent.
	Width, Height int
	// Centres locate each node by ID.
	Centres map[string]topoPoint
	// Edges are the routed flows, in the order the links were given.
	Edges []TopoEdge
}

// layoutDiagram places the nodes and routes the links. Nodes need only
// their ID, Layer and extent; the caller keeps the rest. The returned
// error means the drawing cannot be trusted, and the caller omits the
// diagram rather than showing a wrong one.
func layoutDiagram(ctx context.Context, nodes []TopoNode, links []TopoGraphLink) (topoGeometry, error) {
	if len(nodes) == 0 {
		return topoGeometry{}, fmt.Errorf("no nodes to lay out")
	}
	layoutMu.Lock()
	defer layoutMu.Unlock()

	engine, err := graphviz.New(ctx)
	if err != nil {
		return topoGeometry{}, redact.NewError("diagram layout", redact.CategoryInternal, err)
	}
	defer func() { _ = engine.Close() }()

	graph, err := engine.Graph(graphviz.WithDirectedType(graphviz.Directed), graphviz.WithName("wiring"))
	if err != nil {
		return topoGeometry{}, redact.NewError("diagram layout", redact.CategoryInternal, err)
	}
	defer func() { _ = graph.Close() }()

	graph.SetRankDir(cgraph.LRRank)
	graph.SetSplines("ortho")
	graph.SetNodeSeparator(nodeSeparation)
	graph.SetRankSeparator(tierSeparation)

	// Boxes carry no label: Graphviz sizes them from the extent the
	// console already computed, and the console draws the text itself.
	made := map[string]*cgraph.Node{}
	var tiers []int
	byTier := map[int][]string{}
	for _, n := range nodes {
		node, err := graph.CreateNodeByName(n.ID)
		if err != nil {
			return topoGeometry{}, redact.NewError("diagram layout", redact.CategoryInternal, err)
		}
		node.SetShape(cgraph.BoxShape)
		node.SetWidth(float64(n.W) / pointsPerInch)
		node.SetHeight(float64(n.H) / pointsPerInch)
		node.SetFixedSize(true)
		node.SetLabel("")
		made[n.ID] = node
		if _, seen := byTier[n.Layer]; !seen {
			tiers = append(tiers, n.Layer)
		}
		byTier[n.Layer] = append(byTier[n.Layer], n.ID)
	}

	// One rank per tier pins the columns: the layout decides only how
	// the boxes sit inside them and how the wires get between them.
	for _, tier := range tiers {
		sub, err := graph.CreateSubGraphByName(fmt.Sprintf("rank_%d", tier))
		if err != nil {
			return topoGeometry{}, redact.NewError("diagram layout", redact.CategoryInternal, err)
		}
		if err := sub.SafeSet("rank", "same", ""); err != nil {
			return topoGeometry{}, redact.NewError("diagram layout", redact.CategoryInternal, err)
		}
		for _, id := range byTier[tier] {
			if _, err := sub.CreateNodeByName(id); err != nil {
				return topoGeometry{}, redact.NewError("diagram layout", redact.CategoryInternal, err)
			}
		}
	}

	for i, l := range links {
		if made[l.Source] == nil || made[l.Target] == nil {
			return topoGeometry{}, fmt.Errorf("link %d names a box the diagram does not carry", i)
		}
		if _, err := graph.CreateEdgeByName(fmt.Sprintf("e%d", i), made[l.Source], made[l.Target]); err != nil {
			return topoGeometry{}, redact.NewError("diagram layout", redact.CategoryInternal, err)
		}
	}

	// Graphviz orders a rank by crossing minimisation, which reshuffles
	// the instances into an order the reader did not ask for. An
	// invisible flat edge between consecutive members holds the order
	// the console chose — the primary first, then the replicas.
	for _, tier := range byTier {
		for i := 1; i < len(tier); i++ {
			edge, err := graph.CreateEdgeByName(fmt.Sprintf("order_%s_%s", tier[i-1], tier[i]), made[tier[i-1]], made[tier[i]])
			if err != nil {
				return topoGeometry{}, redact.NewError("diagram layout", redact.CategoryInternal, err)
			}
			edge.SetStyle(cgraph.EdgeStyle("invis"))
			edge.SetWeight(10)
		}
	}

	var out strings.Builder
	if err := engine.Render(ctx, graph, "json", &out); err != nil {
		return topoGeometry{}, redact.NewError("diagram layout", redact.CategoryInternal, err)
	}
	return parseLayout(out.String(), links)
}

// graphvizLayout is the subset of Graphviz's JSON output the console
// reads: the canvas, every object's placement, and every edge's route.
type graphvizLayout struct {
	BB      string `json:"bb"`
	Objects []struct {
		GVID int    `json:"_gvid"`
		Name string `json:"name"`
		Pos  string `json:"pos"`
	} `json:"objects"`
	Edges []struct {
		Tail int    `json:"tail"`
		Head int    `json:"head"`
		Pos  string `json:"pos"`
	} `json:"edges"`
}

// parseLayout converts Graphviz's output into the console's geometry.
// Graphviz measures from the bottom left and SVG from the top left, so
// every y is flipped once, here.
func parseLayout(raw string, links []TopoGraphLink) (topoGeometry, error) {
	var doc graphvizLayout
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return topoGeometry{}, redact.NewError("diagram layout", redact.CategoryInternal, err)
	}
	box := strings.Split(doc.BB, ",")
	if len(box) != 4 {
		return topoGeometry{}, fmt.Errorf("layout carries no bounding box")
	}
	width, err1 := strconv.ParseFloat(box[2], 64)
	height, err2 := strconv.ParseFloat(box[3], 64)
	if err1 != nil || err2 != nil {
		return topoGeometry{}, fmt.Errorf("layout bounding box is not numeric")
	}

	geo := topoGeometry{
		Width:   int(width),
		Height:  int(height),
		Centres: make(map[string]topoPoint, len(doc.Objects)),
		Edges:   make([]TopoEdge, len(links)),
	}
	nameOf := make(map[int]string, len(doc.Objects))
	for _, o := range doc.Objects {
		nameOf[o.GVID] = o.Name
		if o.Pos == "" {
			continue // a rank subgraph carries no placement of its own
		}
		xy := strings.Split(o.Pos, ",")
		if len(xy) != 2 {
			continue
		}
		x, errX := strconv.ParseFloat(xy[0], 64)
		y, errY := strconv.ParseFloat(xy[1], 64)
		if errX != nil || errY != nil {
			continue
		}
		geo.Centres[o.Name] = topoPoint{x, height - y}
	}

	// The routes come back in the order the edges were created, and the
	// invisible ordering edges follow them. Each route is checked
	// against the link it claims to be: a silent mismatch would draw a
	// flow between the wrong boxes, which is worse than no diagram.
	labeled := false
	for i, e := range doc.Edges {
		if i >= len(links) {
			break
		}
		if nameOf[e.Tail] != links[i].Source || nameOf[e.Head] != links[i].Target {
			return topoGeometry{}, fmt.Errorf("route %d runs %s to %s, but the link is %s to %s",
				i, nameOf[e.Tail], nameOf[e.Head], links[i].Source, links[i].Target)
		}
		points := corners(routePoints(e.Pos, height))
		if len(points) < 2 {
			return topoGeometry{}, fmt.Errorf("route %d has no path", i)
		}
		edge := TopoEdge{Kind: links[i].Kind, Path: roundedRoute(points)}
		// One label stays on the wire: the boxes name themselves and the
		// legend keys the styles, but nothing else says what a dashed
		// line between two servers means.
		if links[i].Kind == "replicate" && !labeled {
			labeled = true
			edge.Label = "replication"
			edge.LabelX = int(points[0].x) + 8
			edge.LabelY = int((points[0].y+points[len(points)-1].y)/2) + 4
		}
		geo.Edges[i] = edge
	}
	for i := range geo.Edges {
		if geo.Edges[i].Path == "" {
			return topoGeometry{}, fmt.Errorf("link %d was not routed", i)
		}
	}
	return geo, nil
}

// routePoints reads one Graphviz position list. The "e,x,y" entry is
// the arrowhead, which belongs at the end of the route rather than the
// start where it is written.
func routePoints(pos string, height float64) []topoPoint {
	var head *topoPoint
	var points []topoPoint
	for _, token := range strings.Fields(pos) {
		isHead := strings.HasPrefix(token, "e,")
		xy := strings.Split(strings.TrimPrefix(token, "e,"), ",")
		if len(xy) != 2 {
			continue
		}
		x, errX := strconv.ParseFloat(xy[0], 64)
		y, errY := strconv.ParseFloat(xy[1], 64)
		if errX != nil || errY != nil {
			continue
		}
		point := topoPoint{x, height - y}
		if isHead {
			head = &point
			continue
		}
		points = append(points, point)
	}
	if head != nil {
		points = append(points, *head)
	}
	return points
}

// corners reduces a route to its turns. Graphviz writes an orthogonal
// route as a bezier control list, so every vertex repeats and every
// straight run carries collinear midpoints; neither survives here.
func corners(in []topoPoint) []topoPoint {
	var distinct []topoPoint
	for _, p := range in {
		if len(distinct) > 0 && sameSpot(distinct[len(distinct)-1], p) {
			continue
		}
		distinct = append(distinct, p)
	}
	var out []topoPoint
	for i, p := range distinct {
		if i == 0 || i == len(distinct)-1 {
			out = append(out, p)
			continue
		}
		before, after := distinct[i-1], distinct[i+1]
		straight := (closeTo(before.x, p.x) && closeTo(p.x, after.x)) ||
			(closeTo(before.y, p.y) && closeTo(p.y, after.y))
		if !straight {
			out = append(out, p)
		}
	}
	return out
}

func sameSpot(a, b topoPoint) bool { return closeTo(a.x, b.x) && closeTo(a.y, b.y) }

func closeTo(a, b float64) bool {
	d := a - b
	return d < 0.6 && d > -0.6
}

// roundedRoute renders a polyline with the design system's rounded
// elbows, shrinking a corner that would not fit its segment.
func roundedRoute(points []topoPoint) string {
	var b strings.Builder
	fmt.Fprintf(&b, "M%s %s", coord(points[0].x), coord(points[0].y))
	for i := 1; i < len(points); i++ {
		p := points[i]
		if i == len(points)-1 {
			fmt.Fprintf(&b, " L%s %s", coord(p.x), coord(p.y))
			continue
		}
		before, after := points[i-1], points[i+1]
		radius := cornerRadius
		if half := span(before, p) / 2; half < radius {
			radius = half
		}
		if half := span(p, after) / 2; half < radius {
			radius = half
		}
		inX, inY := direction(p.x-before.x), direction(p.y-before.y)
		outX, outY := direction(after.x-p.x), direction(after.y-p.y)
		fmt.Fprintf(&b, " L%s %s", coord(p.x-inX*radius), coord(p.y-inY*radius))
		fmt.Fprintf(&b, " Q%s %s %s %s", coord(p.x), coord(p.y),
			coord(p.x+outX*radius), coord(p.y+outY*radius))
	}
	return b.String()
}

// span is the axis-aligned distance between two waypoints.
func span(a, b topoPoint) float64 {
	dx, dy := a.x-b.x, a.y-b.y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	return dx + dy
}

func direction(v float64) float64 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}

// coord renders a coordinate without a pointless trailing zero.
func coord(v float64) string {
	return strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
}
