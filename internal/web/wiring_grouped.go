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

import "fmt"

// The grouped wiring drawing. A derivation of the design proposal's
// diagram, and laid out the same way that proposal was: no layout
// engine, every placement a stated rule.
//
//   - Columns, left to right: poolers, services, the primary, the
//     replicas, storage. The primary stands left of its replicas; the
//     claims stand right of the instance that holds them.
//   - Rows: rw above ro, for the poolers and the services alike. In the
//     storage column the object store and the snapshots sit on top; each
//     claim is centred on its own instance, so the volume wire between
//     them is a straight line.
//   - Three dotted frames name the categories: Poolers, Cluster,
//     Storage. The trunk buses run in the alleys between frames.
//   - A flow that fans out leaves its box once: one trunk to a bus, one
//     branch per destination, a dot on each tee. Replication leaves the
//     primary at one port and splits; the read service reaches its
//     replicas the same way.
//
// Deterministic by construction, so it is safe to screenshot and cheap
// to test: the same page renders the same drawing, always.
const (
	// grpColGap separates columns inside the cluster frame; the read
	// bus runs down the middle of the services-to-primary gap.
	grpColGap = 48
	// grpAlley separates two frames; the replication and archive buses
	// run in the cluster-to-storage alley.
	grpAlley = 60
	// grpRowGap separates stacked boxes in a column.
	grpRowGap = 28
	// grpPad is a frame's padding around its boxes.
	grpPad = 16
	// grpLabelBand is the frame headroom holding the category label.
	grpLabelBand = 30
	// grpPortOff separates the ports sharing a box edge, so the archive,
	// volume and replication wires leave the primary as three lines
	// rather than one smear.
	grpPortOff = 15
	// grpMargin is the viewBox margin around everything.
	grpMargin = 8
	// grpWPvc is the claim boxes' width.
	grpWPvc = 200
)

// buildGroupedWiring derives the grouped drawing from the assembled
// page. Like the other builders it reads only p and invents nothing;
// with no observed servers there is no drawing.
func buildGroupedWiring(p *Page) *TopologyView {
	if p.Cluster == nil || p.Cluster.Absent {
		return nil
	}
	servers := wireServers(p, false)
	if len(servers) == 0 {
		return nil
	}

	view := &TopologyView{
		Title: "Physical wiring — grouped",
		Aria:  "The same wiring grouped into poolers, cluster and storage, with one trunk per fanned flow",
	}

	// The capacity is a provable upper bound on every add below, so the
	// slice never reallocates and the pointers add hands out — which the
	// whole placement phase writes through — stay pointed at the slice
	// the view will render.
	capacity := 4 + len(servers)
	if p.Poolers != nil {
		capacity += len(p.Poolers.Poolers)
	}
	if p.Infrastructure != nil {
		capacity += len(p.Infrastructure.Volumes)
	}
	nodes := make([]TopoNode, 0, capacity)
	rowsByID := map[string][]TopoGraphText{}
	add := func(id, kind, state string, w int, rows []TopoGraphText) *TopoNode {
		nodes = append(nodes, TopoNode{
			ID: id, Kind: kind, State: state,
			W: w, H: wireNodeHeight(len(rows)),
		})
		rowsByID[id] = rows
		return &nodes[len(nodes)-1]
	}
	centre := func(n *TopoNode, cx, cy float64) {
		n.X = int(cx) - n.W/2
		n.Y = int(cy) - n.H/2
	}
	cy := func(n *TopoNode) float64 { return float64(n.Y) + float64(n.H)/2 }
	right := func(n *TopoNode) float64 { return float64(n.X + n.W) }
	left := func(n *TopoNode) float64 { return float64(n.X) }

	// --- Content, gathered before any placement. ---

	// Poolers, rw before ro so the write path reads on top.
	type grpPooler struct {
		node *TopoNode
		ro   bool
	}
	var poolers []grpPooler
	if p.Poolers != nil {
		ordered := make([]PoolerRowView, 0, len(p.Poolers.Poolers))
		for _, pooler := range p.Poolers.Poolers {
			if pooler.TypeToken != "ro" {
				ordered = append(ordered, pooler)
			}
		}
		for _, pooler := range p.Poolers.Poolers {
			if pooler.TypeToken == "ro" {
				ordered = append(ordered, pooler)
			}
		}
		for i, pooler := range ordered {
			rows := poolerRows(pooler)
			poolers = append(poolers, grpPooler{
				node: add(fmt.Sprintf("pool-%d", i), "pooler", poolerState(pooler), wireWPool, rows),
				ro:   pooler.TypeToken == "ro",
			})
		}
	}

	// Services: rw above ro, always both.
	rwRows := endpointRows(p, "read-write", p.ClusterName+"-rw", "routes to the current primary")
	roRows := endpointRows(p, "read-only", p.ClusterName+"-ro", "routes to the read-only copies")
	rw := add("rw", "endpoint", "", wireWSvc, rwRows)
	ro := add("ro", "endpoint", "", wireWSvc, roRows)

	// Servers: the primary apart, the replicas as a stack.
	var primary *TopoNode
	var replicas []*TopoNode
	names := map[string]string{}
	for i, s := range servers {
		id := fmt.Sprintf("srv-%d", i)
		n := add(id, s.kind, s.state, wireWSrv, s.rows)
		if s.name != "" {
			names[id] = s.name
		}
		if s.kind == "primary" && primary == nil {
			primary = n
		} else {
			replicas = append(replicas, n)
		}
	}

	// Storage: the object store and snapshots on top, then one claim
	// box per instance claim, in instance order.
	cloud, snapCfg := topoStorageConfigured(p)
	var store, snapshot *TopoNode
	if cloud {
		store = add("store", "storage", "", wireWStore, objectStoreRows(p))
	}
	if snapCfg {
		snapshot = add("snapshot", "snapshot", "", wireWStore, snapshotRows(p))
	}
	type grpClaim struct {
		node *TopoNode
		of   *TopoNode
	}
	var claims []grpClaim
	if p.Infrastructure != nil {
		instances := append([]*TopoNode{}, replicas...)
		if primary != nil {
			instances = append([]*TopoNode{primary}, instances...)
		}
		for _, inst := range instances {
			name := names[inst.ID]
			if name == "" {
				continue
			}
			for _, v := range p.Infrastructure.Volumes {
				if v.Instance != name {
					continue
				}
				claims = append(claims, grpClaim{
					node: add("pvc-"+v.Name, "pvc", "", grpWPvc, claimRows(v)),
					of:   inst,
				})
			}
		}
	}

	// --- Placement: columns first, then rows. ---

	hasPool := len(poolers) > 0
	hasStore := store != nil || snapshot != nil || len(claims) > 0

	x := float64(grpMargin)
	var poolX float64
	if hasPool {
		x += grpPad
		poolX = x
		x += wireWPool + grpPad + grpAlley
	}
	x += grpPad
	svcX := x
	x += wireWSvc + grpColGap
	primX := x
	x += wireWSrv + grpColGap
	replX := x
	x += wireWSrv + grpPad
	clusterRight := x
	var storeX float64
	if hasStore {
		x += grpAlley + grpPad
		storeX = x
		x += float64(maxW(wireWStore, grpWPvc)) + grpPad
	}
	width := int(x) + grpMargin

	// The storage stack's top half: object store, then snapshots.
	yTop := float64(grpMargin + grpLabelBand + grpPad)
	storageY := yTop
	if store != nil {
		centre(store, storeX+float64(store.W)/2, storageY+float64(store.H)/2)
		storageY += float64(store.H) + grpRowGap
	}
	if snapshot != nil {
		centre(snapshot, storeX+float64(snapshot.W)/2, storageY+float64(snapshot.H)/2)
		storageY += float64(snapshot.H) + grpRowGap
	}

	// Instance rows start below that stack, so the claims — each
	// centred on its instance — never collide with it.
	instTop := yTop
	if len(claims) > 0 && storageY > instTop {
		instTop = storageY
	}
	rowY := instTop
	if primary != nil {
		centre(primary, primX+float64(primary.W)/2, rowY+float64(primary.H)/2)
		rowY += float64(primary.H) + grpRowGap
	}
	for _, r := range replicas {
		centre(r, replX+float64(r.W)/2, rowY+float64(r.H)/2)
		rowY += float64(r.H) + grpRowGap
	}

	// Claims, each centred on its instance; a second claim of the same
	// instance stacks below the first.
	usedRows := map[string]float64{}
	for _, c := range claims {
		base := cy(c.of) + usedRows[c.of.ID]
		usedRows[c.of.ID] += float64(c.node.H) + 10
		centre(c.node, storeX+float64(c.node.W)/2, base)
	}

	// Services follow the servers: the write service level with the
	// primary, the read service level with the first replica.
	rwCy := yTop + float64(rw.H)/2
	if primary != nil {
		rwCy = cy(primary)
	}
	centre(rw, svcX+float64(rw.W)/2, rwCy)
	roCy := float64(rw.Y+rw.H) + grpRowGap + float64(ro.H)/2
	if len(replicas) > 0 {
		roCy = cy(replicas[0])
	}
	centre(ro, svcX+float64(ro.W)/2, roCy)

	// Poolers follow their service, singles exactly level with it,
	// groups stacked around it; the ro group never rides over the rw.
	var rwPool, roPool []*TopoNode
	for _, pl := range poolers {
		if pl.ro {
			roPool = append(roPool, pl.node)
		} else {
			rwPool = append(rwPool, pl.node)
		}
	}
	poolBottom := stackAround(rwPool, poolX, cy(rw), yTop)
	stackAround(roPool, poolX, cy(ro), poolBottom+grpRowGap)

	view.Nodes = nodes

	// --- Wires: every fan is one trunk, tee dots where it splits. ---

	var edges []TopoEdge
	var links []TopoGraphLink
	wire := func(kind string, from, to *TopoNode, points []topoPoint) {
		edges = append(edges, TopoEdge{Kind: kind, Path: roundedRoute(corners(points))})
		links = append(links, TopoGraphLink{Source: from.ID, Target: to.ID, Kind: kind})
	}
	dot := func(kind string, x, y float64) {
		view.Dots = append(view.Dots, TopoDot{Kind: kind, X: int(x), Y: int(y)})
	}

	// Poolers into their service, fanning in over the pool alley.
	if hasPool {
		bus := poolX + wireWPool + grpPad + grpAlley/2
		fanIn := func(kind string, from []*TopoNode, to *TopoNode) {
			for _, f := range from {
				wire(kind, f, to, []topoPoint{
					{right(f), cy(f)}, {bus, cy(f)}, {bus, cy(to)}, {left(to), cy(to)},
				})
			}
			if len(from) > 1 {
				for _, f := range from {
					if cy(f) != cy(to) {
						dot(kind, bus, cy(f))
					}
				}
			}
		}
		fanIn("write", rwPool, rw)
		fanIn("read", roPool, ro)
	}

	// The write service into the primary: one straight line.
	if primary != nil {
		wire("write", rw, primary, []topoPoint{
			{right(rw), cy(rw)}, {left(primary), cy(primary)},
		})
	}

	// The read service into every replica: straight into the first,
	// one bus down to the rest.
	readBus := svcX + wireWSvc + grpColGap/2
	for i, r := range replicas {
		wire("read", ro, r, []topoPoint{
			{right(ro), cy(ro)}, {readBus, cy(ro)}, {readBus, cy(r)}, {left(r), cy(r)},
		})
		if len(replicas) > 1 && i < len(replicas)-1 {
			dot("read", readBus, cy(r))
		}
	}

	// Replication leaves the primary once: one trunk to the bus in the
	// storage alley, then a branch left into each replica's right edge.
	archBus := clusterRight + grpAlley*0.32
	replBus := clusterRight + grpAlley*0.68
	if primary != nil && len(replicas) > 0 {
		out := cy(primary) + grpPortOff
		for i, r := range replicas {
			in := cy(r) + grpPortOff
			wire("replicate", primary, r, []topoPoint{
				{right(primary), out}, {replBus, out}, {replBus, in}, {right(r), in},
			})
			if i < len(replicas)-1 {
				dot("replicate", replBus, in)
			}
		}
		// The label rides the trunk itself, in the clear corridor right
		// of the primary, not down the bus where the branches and the
		// volume wires cross.
		edges[len(edges)-1].Label = "replication"
		edges[len(edges)-1].LabelX = int((right(primary) + replBus) / 2)
		edges[len(edges)-1].LabelY = int(out) - 7
	}

	// Each instance onto its claim: level, so the wire is straight.
	for _, c := range claims {
		wire("disk", c.of, c.node, []topoPoint{
			{right(c.of), cy(c.of)}, {left(c.node), cy(c.node)},
		})
	}

	// The archive trunk climbs the alley from the primary to the object
	// store and the snapshots.
	if primary != nil && (store != nil || snapshot != nil) {
		out := cy(primary) - grpPortOff
		targets := []*TopoNode{}
		if snapshot != nil {
			targets = append(targets, snapshot)
		}
		if store != nil {
			targets = append(targets, store)
		}
		for i, tgt := range targets {
			wire("archive", primary, tgt, []topoPoint{
				{right(primary), out}, {archBus, out}, {archBus, cy(tgt)}, {left(tgt), cy(tgt)},
			})
			if len(targets) > 1 && i < len(targets)-1 {
				dot("archive", archBus, cy(tgt))
			}
		}
	}

	view.Edges = edges
	view.Graph.Links = links
	view.Legend = topoLegend(links)

	// --- Frames around what was actually drawn. ---

	frame := func(label string, members []*TopoNode) {
		if len(members) == 0 {
			return
		}
		minX, minY := members[0].X, members[0].Y
		maxX, maxY := members[0].X+members[0].W, members[0].Y+members[0].H
		for _, m := range members[1:] {
			minX = minI(minX, m.X)
			minY = minI(minY, m.Y)
			maxX = maxI(maxX, m.X+m.W)
			maxY = maxI(maxY, m.Y+m.H)
		}
		view.Frames = append(view.Frames, TopoFrame{
			Label: label,
			X:     minX - grpPad, Y: minY - grpPad - grpLabelBand,
			W: maxX - minX + 2*grpPad, H: maxY - minY + 2*grpPad + grpLabelBand,
		})
	}
	var poolMembers []*TopoNode
	for _, pl := range poolers {
		poolMembers = append(poolMembers, pl.node)
	}
	frame("Poolers", poolMembers)
	clusterMembers := []*TopoNode{rw, ro}
	if primary != nil {
		clusterMembers = append(clusterMembers, primary)
	}
	clusterMembers = append(clusterMembers, replicas...)
	frame("Cluster", clusterMembers)
	var storeMembers []*TopoNode
	if store != nil {
		storeMembers = append(storeMembers, store)
	}
	if snapshot != nil {
		storeMembers = append(storeMembers, snapshot)
	}
	for _, c := range claims {
		storeMembers = append(storeMembers, c.node)
	}
	frame("Storage", storeMembers)

	// Every frame's top rides the same line, so the three category
	// labels read as one header row; the bottoms keep following their
	// own content.
	top := view.Frames[0].Y
	for _, f := range view.Frames[1:] {
		if f.Y < top {
			top = f.Y
		}
	}
	for i := range view.Frames {
		view.Frames[i].H += view.Frames[i].Y - top
		view.Frames[i].Y = top
	}

	// The viewBox wraps every frame with the outer margin.
	bottom := 0
	for _, f := range view.Frames {
		if f.Y+f.H > bottom {
			bottom = f.Y + f.H
		}
	}
	view.Width = width
	view.Height = bottom + grpMargin

	for i := range view.Nodes {
		wirePlace(&view.Nodes[i], rowsByID[view.Nodes[i].ID])
	}
	view.Graph.Nodes = wireGraphNodes(view.Nodes, rowsByID)
	view.Caption = "The same observations as the drawing above, grouped by role: poolers, the cluster, and everything its data rests on. Placement is fixed — rw above ro, the primary left of its replicas, each claim beside its instance."
	return view
}

// claimRows describes one PersistentVolumeClaim box.
func claimRows(v VolumeRowView) []TopoGraphText {
	rows := []TopoGraphText{{C: "label", T: v.Name}}
	line := v.Purpose
	if line != "" && v.Phase != "" {
		line += " · "
	}
	line += v.Phase
	if line != "" {
		rows = append(rows, TopoGraphText{C: "sub", T: line})
	}
	disk := v.Capacity
	if disk != "" && v.StorageClass != "" {
		disk += " · "
	}
	disk += v.StorageClass
	if disk != "" {
		rows = append(rows, TopoGraphText{C: "disk", T: disk})
	}
	return rows
}

// stackAround places a column of boxes centred on a level, pushed down
// when the level would ride into the space above. Returns the bottom.
func stackAround(boxes []*TopoNode, x, level, minTop float64) float64 {
	if len(boxes) == 0 {
		return minTop
	}
	total := 0.0
	for _, b := range boxes {
		total += float64(b.H)
	}
	total += float64(len(boxes)-1) * grpRowGap
	top := level - total/2
	if top < minTop {
		top = minTop
	}
	for _, b := range boxes {
		b.X = int(x)
		b.Y = int(top)
		top += float64(b.H) + grpRowGap
	}
	return top - grpRowGap
}

func maxW(a, b int) int { return maxI(a, b) }

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}
