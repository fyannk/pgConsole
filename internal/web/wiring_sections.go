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

// The section drawings: each section screen opens with the same schema
// language the cluster overview uses — dotted frames, observed boxes,
// labelled trunk wires — adapted to the one question that screen
// answers. The backups screen draws the backup path, the poolers screen
// the client path, the databases screen the declared objects. All are
// rule-based arithmetic like the drawings they derive from: no engine,
// deterministic, complete without a script.

// secMaxBoxes bounds each drawn list; more become one "+N more" box.
const secMaxBoxes = 6

// secMaxBackups bounds the drawn Backup catalog, which grows with every
// scheduled run and would otherwise be the tallest thing on the screen
// within a week. Three, and the middle one summarises: the newest, a
// count of what sits between, and the oldest.
//
// A truncating "+N more" would be the wrong shape here. The catalog is
// a time series, so the two ends are the informative rows — the newest
// says whether backups are still running, the oldest says how far the
// retained record reaches — and the middle is a quantity, not a list.
const secMaxBackups = 3

// elideMiddle picks the rows a bounded list shows: everything when it
// fits, otherwise the first and the last with the count between them.
// count is 0 when nothing was dropped.
func elideMiddle[T any](rows []T, max int) (head []T, count int, tail []T) {
	if max < 3 || len(rows) <= max {
		return rows, 0, nil
	}
	return rows[:1], len(rows) - 2, rows[len(rows)-1:]
}

// secWPod is the pooler-pod boxes' width: a deployment pod name
// carries two hash suffixes, so these boxes run wider than their
// pooler's.
const secWPod = 265

// clusterRootRows states the Cluster box shared by the inventory-style
// drawings: identity and operator-reported standing.
func clusterRootRows(p *Page) []TopoGraphText {
	rows := []TopoGraphText{{C: "label", T: "Cluster/" + p.ClusterName}}
	if p.Cluster.Phase != "" && p.Cluster.Phase != unknown {
		rows = append(rows, TopoGraphText{C: "sub", T: p.Cluster.Phase})
	}
	if p.Cluster.Instances != "" && p.Cluster.Instances != unknown {
		rows = append(rows, TopoGraphText{C: "sub", T: p.Cluster.Instances + " instances"})
	}
	if image := shortImage(p.Cluster.Image); image != "" {
		rows = append(rows, TopoGraphText{C: "disk", T: image})
	}
	return rows
}

// backupBoxRows states one Backup resource: the phase alone, without
// its attribution rider — the frame is operator-reported as a whole.
func backupBoxRows(b BackupRowView) []TopoGraphText {
	rows := []TopoGraphText{{C: "label", T: b.Name}}
	line, _, _ := strings.Cut(b.Phase, " — ")
	if b.Method != "" && b.Method != unknown {
		if line != "" {
			line += " · "
		}
		line += b.Method
	}
	if line != "" {
		rows = append(rows, TopoGraphText{C: "sub", T: line})
	}
	if b.Age != "" && b.Age != unknown {
		rows = append(rows, TopoGraphText{C: "disk", T: b.Age + " old"})
	}
	return rows
}

// snapshotBoxRows states one observed VolumeSnapshot.
func snapshotBoxRows(s SnapshotRowView) []TopoGraphText {
	rows := []TopoGraphText{{C: "label", T: s.Name}}
	if s.SourceClaim != "" && s.SourceClaim != unknown {
		rows = append(rows, TopoGraphText{C: "sub", T: "of " + s.SourceClaim})
	}
	line := ""
	if s.RestoreSize != "" && s.RestoreSize != unknown {
		line = s.RestoreSize
	}
	if s.Age != "" && s.Age != unknown {
		if line != "" {
			line += " · "
		}
		line += s.Age + " old"
	}
	if line != "" {
		rows = append(rows, TopoGraphText{C: "disk", T: line})
	}
	return rows
}

// buildBackupsDrawing derives the backup-path schema: the schedules
// that trigger the cluster, the destinations its archive reaches, and
// the Backup records the operator reports. Nil when the page carries
// nothing on the path.
func buildBackupsDrawing(p *Page) *TopologyView {
	if p.Cluster == nil || p.Cluster.Absent {
		return nil
	}
	cloud, snapCfg := topoStorageConfigured(p)
	schedules := 0
	catalog := 0
	if p.Backups != nil {
		schedules = len(p.Backups.ScheduledRows)
		catalog = len(p.Backups.Rows)
	}
	snapshots := 0
	if p.Infrastructure != nil {
		snapshots = len(p.Infrastructure.Snapshots)
	}
	if schedules == 0 && catalog == 0 && !cloud && !snapCfg && snapshots == 0 {
		return nil
	}

	view := &TopologyView{
		Title: "The backup path",
		Aria:  "The schedules that trigger this cluster, the destinations its archive reaches, and the reported Backup records",
	}

	capacity := 1 + minI(schedules, grpMaxSched) + minI(catalog, secMaxBackups) + minI(snapshots, secMaxBoxes) + 2
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
	cy := func(n *TopoNode) float64 { return float64(n.Y) + float64(n.H)/2 }
	right := func(n *TopoNode) float64 { return float64(n.X + n.W) }

	// stack places boxes top-down at x and returns the frame bottom.
	stack := func(boxes []*TopoNode, x, y float64) float64 {
		for _, b := range boxes {
			b.X = int(x)
			b.Y = int(y)
			y += float64(b.H) + chBoxGap
		}
		return y - chBoxGap
	}

	frameW := chWBox + 2*grpPad
	top := float64(grpMargin)
	content := top + grpLabelBand + grpPad

	// Columns: schedules, the cluster, the destinations. The catalog
	// hangs under the cluster.
	x := float64(grpMargin)
	schedX := x
	if schedules > 0 {
		x += float64(frameW) + grpAlley
	}
	rootX := x + grpPad
	x = rootX + chWBox + grpAlley
	destX := x

	// Destination boxes, stacked as two frames on the right. The
	// endpoint rides the object frame's label, so a long one widens
	// that frame — the same deterministic estimate the overview uses.
	endpoint := ""
	if p.ObjectStoreDetail != nil {
		endpoint = p.ObjectStoreDetail.Endpoint
	}
	storeFrameW := frameW
	if endpoint != "" {
		if need := 110 + 8 + 7*len(endpoint); need > storeFrameW {
			storeFrameW = need
		}
	}
	var frames []TopoFrame
	destY := top
	var store *TopoNode
	if cloud {
		store = add("store", "storage", "", chWBox, grpStoreRows(p, endpoint != ""))
		bottom := stack([]*TopoNode{store}, destX+grpPad, destY+grpLabelBand+grpPad)
		frames = append(frames, TopoFrame{
			Label: "Object storage", Note: endpoint, Kind: "store",
			X: int(destX), Y: int(destY), W: storeFrameW, H: int(bottom + grpPad - destY),
		})
		destY = bottom + grpPad + grpRowGap
	}
	// Frames are appended below, so remember the two wire targets by
	// value rather than by pointer into the reallocating slice.
	var snapFrame TopoFrame
	hasSnapFrame := false
	if snapCfg || snapshots > 0 {
		var boxes []*TopoNode
		if snapshots > 0 {
			rows := p.Infrastructure.Snapshots
			extra := 0
			if len(rows) > secMaxBoxes {
				extra = len(rows) - (secMaxBoxes - 1)
				rows = rows[:secMaxBoxes-1]
			}
			for i, s := range rows {
				boxes = append(boxes, add(fmt.Sprintf("snap-%d", i), "snapshot", "", chWBox, snapshotBoxRows(s)))
			}
			if extra > 0 {
				boxes = append(boxes, add("snap-more", "snapshot", "", chWBox, []TopoGraphText{
					{C: "label", T: fmt.Sprintf("+%d more", extra)},
				}))
			}
		} else {
			boxes = append(boxes, add("snapshot", "snapshot", "", chWBox, snapshotRows(p)))
		}
		bottom := stack(boxes, destX+grpPad, destY+grpLabelBand+grpPad)
		snapFrame = TopoFrame{
			Label: "Volume snapshots", Kind: "store",
			X: int(destX), Y: int(destY), W: frameW, H: int(bottom + grpPad - destY),
		}
		hasSnapFrame = true
		frames = append(frames, snapFrame)
		destY = bottom + grpPad + grpRowGap
	}
	destBottom := destY - grpRowGap

	// The cluster faces the things it actually wires to, not the extent
	// of the column they sit in. Centring on the column put the cluster
	// half a label band above a lone destination box, and every wire out
	// of it then had to step to reach anything — the drawing was full of
	// jogs that meant nothing. Averaging the wire endpoints makes the
	// single-destination case exactly straight and keeps the multiple
	// case balanced between them.
	//
	// Everything placed so far is the destination column, so remember
	// where it ends: if the cluster's own height would push it above the
	// frame label band, that column moves down to meet it rather than
	// the cluster being clamped. Clamping was the bug — it left the
	// alignment silently a few pixels out, which is exactly the kind of
	// near-miss a reader reads as sloppiness rather than as meaning.
	destNodes := len(nodes)
	destFrames := len(frames)
	root := add("cluster", "primary", "", chWBox, clusterRootRows(p))
	root.X = int(rootX)
	var targets []float64
	if store != nil {
		targets = append(targets, cy(store))
	}
	if hasSnapFrame {
		targets = append(targets, float64(snapFrame.Y)+float64(snapFrame.H)/2)
	}
	if len(targets) > 0 {
		sum := 0.0
		for _, t := range targets {
			sum += t
		}
		root.Y = int(sum/float64(len(targets))) - root.H/2
		if shift := int(content) - root.Y; shift > 0 {
			for i := 0; i < destNodes; i++ {
				nodes[i].Y += shift
			}
			for i := 0; i < destFrames; i++ {
				frames[i].Y += shift
			}
			snapFrame.Y += shift
			root.Y = int(content)
		}
	} else {
		oppositeBottom := destBottom
		if oppositeBottom <= top {
			oppositeBottom = content + float64(root.H)
		}
		root.Y = int((top+oppositeBottom)/2) - root.H/2
		if root.Y < int(content) {
			root.Y = int(content)
		}
	}
	rootCy := cy(root)

	// The schedules frame on the left, centred on the cluster for the
	// same reason: one schedule should wire to it in a straight line.
	var schedBoxes []*TopoNode
	if schedules > 0 {
		shown := p.Backups.ScheduledRows
		extra := 0
		if len(shown) > grpMaxSched {
			extra = len(shown) - (grpMaxSched - 1)
			shown = shown[:grpMaxSched-1]
		}
		for i, s := range shown {
			schedBoxes = append(schedBoxes, add(fmt.Sprintf("sched-%d", i), "backup", "", chWBox, scheduleRows(s)))
		}
		if extra > 0 {
			schedBoxes = append(schedBoxes, add("sched-more", "backup", "", chWBox, []TopoGraphText{
				{C: "label", T: fmt.Sprintf("+%d more", extra)},
				{C: "sub", T: "backup schedules"},
			}))
		}
		stackH := 0.0
		for i, b := range schedBoxes {
			if i > 0 {
				stackH += chBoxGap
			}
			stackH += float64(b.H)
		}
		stackTop := rootCy - stackH/2
		if stackTop < content {
			stackTop = content
		}
		bottom := stack(schedBoxes, schedX+grpPad, stackTop)
		frameTop := stackTop - grpPad - grpLabelBand
		if frameTop < top {
			frameTop = top
		}
		frames = append(frames, TopoFrame{
			Label: "Backup schedules", Kind: "backup",
			X: int(schedX), Y: int(frameTop), W: frameW, H: int(bottom + grpPad - frameTop),
		})
	}

	// The catalog of Backup records, under the cluster.
	var catFrame TopoFrame
	hasCatFrame := false
	if catalog > 0 {
		// Newest first, so head is the most recent run and tail the
		// oldest record still reported.
		head, between, tail := elideMiddle(p.Backups.Rows, secMaxBackups)
		var boxes []*TopoNode
		for i, b := range head {
			boxes = append(boxes, add(fmt.Sprintf("bak-%d", i), "storage", "", chWBox, backupBoxRows(b)))
		}
		if between > 0 {
			boxes = append(boxes, add("bak-between", "storage", "", chWBox, []TopoGraphText{
				{C: "label", T: fmt.Sprintf("%d more backups", between)},
				{C: "sub", T: "between these two"},
			}))
		}
		for i, b := range tail {
			boxes = append(boxes, add(fmt.Sprintf("bak-tail-%d", i), "storage", "", chWBox, backupBoxRows(b)))
		}
		frameX := float64(root.X+root.W/2) - float64(frameW)/2
		frameTop := float64(root.Y+root.H) + grpRowGap + 14
		bottom := stack(boxes, frameX+grpPad, frameTop+grpLabelBand+grpPad)
		catFrame = TopoFrame{
			Label: "Backups", Kind: "backup",
			X: int(frameX), Y: int(frameTop), W: frameW, H: int(bottom + grpPad - frameTop),
		}
		hasCatFrame = true
		frames = append(frames, catFrame)
	}

	view.Nodes = nodes

	// --- Wires: the whole path in the one backup style. ---

	var edges []TopoEdge
	var links []TopoGraphLink
	wire := func(from, to string, label string, points []topoPoint) {
		edge := TopoEdge{Kind: "archive", Path: roundedRoute(corners(points)), Label: label}
		if label != "" {
			edge.LabelX = int((points[0].x + points[len(points)-1].x) / 2)
			edge.LabelY = int(points[0].y) - 7
		}
		edges = append(edges, edge)
		links = append(links, TopoGraphLink{Source: from, Target: to, Kind: "archive"})
	}
	dot := func(x, y float64) {
		view.Dots = append(view.Dots, TopoDot{Kind: "archive", X: int(x), Y: int(y)})
	}

	// Schedules fan into the cluster.
	if len(schedBoxes) > 0 {
		bus := rootX - grpAlley/2
		for i, s := range schedBoxes {
			label := ""
			if i == 0 {
				label = "triggers"
			}
			wire(s.ID, "cluster", label, []topoPoint{
				{right(s), cy(s)}, {bus, cy(s)}, {bus, rootCy}, {float64(root.X), rootCy},
			})
			if len(schedBoxes) > 1 && cy(s) != rootCy {
				dot(bus, cy(s))
			}
		}
	}
	// The archive reaches each destination.
	// The destination frames name themselves, so their wires carry no
	// label — a label here would ride over the cluster box.
	//
	// The port offset exists to keep two wires leaving the same edge
	// from smearing into one line. With a single destination there is
	// nothing to separate, and applying it anyway bent a wire that had
	// every reason to be straight — so it is spent only when earned.
	port := 0.0
	if len(targets) > 1 {
		port = grpPortOff
	}
	if store != nil {
		out := rootCy - port
		bus := destX - grpAlley*0.5
		wire("cluster", "store", "", []topoPoint{
			{right(root), out}, {bus, out}, {bus, cy(store)}, {float64(store.X), cy(store)},
		})
	}
	if hasSnapFrame {
		out := rootCy + port
		bus := destX - grpAlley*0.25
		frameCy := float64(snapFrame.Y) + float64(snapFrame.H)/2
		wire("cluster", "snapshots", "", []topoPoint{
			{right(root), out}, {bus, out}, {bus, frameCy}, {float64(snapFrame.X), frameCy},
		})
	}
	// The records the operator keeps of it.
	if hasCatFrame {
		rootCx := float64(root.X) + float64(root.W)/2
		wire("cluster", "backups", "records", []topoPoint{
			{rootCx, float64(root.Y + root.H)}, {rootCx, float64(catFrame.Y)},
		})
		edges[len(edges)-1].LabelX = int(rootCx) + 36
		edges[len(edges)-1].LabelY = catFrame.Y - 6
	}

	view.Edges = edges
	view.Graph.Links = links
	view.Legend = topoLegend(links)
	view.Frames = frames

	right2 := root.X + root.W
	bottom2 := root.Y + root.H
	for _, f := range view.Frames {
		right2 = maxI(right2, f.X+f.W)
		bottom2 = maxI(bottom2, f.Y+f.H)
	}
	view.Width = right2 + grpMargin
	view.Height = bottom2 + grpMargin

	for i := range view.Nodes {
		wirePlace(&view.Nodes[i], rowsByID[view.Nodes[i].ID])
	}
	view.Graph.Nodes = wireGraphNodes(view.Nodes, rowsByID)
	view.Caption = "The backup path as observed: ScheduledBackup resources trigger the cluster, the archive reaches the destinations actually configured, and the Backups frame lists the operator's records — claims about what ran, not proof of what the repository holds."
	return view
}

// buildPoolersWiring derives the client-path schema: the poolers, the
// pods that run them, the services they front, and the instances those
// services reach. Nil without observed poolers or servers.
func buildPoolersWiring(p *Page) *TopologyView {
	if p.Cluster == nil || p.Cluster.Absent {
		return nil
	}
	if p.Poolers == nil || len(p.Poolers.Poolers) == 0 {
		return nil
	}
	servers := wireServers(p, false)
	if len(servers) == 0 {
		return nil
	}

	view := &TopologyView{
		Title: "The client path",
		Aria:  "Poolers and their pods, the services they front, and the instances behind those services",
	}

	podRows := 0
	if p.PoolerPods != nil {
		podRows = len(p.PoolerPods.Rows)
	}
	capacity := 2 + len(servers) + len(p.Poolers.Poolers) + minI(podRows, secMaxBoxes)
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
	centre := func(n *TopoNode, cx, cyv float64) {
		n.X = int(cx) - n.W/2
		n.Y = int(cyv) - n.H/2
	}
	cy := func(n *TopoNode) float64 { return float64(n.Y) + float64(n.H)/2 }
	right := func(n *TopoNode) float64 { return float64(n.X + n.W) }
	left := func(n *TopoNode) float64 { return float64(n.X) }

	// Poolers, rw before ro, exactly as the overview drawing orders them.
	type secPooler struct {
		node *TopoNode
		name string
		ro   bool
	}
	var poolers []secPooler
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
		poolers = append(poolers, secPooler{
			node: add(fmt.Sprintf("pool-%d", i), "pooler", poolerState(pooler), wireWPool, rows),
			name: pooler.Name,
			ro:   pooler.TypeToken == "ro",
		})
	}

	// Services and servers, the overview drawing's cluster half.
	rwRows := endpointRows(p, "read-write", p.ClusterName+"-rw", "routes to the current primary")
	roRows := endpointRows(p, "read-only", p.ClusterName+"-ro", "routes to the read-only copies")
	rw := add("rw", "endpoint", "", wireWSvc, rwRows)
	ro := add("ro", "endpoint", "", wireWSvc, roRows)
	var primary *TopoNode
	var replicas []*TopoNode
	for i, s := range servers {
		n := add(fmt.Sprintf("srv-%d", i), s.kind, s.state, wireWSrv, s.rows)
		if s.kind == "primary" && primary == nil {
			primary = n
		} else {
			replicas = append(replicas, n)
		}
	}

	// The pods behind the poolers, wired to their pooler by the
	// ownership the roster already proved.
	type secPod struct {
		node *TopoNode
		of   string
	}
	var pods []secPod
	if p.PoolerPods != nil {
		rows := p.PoolerPods.Rows
		extra := 0
		if len(rows) > secMaxBoxes {
			extra = len(rows) - (secMaxBoxes - 1)
			rows = rows[:secMaxBoxes-1]
		}
		for i, pod := range rows {
			boxRows := []TopoGraphText{
				{C: "label", T: pod.Name},
				{C: "sub", T: instanceCondition(pod)},
			}
			if pod.Node != "" && pod.Node != unknown {
				boxRows = append(boxRows, TopoGraphText{C: "disk", T: "node " + pod.Node})
			}
			pods = append(pods, secPod{
				node: add(fmt.Sprintf("ppod-%d", i), "pooler", podState(pod), secWPod, boxRows),
				of:   pod.Role,
			})
		}
		if extra > 0 {
			pods = append(pods, secPod{node: add("ppod-more", "pooler", "", secWPod, []TopoGraphText{
				{C: "label", T: fmt.Sprintf("+%d more", extra)},
				{C: "sub", T: "pooler pods"},
			})})
		}
	}

	// --- Placement: pooler column, services column, instances. ---

	x := float64(grpMargin) + grpPad
	poolX := x
	x += wireWPool + grpPad + grpAlley + grpPad
	svcX := x
	x += wireWSvc + grpColGap
	primX := x
	x += wireWSrv + grpColGap
	replX := x
	x += wireWSrv + grpPad
	width := int(x) + grpMargin

	top := float64(grpMargin)
	content := top + grpLabelBand + grpPad
	rowY := content
	if primary != nil {
		centre(primary, primX+float64(primary.W)/2, rowY+float64(primary.H)/2)
		rowY += float64(primary.H) + grpRowGap
	}
	for _, r := range replicas {
		centre(r, replX+float64(r.W)/2, rowY+float64(r.H)/2)
		rowY += float64(r.H) + grpRowGap
	}
	rwCy := content + float64(rw.H)/2
	if primary != nil {
		rwCy = cy(primary)
	}
	centre(rw, svcX+float64(rw.W)/2, rwCy)
	roCy := float64(rw.Y+rw.H) + grpRowGap + float64(ro.H)/2
	if len(replicas) > 0 {
		roCy = cy(replicas[0])
	}
	centre(ro, svcX+float64(ro.W)/2, roCy)

	var rwPool, roPool []*TopoNode
	for _, pl := range poolers {
		if pl.ro {
			roPool = append(roPool, pl.node)
		} else {
			rwPool = append(rwPool, pl.node)
		}
	}
	poolBottom := stackAround(rwPool, poolX, cy(rw), content)
	poolBottom = stackAround(roPool, poolX, cy(ro), poolBottom+grpRowGap)

	// The pods frame under the poolers.
	var frames []TopoFrame
	poolFrameBottom := poolBottom + grpPad
	if len(pods) > 0 {
		podTop := poolFrameBottom + grpRowGap + 14
		y := podTop + grpLabelBand + grpPad
		for _, pod := range pods {
			pod.node.X = int(poolX)
			pod.node.Y = int(y)
			y += float64(pod.node.H) + chBoxGap
		}
		frames = append(frames, TopoFrame{
			Label: "Pooler pods", Kind: "pool",
			X: int(poolX) - grpPad, Y: int(podTop),
			W: secWPod + 2*grpPad, H: int(y - chBoxGap + grpPad - podTop),
		})
	}

	view.Nodes = nodes

	// --- Wires. ---

	var edges []TopoEdge
	var links []TopoGraphLink
	wire := func(kind string, from, to *TopoNode, points []topoPoint) {
		edges = append(edges, TopoEdge{Kind: kind, Path: roundedRoute(corners(points))})
		links = append(links, TopoGraphLink{Source: from.ID, Target: to.ID, Kind: kind})
	}
	dot := func(kind string, x, y float64) {
		view.Dots = append(view.Dots, TopoDot{Kind: kind, X: int(x), Y: int(y)})
	}

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
	if primary != nil {
		wire("write", rw, primary, []topoPoint{
			{right(rw), cy(rw)}, {left(primary), cy(primary)},
		})
	}
	readBus := svcX + wireWSvc + grpColGap/2
	for i, r := range replicas {
		wire("read", ro, r, []topoPoint{
			{right(ro), cy(ro)}, {readBus, cy(ro)}, {readBus, cy(r)}, {left(r), cy(r)},
		})
		if len(replicas) > 1 && i < len(replicas)-1 {
			dot("read", readBus, cy(r))
		}
	}
	// Each pod up into the pooler it runs, on a bus right of the column.
	podBus := poolX + wireWPool + grpPad + grpAlley*0.2
	byName := map[string]*TopoNode{}
	for _, pl := range poolers {
		byName[pl.name] = pl.node
	}
	for _, pod := range pods {
		owner := byName[pod.of]
		if owner == nil {
			continue
		}
		in := float64(owner.Y+owner.H) - 8
		wire("owns", pod.node, owner, []topoPoint{
			{right(pod.node), cy(pod.node)}, {podBus, cy(pod.node)}, {podBus, in}, {right(owner), in},
		})
	}

	view.Edges = edges
	view.Graph.Links = links
	view.Legend = topoLegend(links)

	// Frames: poolers and the cluster, plus the pods frame placed above.
	var poolMembers []*TopoNode
	for _, pl := range poolers {
		poolMembers = append(poolMembers, pl.node)
	}
	minX, _, maxX, maxY := secBounds(poolMembers)
	frames = append(frames, TopoFrame{
		Label: "Poolers", Kind: "pool",
		X: minX - grpPad, Y: int(top),
		W: maxX - minX + 2*grpPad, H: maxY + grpPad - int(top),
	})
	clusterMembers := []*TopoNode{rw, ro}
	if primary != nil {
		clusterMembers = append(clusterMembers, primary)
	}
	clusterMembers = append(clusterMembers, replicas...)
	minX, _, maxX, maxY = secBounds(clusterMembers)
	frames = append(frames, TopoFrame{
		Label: "Cluster", Kind: "cluster",
		X: minX - grpPad, Y: int(top),
		W: maxX - minX + 2*grpPad, H: maxY + grpPad - int(top),
	})
	view.Frames = frames

	bottom := 0
	for _, f := range view.Frames {
		bottom = maxI(bottom, f.Y+f.H)
	}
	view.Width = width
	view.Height = bottom + grpMargin

	for i := range view.Nodes {
		wirePlace(&view.Nodes[i], rowsByID[view.Nodes[i].ID])
	}
	view.Graph.Nodes = wireGraphNodes(view.Nodes, rowsByID)
	view.Caption = "The path a client takes: poolers front the services, the services reach the instances. The pods frame shows what actually runs each pooler, wired by the same ownership proof the roster uses. Every value is operator-reported or Kubernetes-observed; the console never connects to PgBouncer."
	return view
}

// secBounds is the bounding box of a set of placed nodes.
func secBounds(members []*TopoNode) (minX, minY, maxX, maxY int) {
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

// declaredState maps a reconciliation verdict onto the drawing's
// state tokens: applied is current, failed is degraded, and an
// unreported verdict stays unknown.
func declaredState(d DeclaredView) string {
	switch d.State {
	case "applied":
		return "current"
	case "failed":
		return "degraded"
	default:
		return unknown
	}
}

// buildDatabasesDrawing derives the declared-objects schema: the
// databases and what lives in them on the left, the cluster they are
// declared into in the middle, and the roles on the right. Nil when
// nothing is declared.
//
// Only objects naming this cluster reach here at all — the boundary in
// internal/kube drops the rest, so a role declared into a different
// cluster in the same namespace is never drawn as if it were ours.
func buildDatabasesDrawing(p *Page) *TopologyView {
	if p.Cluster == nil || p.Cluster.Absent || p.Databases == nil {
		return nil
	}
	d := p.Databases
	if len(d.Databases) == 0 && len(d.Roles) == 0 && len(d.Publications) == 0 && len(d.Subscriptions) == 0 {
		return nil
	}

	view := &TopologyView{
		Title: "Declared database objects",
		Aria:  "The declared databases, roles, publications and subscriptions, and which database each publication and subscription names",
	}

	capacity := 1 + minI(len(d.Databases), secMaxBoxes) + minI(len(d.Roles), secMaxBoxes) +
		minI(len(d.Publications), secMaxBoxes) + minI(len(d.Subscriptions), secMaxBoxes) + 4
	nodes := make([]TopoNode, 0, capacity)
	rowsByID := map[string][]TopoGraphText{}
	add := func(id, kind, state string, rows []TopoGraphText) *TopoNode {
		nodes = append(nodes, TopoNode{
			ID: id, Kind: kind, State: state,
			W: chWBox, H: wireNodeHeight(len(rows)),
		})
		rowsByID[id] = rows
		return &nodes[len(nodes)-1]
	}
	cy := func(n *TopoNode) float64 { return float64(n.Y) + float64(n.H)/2 }

	// Boxes per kind, bounded, remembering each database by the name
	// publications and subscriptions declare against.
	byDatabase := map[string]*TopoNode{}
	var dbBoxes, roleBoxes, pubBoxes, subBoxes []*TopoNode
	type pubWire struct {
		node *TopoNode
		db   string
		sub  bool
	}
	var pubWires []pubWire
	// A database names the role that owns it. Both boxes sit in the
	// same column — databases above, roles below — so the wire is
	// resolved after both frames are placed.
	type ownerWire struct {
		db   *TopoNode
		role string
	}
	var ownerOf []ownerWire

	bounded := func(n int) (int, int) {
		if n > secMaxBoxes {
			return secMaxBoxes - 1, n - (secMaxBoxes - 1)
		}
		return n, 0
	}

	shown, extra := bounded(len(d.Databases))
	for i, db := range d.Databases[:shown] {
		rows := []TopoGraphText{{C: "label", T: db.Name}}
		sub := db.Database
		if db.Owner != "" && db.Owner != unknown {
			sub += " · owner " + db.Owner
		}
		if sub != "" {
			rows = append(rows, TopoGraphText{C: "sub", T: sub})
		}
		line := db.Declared.State
		if db.Encoding != "" && db.Encoding != unknown {
			line += " · " + db.Encoding
		}
		rows = append(rows, TopoGraphText{C: "disk", T: line})
		n := add(fmt.Sprintf("db-%d", i), "endpoint", declaredState(db.Declared), rows)
		dbBoxes = append(dbBoxes, n)
		if db.Database != "" {
			byDatabase[db.Database] = n
		}
		if db.Owner != "" && db.Owner != unknown {
			ownerOf = append(ownerOf, ownerWire{db: n, role: db.Owner})
		}
	}
	if extra > 0 {
		dbBoxes = append(dbBoxes, add("db-more", "endpoint", "", []TopoGraphText{
			{C: "label", T: fmt.Sprintf("+%d more", extra)},
		}))
	}

	// Remembered by the PostgreSQL role name, because that is what a
	// Database's owner field names — not the resource name.
	byRole := map[string]*TopoNode{}
	// The roles a drawn database names as its owner come first, so the
	// bound below cannot elide the far end of a wire this diagram is
	// about to draw. Two of three databases wired and the third not,
	// for no reason a reader can see, reads as the diagram being wrong
	// rather than as the list being long.
	wanted := make(map[string]bool, len(ownerOf))
	for _, w := range ownerOf {
		wanted[w.role] = true
	}
	roles := make([]DatabaseRoleRowView, 0, len(d.Roles))
	for _, role := range d.Roles {
		if wanted[role.Role] {
			roles = append(roles, role)
		}
	}
	for _, role := range d.Roles {
		if !wanted[role.Role] {
			roles = append(roles, role)
		}
	}
	shown, extra = bounded(len(roles))
	for i, role := range roles[:shown] {
		rows := []TopoGraphText{{C: "label", T: role.Name}}
		if role.Role != "" && role.Role != role.Name {
			rows = append(rows, TopoGraphText{C: "sub", T: "role " + role.Role})
		}
		line := role.Declared.State
		if role.Attributes != "" && role.Attributes != "none" {
			line += " · " + role.Attributes
		}
		rows = append(rows, TopoGraphText{C: "disk", T: line})
		n := add(fmt.Sprintf("role-%d", i), "pvc", declaredState(role.Declared), rows)
		roleBoxes = append(roleBoxes, n)
		if role.Role != "" {
			byRole[role.Role] = n
		}
	}
	if extra > 0 {
		roleBoxes = append(roleBoxes, add("role-more", "pvc", "", []TopoGraphText{
			{C: "label", T: fmt.Sprintf("+%d more", extra)},
		}))
	}

	shown, extra = bounded(len(d.Publications))
	for i, pub := range d.Publications[:shown] {
		rows := []TopoGraphText{{C: "label", T: pub.Name}}
		if pub.Database != "" {
			rows = append(rows, TopoGraphText{C: "sub", T: "on " + pub.Database})
		}
		line := pub.Declared.State
		if pub.Target != "" {
			line += " · " + pub.Target
		}
		rows = append(rows, TopoGraphText{C: "disk", T: line})
		n := add(fmt.Sprintf("pub-%d", i), "pvc", declaredState(pub.Declared), rows)
		pubBoxes = append(pubBoxes, n)
		pubWires = append(pubWires, pubWire{node: n, db: pub.Database})
	}
	if extra > 0 {
		pubBoxes = append(pubBoxes, add("pub-more", "pvc", "", []TopoGraphText{
			{C: "label", T: fmt.Sprintf("+%d more", extra)},
		}))
	}

	shown, extra = bounded(len(d.Subscriptions))
	for i, sub := range d.Subscriptions[:shown] {
		rows := []TopoGraphText{{C: "label", T: sub.Name}}
		if sub.Database != "" {
			rows = append(rows, TopoGraphText{C: "sub", T: "into " + sub.Database})
		}
		line := sub.Declared.State
		if sub.Publication != "" {
			line += " · from " + sub.Publication
		}
		rows = append(rows, TopoGraphText{C: "disk", T: line})
		n := add(fmt.Sprintf("sub-%d", i), "pvc", declaredState(sub.Declared), rows)
		subBoxes = append(subBoxes, n)
		pubWires = append(pubWires, pubWire{node: n, db: sub.Database, sub: true})
	}
	if extra > 0 {
		subBoxes = append(subBoxes, add("sub-more", "pvc", "", []TopoGraphText{
			{C: "label", T: fmt.Sprintf("+%d more", extra)},
		}))
	}

	root := add("cluster", "primary", "", clusterRootRows(p))

	// --- Placement: databases left, the cluster between them, roles
	// right.
	//
	// There is no super-frame. One dotted box drawn round everything
	// said only "these are declared into the cluster", which is true of
	// every object on the screen and so distinguished nothing; each
	// group now says it for itself by wiring to the cluster.
	//
	// The publications and subscriptions stay with the databases they
	// name, stacked under them in the left column, so those wires never
	// leave that column and never cross the cluster.

	frameW := chWBox + 2*grpPad
	top := float64(grpMargin)
	content := top

	var frames []TopoFrame
	// stackFrames places a column of frames at x and returns its bottom.
	stackFrames := func(x, y float64, groups []struct {
		label string
		boxes []*TopoNode
	}) float64 {
		for _, g := range groups {
			if len(g.boxes) == 0 {
				continue
			}
			boxY := y + grpLabelBand + grpPad
			for _, b := range g.boxes {
				b.X = int(x) + grpPad
				b.Y = int(boxY)
				boxY += float64(b.H) + chBoxGap
			}
			frames = append(frames, TopoFrame{
				Label: g.label, Kind: "cluster",
				X: int(x), Y: int(y), W: frameW, H: int(boxY - chBoxGap + grpPad - y),
			})
			y = boxY - chBoxGap + grpPad + chFrameGap
		}
		return y - chFrameGap
	}

	// Left margin enough for the publication and subscription wires to
	// run up the outside of their own column without hugging the
	// viewBox edge.
	leftX := float64(grpMargin) + 16
	rootX := leftX + float64(frameW) + grpAlley
	rightX := rootX + chWBox + grpAlley

	leftBottom := stackFrames(leftX, content, []struct {
		label string
		boxes []*TopoNode
	}{{"Databases", dbBoxes}, {"Publications", pubBoxes}, {"Subscriptions", subBoxes}})
	rightBottom := stackFrames(rightX, content, []struct {
		label string
		boxes []*TopoNode
	}{{"Database roles", roleBoxes}})

	// The frame each group wires to the cluster from, remembered by
	// value: frames reallocates as columns are added.
	frameByLabel := map[string]TopoFrame{}
	for _, f := range frames {
		frameByLabel[f.Label] = f
	}

	columnsBottom := leftBottom
	if rightBottom > columnsBottom {
		columnsBottom = rightBottom
	}
	root.X = int(rootX)
	root.Y = int((content+columnsBottom)/2) - root.H/2
	if root.Y < int(content) {
		root.Y = int(content)
	}

	view.Nodes = nodes

	// --- Wires. ---

	var edges []TopoEdge
	var links []TopoGraphLink
	rootCy := cy(root)

	// Each group states for itself that it is declared into the
	// cluster, which is what the removed super-frame used to say once
	// for all of them.
	declares := func(label, id string, fromRight bool) {
		f, ok := frameByLabel[label]
		if !ok {
			return
		}
		frameCy := float64(f.Y) + float64(f.H)/2
		from, to := float64(f.X+f.W), float64(root.X)
		if fromRight {
			from, to = float64(f.X), float64(root.X+root.W)
		}
		edges = append(edges, TopoEdge{Kind: "refs", Path: roundedRoute(corners([]topoPoint{
			{from, frameCy}, {(from + to) / 2, frameCy}, {(from + to) / 2, rootCy}, {to, rootCy},
		}))})
		links = append(links, TopoGraphLink{Source: id, Target: "cluster", Kind: "refs"})
	}
	declares("Databases", "databases", false)
	declares("Database roles", "roles", true)

	// Publications and subscriptions against the database they name,
	// in the gutter left of their own column.
	inBus := leftX - 10
	for _, w := range pubWires {
		db := byDatabase[w.db]
		if db == nil {
			continue
		}
		kind := "owns"
		if w.sub {
			kind = "replicate"
		}
		edges = append(edges, TopoEdge{Kind: kind, Path: roundedRoute(corners([]topoPoint{
			{float64(w.node.X), cy(w.node)}, {inBus, cy(w.node)},
			{inBus, cy(db)}, {float64(db.X), cy(db)},
		}))})
		links = append(links, TopoGraphLink{Source: w.node.ID, Target: db.ID, Kind: kind})
	}

	// A database and the role that owns it sit on opposite sides of the
	// cluster, so these route over it: out of the role's left edge, up
	// the right gutter, across a lane above the cluster box, down the
	// left gutter, into the database's right edge.
	//
	// Leaving by the side rather than the top is what makes it work.
	// Both columns are vertical stacks, so a wire entering a box from
	// above would have to pass through every box stacked over it; and
	// every box in a column shares one centre x, so three wires drawn
	// over the top would have laid down on each other and read as one.
	// The gutters are empty, and each wire takes its own x and its own
	// lane there.
	laneBase := rootCy - float64(root.H)/2 - 14
	for i, w := range ownerOf {
		role := byRole[w.role]
		if role == nil {
			continue // owner declared, no DatabaseRole observed for it
		}
		offset := float64(10 + i*8)
		lane := laneBase - float64(i)*9
		if lane < content+4 {
			lane = content + 4
		}
		edges = append(edges, TopoEdge{Kind: "owner", Path: roundedRoute(corners([]topoPoint{
			{float64(role.X), cy(role)},
			{rootX + chWBox + offset, cy(role)},
			{rootX + chWBox + offset, lane},
			{leftX + float64(frameW) + offset, lane},
			{leftX + float64(frameW) + offset, cy(w.db)},
			{float64(w.db.X + w.db.W), cy(w.db)},
		}))})
		links = append(links, TopoGraphLink{Source: role.ID, Target: w.db.ID, Kind: "owner"})
	}

	view.Edges = edges
	view.Graph.Links = links
	view.Legend = topoLegend(links)
	view.Frames = frames

	right := root.X + root.W
	bottom := root.Y + root.H
	for _, f := range view.Frames {
		right = maxI(right, f.X+f.W)
		bottom = maxI(bottom, f.Y+f.H)
	}
	view.Width = right + grpMargin
	view.Height = bottom + grpMargin

	for i := range view.Nodes {
		wirePlace(&view.Nodes[i], rowsByID[view.Nodes[i].ID])
	}
	view.Graph.Nodes = wireGraphNodes(view.Nodes, rowsByID)
	view.Caption = "Declarations and their reconciliation verdicts, exactly as the operator reports them: a green border marks an applied declaration, a red one a failed one. Only objects naming this cluster appear at all. Each group is wired to the cluster it is declared into, publications and subscriptions to the database they name, and each database to the role it declares as its owner. Nothing here is read from PostgreSQL."
	return view
}
