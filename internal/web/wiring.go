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
)

// The cluster-overview wiring is the power-user counterpart of the
// Overview diagram: the same observed shape, drawn with the facts a DBA
// reaches for — per-instance placement, the operator's timeline and
// quorum membership — instead of the plain-language roles. Nodes carry a
// variable list of rows (the lines[] schema the enhancement layer also
// reads), so a box holds as many facts as are actually observed and no
// row is ever invented to fill a slot.
//
// Box extents, in viewBox units; there is no applications tier, so the
// endpoints take the left column.
const (
	wireWSvc   = 190
	wireWSrv   = 230
	wireWStore = 265
)

// wireLineStep mirrors the enhancement layer's row spacing: the label
// row leads, the second row sits 17px under it, the rest 14px apart.
func wireLineStep(i int) int {
	switch i {
	case 0:
		return 0
	case 1:
		return 17
	default:
		return 14
	}
}

// wireBlockHeight is the height of a node's text block.
func wireBlockHeight(rows int) int {
	h := 14
	for i := 0; i < rows; i++ {
		h += wireLineStep(i)
	}
	return h
}

// wireNodeHeight mirrors the enhancement layer's default box height for
// a lines[] node, so the served drawing and the re-layout agree.
func wireNodeHeight(rows int) int {
	if rows < 1 {
		rows = 1
	}
	return 32 + 10*rows
}

// wireNode builds one lines[]-style node: height from the row count,
// text placed centred inside the box once the caller sets X and Y.
func wireNode(kind, state string, w int, rows []TopoGraphText) TopoNode {
	return TopoNode{Kind: kind, State: state, W: w, H: wireNodeHeight(len(rows)),
		Lines: make([]TopoText, 0, len(rows)), // placed by wirePlace
		Label: "",                             // lines carry all text
		Sub:   "", Disk: "",
		X: 0, Y: 0, ID: "", Layer: 0,
	}
}

// wirePlace positions a node's rows now that the box has its X and Y.
func wirePlace(n *TopoNode, rows []TopoGraphText) {
	x := n.X + 14
	y := n.Y + (n.H-wireBlockHeight(len(rows)))/2 + 11
	for i, row := range rows {
		y += wireLineStep(i)
		n.Lines = append(n.Lines, TopoText{Class: row.C, Text: row.T, X: x, Y: y})
	}
}

// buildClusterWiring derives the power-user wiring diagram from the
// assembled page. Like buildTopology it reads only p and invents
// nothing: absent facts drop their row rather than rendering a guess.
func buildClusterWiring(ctx context.Context, p *Page) *TopologyView {
	if p.Cluster == nil || p.Cluster.Absent {
		return nil
	}
	serverRows := wireServers(p)
	if len(serverRows) == 0 {
		return nil
	}

	view := &TopologyView{
		Title: "Physical wiring",
		Aria:  "Services, instances with node placement, replication and backup targets",
	}

	// Nodes carry their rows and extent; the layout engine places them,
	// and the rows are positioned inside each box once it has a home.
	nodes := make([]TopoNode, 0, len(serverRows)+4)
	rowsByID := map[string][]TopoGraphText{}
	addNode := func(id string, layer int, n TopoNode, rows []TopoGraphText) *TopoNode {
		n.ID, n.Layer = id, layer
		rowsByID[id] = rows
		nodes = append(nodes, n)
		return &nodes[len(nodes)-1]
	}

	// Endpoints, the left column: the two doors every client uses. The
	// routing statements are the standard CloudNativePG wiring, and the
	// caption says so.
	rwRows := []TopoGraphText{
		{C: "label", T: p.ClusterName + "-rw"},
		{C: "sub", T: "routes to the current primary"},
	}
	roRows := []TopoGraphText{
		{C: "label", T: p.ClusterName + "-ro"},
		{C: "sub", T: "routes to the read-only copies"},
	}
	rw := addNode("rw", 1, wireNode("endpoint", "", wireWSvc, rwRows), rwRows)
	ro := addNode("ro", 1, wireNode("endpoint", "", wireWSvc, roRows), roRows)

	// Servers, primary first, each carrying its observed facts.
	var primary *TopoNode
	var replicas []*TopoNode
	for i, s := range serverRows {
		ptr := addNode(fmt.Sprintf("srv-%d", i), 2, wireNode(s.kind, s.state, wireWSrv, s.rows), s.rows)
		if s.kind == "primary" {
			primary = ptr
		} else {
			replicas = append(replicas, ptr)
		}
	}

	// Storage, evidence-based exactly like the Overview.
	cloud, snap := topoStorageConfigured(p)
	var storeN, snapshotN *TopoNode
	storeRows := []TopoGraphText{
		{C: "label", T: "Cloud backup"},
		{C: "sub", T: "object storage (WAL + backups)"},
	}
	snapRows := []TopoGraphText{
		{C: "label", T: "Volume snapshots"},
		{C: "sub", T: "disk-level copies"},
	}
	if cloud {
		storeN = addNode("store", 3, wireNode("storage", "", wireWStore, storeRows), storeRows)
	}
	if snap {
		snapshotN = addNode("snapshot", 3, wireNode("snapshot", "", wireWStore, snapRows), snapRows)
	}

	view.Nodes = nodes

	// Flows, recorded once and routed by the layout engine.
	link := func(kind string, from, to *TopoNode) {
		view.Graph.Links = append(view.Graph.Links,
			TopoGraphLink{Source: from.ID, Target: to.ID, Kind: kind})
	}
	if primary != nil {
		link("write", rw, primary)
	}
	for _, r := range replicas {
		link("read", ro, r)
		if primary != nil {
			link("replicate", primary, r)
		}
	}
	if primary != nil && storeN != nil {
		link("archive", primary, storeN)
	}
	if primary != nil && snapshotN != nil {
		link("archive", primary, snapshotN)
	}

	// A layout that cannot be trusted is no diagram at all.
	geo, err := layoutDiagram(ctx, view.Nodes, view.Graph.Links)
	if err != nil {
		return nil
	}
	view.Width, view.Height = geo.Width, geo.Height
	view.Edges = geo.Edges
	view.Legend = topoLegend(view.Graph.Links)
	for i := range view.Nodes {
		centre, ok := geo.Centres[view.Nodes[i].ID]
		if !ok {
			return nil
		}
		view.Nodes[i].X = int(centre.x) - view.Nodes[i].W/2
		view.Nodes[i].Y = int(centre.y) - view.Nodes[i].H/2
		wirePlace(&view.Nodes[i], rowsByID[view.Nodes[i].ID])
	}

	view.Caption = "Instances, roles and placement are observed; the timeline and quorum membership are operator-reported; the endpoints and backup path follow the standard CloudNativePG wiring."
	return view
}

// wireServer is one server node before layout: its facts as rows.
type wireServer struct {
	kind  string
	state string
	rows  []TopoGraphText
}

// wireServers builds the server rows from the observed pods, primary
// first. Each row is an observed or operator-reported fact; a fact the
// snapshot does not carry simply has no row.
func wireServers(p *Page) []wireServer {
	if p.Pods == nil || len(p.Pods.Rows) == 0 {
		return nil
	}
	quorum := map[string]bool{}
	quorumConfigured := p.Quorum != nil && p.Quorum.Configured
	if quorumConfigured {
		for _, s := range p.Quorum.Standbys {
			quorum[s] = true
		}
	}

	var primary *wireServer
	var replicas []wireServer
	for _, row := range p.Pods.Rows {
		s := wireServer{state: podState(row)}
		if row.Role == "primary" {
			s.kind = "primary"
			s.rows = append(s.rows, TopoGraphText{C: "label", T: row.Name + " — primary"})
			if p.Cluster.Timeline != unknown {
				s.rows = append(s.rows, TopoGraphText{C: "sub", T: "timeline " + p.Cluster.Timeline})
			}
		} else {
			s.kind = "replica"
			role := row.Role
			if role == unknown {
				role = "role unknown"
			}
			s.rows = append(s.rows, TopoGraphText{C: "label", T: row.Name + " — " + role})
			if quorumConfigured {
				if quorum[row.Name] {
					s.rows = append(s.rows, TopoGraphText{C: "sub", T: "potentially synchronous (quorum)"})
				} else {
					s.rows = append(s.rows, TopoGraphText{C: "sub", T: "not in the reported standby set"})
				}
			}
		}
		s.rows = append(s.rows, TopoGraphText{C: "disk", T: "node " + row.Node})
		if s.kind == "primary" {
			v := s
			primary = &v
			continue
		}
		replicas = append(replicas, s)
	}

	var out []wireServer
	if primary != nil {
		out = append(out, *primary)
	}
	out = append(out, replicas...)
	if len(out) > topoMaxSrv {
		extra := len(out) - (topoMaxSrv - 1)
		out = out[:topoMaxSrv-1]
		out = append(out, wireServer{kind: "replica", state: unknown, rows: []TopoGraphText{
			{C: "label", T: fmt.Sprintf("+%d more copies", extra)},
			{C: "sub", T: "read-only"},
		}})
	}
	return out
}
