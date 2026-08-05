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
	"strings"
)

// The grouped wiring drawing. A derivation of the operator's own
// draw.io sketch of the cluster, laid out the same way that sketch
// was: no layout engine, every placement a stated rule.
//
//   - Columns, left to right: poolers, services, the primary, the
//     replicas, storage. The primary stands left of its replicas; the
//     claims stand right of the instances, staggered over two columns
//     so consecutive instances never fight for the same corridor.
//   - A top band holds what feeds and receives the backup path: the
//     ScheduledBackup boxes over the primary, the object store over the
//     Kubernetes storage. Both drop out of the band into the drawing.
//   - Rows: rw above ro, for the poolers and the services alike. In the
//     Kubernetes storage frame the snapshots sit on top; each claim
//     group is centred on its own instance, so a lone claim's volume
//     wire is a straight line.
//   - Five dotted frames name the categories, each in its domain's
//     colour: Poolers, Cluster, Backups, Kubernetes storage, Object
//     storage. The object store's endpoint rides its frame label — it
//     describes the region, not one bucket. The trunk buses run in the
//     alleys between frames.
//   - A flow that fans out leaves its box once: one trunk to a bus, one
//     branch per destination, a dot on each tee. Replication leaves the
//     primary at one port and splits; the read service reaches its
//     replicas the same way; an instance reaches its claims the same
//     way. The whole backup path — schedule to primary, primary to
//     snapshots and object store — shares one line style, so it can be
//     followed across three frames by colour alone.
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
	// grpRowGap separates stacked boxes in a column, and a top-band
	// frame from the frame below it.
	grpRowGap = 28
	// grpPad is a frame's padding around its boxes.
	grpPad = 16
	// grpLabelBand is the frame headroom holding the category label.
	grpLabelBand = 30
	// grpPortOff separates the ports sharing a box edge, so the archive
	// and replication wires leave the primary as distinct lines rather
	// than one smear.
	grpPortOff = 15
	// grpMargin is the viewBox margin around everything.
	grpMargin = 8
	// grpWPvc is the claim boxes' width.
	grpWPvc = 200
	// grpWBak is the scheduled-backup boxes' width.
	grpWBak = 210
	// grpClaimGap separates the two claim columns; the second column's
	// drop bus runs inside it.
	grpClaimGap = 24
	// grpClaimStack separates two claims of the same instance.
	grpClaimStack = 10
	// grpBusInset is how far left of its claim column a disk drop bus
	// runs.
	grpBusInset = 10
	// grpMaxSched bounds the scheduled-backup boxes; more become one
	// "+N more" box, exactly like the server cap.
	grpMaxSched = 3
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
		Title: "Physical wiring",
		Aria:  "The cluster's wiring grouped into poolers, cluster, backups and storage, with one trunk per fanned flow",
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
	if p.Backups != nil {
		capacity += len(p.Backups.ScheduledRows)
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
	cx := func(n *TopoNode) float64 { return float64(n.X) + float64(n.W)/2 }
	right := func(n *TopoNode) float64 { return float64(n.X + n.W) }
	left := func(n *TopoNode) float64 { return float64(n.X) }
	bottom := func(n *TopoNode) float64 { return float64(n.Y + n.H) }

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
	// The reverse map lets a flow the operator attributed to an instance
	// by name — a base backup's source — leave that instance's box.
	byName := map[string]*TopoNode{}
	for i, s := range servers {
		id := fmt.Sprintf("srv-%d", i)
		n := add(id, s.kind, s.state, wireWSrv, s.rows)
		if s.name != "" {
			names[id] = s.name
			byName[s.name] = n
		}
		if s.kind == "primary" && primary == nil {
			primary = n
		} else {
			replicas = append(replicas, n)
		}
	}

	// Scheduled backups, bounded like the servers are.
	var schedules []*TopoNode
	if p.Backups != nil {
		shown := p.Backups.ScheduledRows
		extra := 0
		if len(shown) > grpMaxSched {
			extra = len(shown) - (grpMaxSched - 1)
			shown = shown[:grpMaxSched-1]
		}
		for i, s := range shown {
			schedules = append(schedules,
				add(fmt.Sprintf("sched-%d", i), "backup", "", grpWBak, scheduleRows(s)))
		}
		if extra > 0 {
			schedules = append(schedules,
				add("sched-more", "backup", "", grpWBak, []TopoGraphText{
					{C: "label", T: fmt.Sprintf("+%d more", extra)},
					{C: "sub", T: "backup schedules"},
				}))
		}
	}

	// Storage: the object store apart in its own frame, the snapshots
	// and claims in the Kubernetes storage frame. The endpoint moves to
	// the object frame's label, so the box does not repeat it.
	cloud, snapCfg := topoStorageConfigured(p)
	endpoint := ""
	if p.ObjectStoreDetail != nil {
		endpoint = p.ObjectStoreDetail.Endpoint
	}
	var store, snapshot *TopoNode
	if cloud {
		store = add("store", "storage", "", wireWStore, grpStoreRows(p, endpoint != ""))
	}
	if snapCfg {
		snapshot = add("snapshot", "snapshot", "", wireWStore, snapshotRows(p))
	}

	// Claims, grouped by the instance that holds them and staggered
	// over two columns: consecutive instances alternate, so one
	// instance's wires cross the other column in the corridor its
	// neighbours leave free.
	type grpClaimGroup struct {
		of    *TopoNode
		col   int
		nodes []*TopoNode
	}
	var claimGroups []grpClaimGroup
	claimCount := 0
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
			var group []*TopoNode
			for _, v := range p.Infrastructure.Volumes {
				if v.Instance != name {
					continue
				}
				group = append(group, add("pvc-"+v.Name, "pvc", "", grpWPvc, claimRows(v)))
			}
			if len(group) == 0 {
				continue
			}
			claimGroups = append(claimGroups, grpClaimGroup{of: inst, col: len(claimGroups) % 2, nodes: group})
			claimCount += len(group)
		}
	}
	twoCols := len(claimGroups) > 1

	// --- Placement: columns first, then the top band, then rows. ---

	hasPool := len(poolers) > 0
	hasK8s := snapshot != nil || claimCount > 0
	hasObject := store != nil

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
	var storeX, regionW float64
	if hasK8s || hasObject {
		x += grpAlley + grpPad
		storeX = x
		regionW = float64(grpWPvc)
		if twoCols {
			regionW = float64(2*grpWPvc + grpClaimGap)
		}
		if (hasObject || snapshot != nil) && regionW < wireWStore {
			regionW = wireWStore
		}
		// The endpoint rides the object frame's label; a long one
		// widens the region rather than bleeding past it. The estimate
		// is written out so the rule stays deterministic: the label
		// itself, a gap, the endpoint at its mono metrics, and slack
		// so the note never kisses the frame border.
		if hasObject && endpoint != "" {
			if need := float64(110 + 8 + 7*len(endpoint)); need > regionW {
				regionW = need
			}
		}
		x += regionW + grpPad
	}
	width := int(x) + grpMargin

	// The top band: the schedules over the primary column, the object
	// store over the storage region. Either lifts the frame below it;
	// with neither, the drawing keeps its one-row shape.
	bandTop := float64(grpMargin)
	bandContent := bandTop + grpLabelBand + grpPad
	primCx := primX + float64(wireWSrv)/2
	clusterTop := bandTop
	var bakLeft, bakRight, bakBottom float64
	if len(schedules) > 0 {
		total := float64(len(schedules)*grpWBak + (len(schedules)-1)*grpPad)
		sx := primCx - total/2
		bakLeft = sx
		bakBottom = bandContent
		for _, s := range schedules {
			s.X = int(sx)
			s.Y = int(bandContent)
			sx += float64(grpWBak + grpPad)
			if b := bottom(s); b > bakBottom {
				bakBottom = b
			}
		}
		bakRight = sx - grpPad
		clusterTop = bakBottom + grpPad + grpRowGap
	}
	k8sTop := bandTop
	var objBottom float64
	if hasObject {
		store.X = int(storeX)
		store.Y = int(bandContent)
		objBottom = bottom(store)
		if hasK8s {
			k8sTop = objBottom + grpPad + grpRowGap
		}
	}

	// Instance rows, below the cluster frame's label band.
	clusterContent := clusterTop + grpLabelBand + grpPad
	rowY := clusterContent
	if primary != nil {
		centre(primary, primX+float64(primary.W)/2, rowY+float64(primary.H)/2)
		rowY += float64(primary.H) + grpRowGap
	}
	for _, r := range replicas {
		centre(r, replX+float64(r.W)/2, rowY+float64(r.H)/2)
		rowY += float64(r.H) + grpRowGap
	}

	// The Kubernetes storage stack: snapshots on top, then the claim
	// groups, each centred on its instance and pushed down only when
	// its own column is already occupied.
	k8sContent := k8sTop + grpLabelBand + grpPad
	sY := k8sContent
	if snapshot != nil {
		snapshot.X = int(storeX)
		snapshot.Y = int(sY)
		sY = bottom(snapshot) + grpRowGap
	}
	colX := [2]float64{storeX, storeX + grpWPvc + grpClaimGap}
	nextFree := [2]float64{sY, sY}
	for _, g := range claimGroups {
		total := float64((len(g.nodes) - 1) * grpClaimStack)
		for _, n := range g.nodes {
			total += float64(n.H)
		}
		top := cy(g.of) - total/2
		if top < nextFree[g.col] {
			top = nextFree[g.col]
		}
		for _, n := range g.nodes {
			n.X = int(colX[g.col])
			n.Y = int(top)
			top += float64(n.H) + grpClaimStack
		}
		nextFree[g.col] = top - grpClaimStack + grpRowGap
	}

	// Services follow the servers: the write service level with the
	// primary, the read service level with the first replica.
	rwCy := clusterContent + float64(rw.H)/2
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
	poolBottom := stackAround(rwPool, poolX, cy(rw), clusterContent)
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

	// Each schedule drops out of the Backups frame into the primary's
	// top edge: a collector bus in the band-to-cluster gap, one drop.
	// With a single schedule the whole route collapses to a straight
	// vertical line, exactly the sketch this drawing derives from.
	if primary != nil && len(schedules) > 0 {
		busY := clusterTop - float64(grpRowGap)/2
		for _, s := range schedules {
			wire("archive", s, primary, []topoPoint{
				{cx(s), bottom(s)}, {cx(s), busY}, {primCx, busY}, {primCx, float64(primary.Y)},
			})
			if len(schedules) > 1 && cx(s) != primCx {
				dot("archive", cx(s), busY)
			}
		}
	}

	// Replication leaves the primary once: one trunk to the bus in the
	// storage alley, then a branch left into each replica's right edge.
	// The two object-store flows get their own lanes in the alley so a
	// cluster that ships WAL and takes base backups from the same
	// instance still reads as two wires rather than one thick smear.
	walBus := clusterRight + grpAlley*0.24
	baseBus := clusterRight + grpAlley*0.44
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

	// Each instance onto its claims: one exit, a drop bus beside the
	// claim column, a branch per claim. A lone level claim keeps its
	// straight wire — the route collapses.
	for _, g := range claimGroups {
		busX := colX[g.col] - grpBusInset
		out := cy(g.of)
		for _, c := range g.nodes {
			wire("disk", g.of, c, []topoPoint{
				{right(g.of), out}, {busX, out}, {busX, cy(c)}, {left(c), cy(c)},
			})
		}
		if len(g.nodes) > 1 {
			dot("disk", busX, out)
		}
	}

	// The trunk climbs the alley to the snapshots and on up into the
	// object storage frame. Two flows end in that frame and they do not
	// share an origin: shipping WAL is the primary's continuous duty,
	// while a base backup is served by whichever instance the operator
	// picked — by default a standby, not the primary. One wire for both
	// would assert an origin the Backup resources deny, so the WAL wire
	// leaves the primary and the base wire leaves the instance the
	// operator actually named.
	//
	// When no Backup named an instance the base wire is simply not
	// drawn: the console has no word on where that flow began, and
	// inventing the primary is the claim this split exists to retire.
	// The frame's own rows still say the repository holds both
	// prefixes, so nothing about the destination goes unsaid.
	var baseSrc *TopoNode
	if p.Backups != nil && p.Backups.BaseSourceInstance != "" {
		baseSrc = byName[p.Backups.BaseSourceInstance]
	}
	// Each flow enters the frames it reaches at its own port, so two
	// arrows land on the Cloud backup box rather than one doubled line.
	archTrunk := func(kind string, from *TopoNode, bus, inOff float64, targets []*TopoNode) {
		out := cy(from) - grpPortOff
		for i, tgt := range targets {
			in := cy(tgt) + inOff
			wire(kind, from, tgt, []topoPoint{
				{right(from), out}, {bus, out}, {bus, in}, {left(tgt), in},
			})
			if len(targets) > 1 && i < len(targets)-1 {
				dot(kind, bus, in)
			}
		}
	}
	if primary != nil && store != nil {
		archTrunk("wal", primary, walBus, -grpPortOff/2, []*TopoNode{store})
	}
	// A volume snapshot is a base backup taken by another method, so it
	// leaves the same attributed instance rather than the primary.
	if baseSrc != nil {
		var targets []*TopoNode
		if snapshot != nil {
			targets = append(targets, snapshot)
		}
		if store != nil {
			targets = append(targets, store)
		}
		if len(targets) > 0 {
			archTrunk("archive", baseSrc, baseBus, grpPortOff/2, targets)
			// When the operator picked the primary, both flows leave one
			// port: mark the split the way every other fan marks it.
			if baseSrc == primary && store != nil {
				dot("archive", right(baseSrc), cy(baseSrc)-grpPortOff)
			}
		}
	}

	view.Edges = edges
	view.Graph.Links = links
	view.Legend = topoLegend(links)

	// --- Frames around what was actually drawn. ---

	bounds := func(members []*TopoNode) (minX, minY, maxX, maxY int) {
		minX, minY = members[0].X, members[0].Y
		maxX, maxY = members[0].X+members[0].W, members[0].Y+members[0].H
		for _, m := range members[1:] {
			minX = minI(minX, m.X)
			minY = minI(minY, m.Y)
			maxX = maxI(maxX, m.X+m.W)
			maxY = maxI(maxY, m.Y+m.H)
		}
		return
	}
	// The poolers and the cluster share the main row: both frames hang
	// from the cluster's top line, and each bottom follows its content.
	if hasPool {
		var poolMembers []*TopoNode
		for _, pl := range poolers {
			poolMembers = append(poolMembers, pl.node)
		}
		minX, _, maxX, maxY := bounds(poolMembers)
		view.Frames = append(view.Frames, TopoFrame{
			Label: "Poolers", Kind: "pool",
			X: minX - grpPad, Y: int(clusterTop),
			W: maxX - minX + 2*grpPad, H: maxY + grpPad - int(clusterTop),
		})
	}
	clusterMembers := []*TopoNode{rw, ro}
	if primary != nil {
		clusterMembers = append(clusterMembers, primary)
	}
	clusterMembers = append(clusterMembers, replicas...)
	{
		minX, _, maxX, maxY := bounds(clusterMembers)
		view.Frames = append(view.Frames, TopoFrame{
			Label: "Cluster", Kind: "cluster",
			X: minX - grpPad, Y: int(clusterTop),
			W: maxX - minX + 2*grpPad, H: maxY + grpPad - int(clusterTop),
		})
	}
	if len(schedules) > 0 {
		view.Frames = append(view.Frames, TopoFrame{
			Label: "Backups", Kind: "backup",
			X: int(bakLeft) - grpPad, Y: int(bandTop),
			W: int(bakRight-bakLeft) + 2*grpPad, H: int(bakBottom + grpPad - bandTop),
		})
	}
	if hasK8s {
		var members []*TopoNode
		if snapshot != nil {
			members = append(members, snapshot)
		}
		for _, g := range claimGroups {
			members = append(members, g.nodes...)
		}
		_, _, _, maxY := bounds(members)
		view.Frames = append(view.Frames, TopoFrame{
			Label: "Kubernetes storage", Kind: "store",
			X: int(storeX) - grpPad, Y: int(k8sTop),
			W: int(regionW) + 2*grpPad, H: maxY + grpPad - int(k8sTop),
		})
	}
	if hasObject {
		view.Frames = append(view.Frames, TopoFrame{
			Label: "Object storage", Note: endpoint, Kind: "store",
			X: int(storeX) - grpPad, Y: int(bandTop),
			W: int(regionW) + 2*grpPad, H: int(objBottom + grpPad - bandTop),
		})
	}

	// The viewBox wraps every frame with the outer margin.
	bottomEdge := 0
	for _, f := range view.Frames {
		if f.Y+f.H > bottomEdge {
			bottomEdge = f.Y + f.H
		}
	}
	view.Width = width
	view.Height = bottomEdge + grpMargin

	for i := range view.Nodes {
		wirePlace(&view.Nodes[i], rowsByID[view.Nodes[i].ID])
	}
	view.Graph.Nodes = wireGraphNodes(view.Nodes, rowsByID)
	view.Caption = "The observed wiring grouped by role: poolers, the cluster, its backup schedules, and everything the data rests on — Kubernetes claims and snapshots apart from the object store. Placement is fixed — rw above ro, the primary left of its replicas, the claims staggered beside their instances."
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

// scheduleRows describes one ScheduledBackup box: the resource name,
// its cron expression exactly as the operator wrote it, and its state
// — suspended, or the next firing when one was reported.
func scheduleRows(s ScheduledBackupRowView) []TopoGraphText {
	rows := []TopoGraphText{{C: "label", T: s.Name}}
	if s.Schedule != "" && s.Schedule != unknown {
		rows = append(rows, TopoGraphText{C: "disk", T: s.Schedule})
	}
	switch {
	case s.Suspended == "true":
		rows = append(rows, TopoGraphText{C: "sub", T: "suspended"})
	case s.NextSchedule != "" && s.NextSchedule != unknown:
		rows = append(rows, TopoGraphText{C: "sub", T: "next " + s.NextSchedule})
	}
	return rows
}

// grpStoreRows is objectStoreRows with the endpoint row dropped when
// the object storage frame already says it: a fact stated once, at the
// level it describes.
func grpStoreRows(p *Page, endpointOnFrame bool) []TopoGraphText {
	rows := objectStoreRows(p)
	if !endpointOnFrame {
		return rows
	}
	kept := rows[:0:0]
	for _, r := range rows {
		if r.C == "sub" && strings.HasPrefix(r.T, "endpoint ") {
			continue
		}
		kept = append(kept, r)
	}
	return kept
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
