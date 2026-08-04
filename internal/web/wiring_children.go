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

// The children drawing: the Cluster resource and every object attached
// to it, drawn as an inventory rather than a data path. Two dotted
// super-frames split the attachment by its proof — objects the Cluster
// owns through a controller owner reference, and objects that reference
// the cluster from outside (poolers, backups and their schedules,
// volume snapshots). Inside each, one frame per kind, one box per
// object, every box carrying only observed facts.
//
// Like the grouped wiring it uses no layout engine: kind frames flow
// into fixed columns, each frame dropping into the shortest column at
// its turn — deterministic for a given page, so it is safe to
// screenshot and cheap to test.
const (
	// chWBox is every box's width; uniform boxes make the columns read
	// as columns.
	chWBox = 230
	// chBoxGap separates boxes inside a kind frame.
	chBoxGap = 10
	// chFrameGap separates kind frames in a column.
	chFrameGap = 20
	// chColsOwned and chColsRefs are the fixed column counts.
	chColsOwned = 3
	chColsRefs  = 1
	// chMaxPerKind bounds each kind frame; more become one "+N more".
	chMaxPerKind = 6
)

// buildClusterChildren derives the inventory from the assembled page.
// It reads only p and invents nothing; without an observed cluster
// there is no drawing.
func buildClusterChildren(p *Page) *TopologyView {
	if p.Cluster == nil || p.Cluster.Absent {
		return nil
	}

	view := &TopologyView{
		Title: "The cluster and everything attached to it",
		Aria:  "The Cluster resource, the objects it owns grouped by kind, and the objects referencing it",
	}

	// --- Content: one kindGroup per kind, in fixed reading order. ---

	type box struct {
		id    string
		kind  string // CSS treatment
		state string
		rows  []TopoGraphText
	}
	type kindGroup struct {
		label string
		boxes []box
	}

	bounded := func(label string, boxes []box) kindGroup {
		if len(boxes) > chMaxPerKind {
			extra := len(boxes) - (chMaxPerKind - 1)
			boxes = boxes[:chMaxPerKind-1]
			boxes = append(boxes, box{
				id: "more-" + strings.ToLower(strings.ReplaceAll(label, " ", "-")), kind: "pvc",
				rows: []TopoGraphText{{C: "label", T: fmt.Sprintf("+%d more", extra)}},
			})
		}
		return kindGroup{label: label, boxes: boxes}
	}

	var owned []kindGroup
	addOwned := func(label string, boxes []box) {
		if len(boxes) > 0 {
			owned = append(owned, bounded(label, boxes))
		}
	}
	var refs []kindGroup
	addRef := func(label string, boxes []box) {
		if len(boxes) > 0 {
			refs = append(refs, bounded(label, boxes))
		}
	}

	// Instances: the same observed rows the wiring drawings use.
	var instances []box
	for i, s := range wireServers(p, false) {
		instances = append(instances, box{
			id: fmt.Sprintf("srv-%d", i), kind: s.kind, state: s.state, rows: s.rows,
		})
	}
	addOwned("Instances", instances)

	// Services, claims and the further children need the observed set.
	if p.Infrastructure != nil {
		var services []box
		for i, svc := range p.Infrastructure.Services {
			rows := []TopoGraphText{{C: "label", T: svc.Name}}
			line := svc.Type
			if svc.Address != "" && svc.Address != unknown {
				line += " " + svc.Address
			}
			if svc.Port != "" {
				line += ":" + svc.Port
			}
			rows = append(rows, TopoGraphText{C: "sub", T: line})
			if sel := distinguishingSelector(svc.Selector); sel != "" {
				rows = append(rows, TopoGraphText{C: "disk", T: "selector: " + sel})
			}
			services = append(services, box{id: fmt.Sprintf("svc-%d", i), kind: "endpoint", rows: rows})
		}
		addOwned("Services", services)

		var claims []box
		for _, v := range p.Infrastructure.Volumes {
			claims = append(claims, box{id: "pvc-" + v.Name, kind: "pvc", rows: claimRows(v)})
		}
		addOwned("Volume claims", claims)

		grouped := map[string][]box{}
		for i, child := range p.Infrastructure.Children {
			grouped[child.Kind] = append(grouped[child.Kind], box{
				id: fmt.Sprintf("child-%d", i), kind: childBoxKind(child.Kind), rows: childRows(child),
			})
		}
		addOwned("Secrets", grouped["Secret"])
		addOwned("Config maps", grouped["ConfigMap"])
		addOwned("Disruption budgets", grouped["PodDisruptionBudget"])
		addOwned("RBAC", append(append(grouped["ServiceAccount"], grouped["Role"]...), grouped["RoleBinding"]...))
		addOwned("Jobs", grouped["Job"])
	}

	// The referencing side: poolers, schedules, backups, snapshots.
	if p.Poolers != nil {
		var poolers []box
		for i, pooler := range p.Poolers.Poolers {
			poolers = append(poolers, box{
				id: fmt.Sprintf("pool-%d", i), kind: "pooler",
				state: poolerState(pooler), rows: poolerRows(pooler),
			})
		}
		addRef("Poolers", poolers)
	}
	if p.Backups != nil {
		var schedules []box
		for i, s := range p.Backups.ScheduledRows {
			schedules = append(schedules, box{
				id: fmt.Sprintf("sched-%d", i), kind: "backup", rows: scheduleRows(s),
			})
		}
		addRef("Backup schedules", schedules)

		var backups []box
		for i, b := range p.Backups.Rows {
			rows := []TopoGraphText{{C: "label", T: b.Name}}
			// The phase alone: its attribution rider belongs to the
			// backups panel, and the frame is operator-reported anyway.
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
			backups = append(backups, box{id: fmt.Sprintf("bak-%d", i), kind: "storage", rows: rows})
		}
		addRef("Backups", backups)
	}
	if p.Infrastructure != nil {
		var snapshots []box
		for i, s := range p.Infrastructure.Snapshots {
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
			snapshots = append(snapshots, box{id: fmt.Sprintf("snap-%d", i), kind: "snapshot", rows: rows})
		}
		addRef("Volume snapshots", snapshots)
	}

	// --- Placement. ---

	// Every box is added to the node slice once, so the capacity is the
	// exact box count plus the cluster's own box.
	capacity := 1
	for _, g := range owned {
		capacity += len(g.boxes)
	}
	for _, g := range refs {
		capacity += len(g.boxes)
	}
	nodes := make([]TopoNode, 0, capacity)
	rowsByID := map[string][]TopoGraphText{}
	add := func(b box) *TopoNode {
		nodes = append(nodes, TopoNode{
			ID: b.id, Kind: b.kind, State: b.state,
			W: chWBox, H: wireNodeHeight(len(b.rows)),
		})
		rowsByID[b.id] = b.rows
		return &nodes[len(nodes)-1]
	}

	// The Cluster's own box: identity and operator-reported standing.
	clusterRows := []TopoGraphText{{C: "label", T: "Cluster/" + p.ClusterName}}
	if p.Cluster.Phase != "" && p.Cluster.Phase != unknown {
		clusterRows = append(clusterRows, TopoGraphText{C: "sub", T: p.Cluster.Phase})
	}
	if p.Cluster.Instances != "" && p.Cluster.Instances != unknown {
		clusterRows = append(clusterRows, TopoGraphText{C: "sub", T: p.Cluster.Instances + " instances"})
	}
	if image := shortImage(p.Cluster.Image); image != "" {
		clusterRows = append(clusterRows, TopoGraphText{C: "disk", T: image})
	}
	cluster := add(box{id: "cluster", kind: "primary", rows: clusterRows})

	// placeGroups flows kind frames into fixed columns — each frame
	// into the currently shortest column — and returns the inner
	// frames plus the region extent. Inner frames stay neutral; the
	// super-frames carry the colour.
	frameW := chWBox + 2*grpPad
	placeGroups := func(groups []kindGroup, cols int, x0, y0 float64) ([]TopoFrame, float64, float64) {
		if len(groups) < cols {
			cols = len(groups)
		}
		colX := make([]float64, cols)
		colY := make([]float64, cols)
		for i := range colX {
			colX[i] = x0 + float64(i*(frameW+chFrameGap))
			colY[i] = y0
		}
		var frames []TopoFrame
		for _, g := range groups {
			col := 0
			for i := 1; i < cols; i++ {
				if colY[i] < colY[col] {
					col = i
				}
			}
			top := colY[col]
			y := top + grpLabelBand + grpPad
			for _, b := range g.boxes {
				n := add(b)
				n.X = int(colX[col]) + grpPad
				n.Y = int(y)
				y += float64(n.H) + chBoxGap
			}
			bottom := y - chBoxGap + grpPad
			frames = append(frames, TopoFrame{
				Label: g.label,
				X:     int(colX[col]), Y: int(top),
				W: frameW, H: int(bottom - top),
			})
			colY[col] = bottom + chFrameGap
		}
		width := float64(cols*(frameW+chFrameGap) - chFrameGap)
		maxY := y0
		for _, y := range colY {
			if y-chFrameGap > maxY {
				maxY = y - chFrameGap
			}
		}
		return frames, width, maxY
	}

	// The Cluster column, then the owned region, then the referencing
	// region, each region wrapped in its own labelled super-frame. A
	// corridor above the frames carries the references wire back to
	// the cluster; without a referencing side there is no corridor.
	superPad := float64(grpPad)
	corridor := 0.0
	if len(refs) > 0 {
		corridor = 26
	}
	clusterX := float64(grpMargin + grpPad)
	ownedX := clusterX + chWBox + grpAlley + superPad
	ownedTop := float64(grpMargin) + corridor
	ownedContent := ownedTop + grpLabelBand + superPad

	ownedFrames, ownedW, ownedBottom := placeGroups(owned, chColsOwned, ownedX, ownedContent)
	ownedSuper := TopoFrame{
		Label: "Owned by the cluster", Kind: "cluster",
		X: int(ownedX - superPad), Y: int(ownedTop),
		W: int(ownedW + 2*superPad), H: int(ownedBottom + superPad - ownedTop),
	}

	refsX := ownedX + ownedW + superPad + grpAlley + superPad
	refFrames, refW, refBottom := placeGroups(refs, chColsRefs, refsX, ownedContent)
	var refSuper *TopoFrame
	if len(refFrames) > 0 {
		refSuper = &TopoFrame{
			Label: "References the cluster", Kind: "backup",
			X: int(refsX - superPad), Y: int(ownedTop),
			W: int(refW + 2*superPad), H: int(refBottom + superPad - ownedTop),
		}
	}

	// The Cluster box faces the owned frame's centre, clamped below
	// the corridor when the frame is short.
	clusterCy := ownedTop + (ownedBottom+superPad-ownedTop)/2
	cluster.X = int(clusterX)
	cluster.Y = int(clusterCy) - cluster.H/2
	if minY := int(ownedTop) + grpLabelBand; cluster.Y < minY {
		cluster.Y = minY
	}

	view.Nodes = nodes

	// --- Wires: the two relations, stated once each. The edges carry
	// their own words, so no legend is needed. ---

	var links []TopoGraphLink
	cy := float64(cluster.Y) + float64(cluster.H)/2
	if len(ownedFrames) > 0 {
		view.Edges = append(view.Edges, TopoEdge{
			Kind: "owns",
			Path: roundedRoute(corners([]topoPoint{
				{float64(cluster.X + cluster.W), cy},
				{float64(ownedSuper.X), cy},
			})),
			Label:  "owns",
			LabelX: (cluster.X + cluster.W + ownedSuper.X) / 2,
			LabelY: int(cy) - 7,
		})
		links = append(links, TopoGraphLink{Source: "cluster", Target: "owned", Kind: "owns"})
	}
	if refSuper != nil {
		// The referencing objects point at the cluster: out of their
		// frame's top, along the corridor, into the cluster's top edge.
		overY := float64(grpMargin) + corridor/2
		cx := float64(cluster.X) + float64(cluster.W)/2
		mid := float64(refSuper.X) + float64(refSuper.W)/2
		view.Edges = append(view.Edges, TopoEdge{
			Kind: "refs",
			Path: roundedRoute(corners([]topoPoint{
				{mid, float64(refSuper.Y)},
				{mid, overY},
				{cx, overY},
				{cx, float64(cluster.Y)},
			})),
			Label:  "references",
			LabelX: int((mid + cx) / 2),
			LabelY: int(overY) - 4,
		})
		links = append(links, TopoGraphLink{Source: "refs", Target: "cluster", Kind: "refs"})
	}
	view.Graph.Links = links

	// Frames render before nodes, outer before inner.
	view.Frames = append(view.Frames, ownedSuper)
	if refSuper != nil {
		view.Frames = append(view.Frames, *refSuper)
	}
	view.Frames = append(view.Frames, ownedFrames...)
	view.Frames = append(view.Frames, refFrames...)

	// The viewBox wraps every frame and the cluster box.
	right := 0
	bottom := cluster.Y + cluster.H
	for _, f := range view.Frames {
		if f.X+f.W > right {
			right = f.X + f.W
		}
		if f.Y+f.H > bottom {
			bottom = f.Y + f.H
		}
	}
	view.Width = right + grpMargin
	view.Height = bottom + grpMargin

	for i := range view.Nodes {
		wirePlace(&view.Nodes[i], rowsByID[view.Nodes[i].ID])
	}
	view.Graph.Nodes = wireGraphNodes(view.Nodes, rowsByID)

	caption := "Every object attached to this cluster, grouped by kind: owned means the controller owner reference names the Cluster; referencing objects name it from outside. Secrets show their name, type and key count only — no key or value is read."
	if p.Infrastructure != nil && len(p.Infrastructure.ChildrenUnobserved) > 0 {
		caption += " Not granted, so not observed: " +
			strings.Join(p.Infrastructure.ChildrenUnobserved, ", ") + "."
	}
	view.Caption = caption
	return view
}

// childBoxKind maps a child kind to the CSS treatment its box takes.
func childBoxKind(kind string) string {
	switch kind {
	case "Secret":
		return "secret"
	case "ConfigMap", "ServiceAccount", "Role", "RoleBinding":
		return "pvc"
	case "PodDisruptionBudget":
		return "endpoint"
	default:
		return "pvc"
	}
}

// childRows states one owned object's box: the name, then the lines
// the viewmodel already formatted from observed facts.
func childRows(child ChildRowView) []TopoGraphText {
	rows := []TopoGraphText{{C: "label", T: child.Name}}
	sub := child.Detail
	if child.Kind == "ServiceAccount" || child.Kind == "Role" || child.Kind == "RoleBinding" {
		// The RBAC frame mixes three kinds, so each box says its own.
		sub = child.Kind
		if child.Detail != "" {
			sub += " — " + child.Detail
		}
	}
	if sub != "" {
		rows = append(rows, TopoGraphText{C: "sub", T: sub})
	}
	line := child.Extra
	if child.Age != "" && child.Age != unknown {
		if line != "" {
			line += " · "
		}
		line += child.Age + " old"
	}
	if line != "" {
		rows = append(rows, TopoGraphText{C: "disk", T: line})
	}
	return rows
}
