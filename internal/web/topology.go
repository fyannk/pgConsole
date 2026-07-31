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

// TopologyView is the plain-language wiring diagram that opens the
// Overview. Like the summary (AGENTS.md rule 8) it is derived from the
// already-assembled page and restates nothing the attributed sections do
// not carry: the servers and their roles are observed, and the diagram
// says so. The endpoints and the backup path are the fixed CloudNativePG
// shape, drawn so a non-technical reader can see how their apps reach the
// database and where the data is kept.
//
// The whole diagram is one inline SVG with a computed geometry, so it
// needs no script and no external asset; the width/height set its
// viewBox and it scales to its container. Every node carries a text
// label, never colour alone.
type TopologyView struct {
	// Width and Height are the SVG viewBox extent.
	Width, Height int
	// Nodes are the boxes, in render order.
	Nodes []TopoNode
	// Edges are the connecting flows, drawn under the nodes.
	Edges []TopoEdge
	// Caption states what is observed versus standard wiring.
	Caption string
}

// TopoNode is one box in the diagram.
type TopoNode struct {
	// Kind selects the CSS treatment: apps, endpoint, primary, replica,
	// storage, snapshot.
	Kind string
	// Label is the plain-language name.
	Label string
	// Sub is the muted second line: a role, an endpoint name, a detail.
	Sub string
	// Disk is the per-server storage line, empty for non-server nodes.
	Disk string
	// State is the presentation token for a server's health, empty for
	// infrastructure nodes.
	State string
	// X, Y, W, H place the box in the viewBox.
	X, Y, W, H int
}

// CenterY is the vertical middle of the node, for edge anchoring.
func (n TopoNode) CenterY() int { return n.Y + n.H/2 }

// Right is the x of the node's right edge.
func (n TopoNode) Right() int { return n.X + n.W }

// TopoEdge is one flow between two nodes, already resolved to an SVG path.
type TopoEdge struct {
	// Kind selects the stroke: write, read, replicate, archive.
	Kind string
	// Label is the plain-language flow name, empty for none.
	Label string
	// Path is the precomputed SVG path data.
	Path string
	// LabelX, LabelY place the label at the curve's midpoint.
	LabelX, LabelY int
}

// topology geometry, in viewBox units. The SVG scales to its container.
const (
	topoWidth    = 920
	topoColApps  = 24
	topoColSvc   = 210
	topoColSrv   = 448
	topoColStore = 712
	topoWApps    = 150
	topoWSvc     = 158
	topoWSrv     = 176
	topoWStore   = 184
	topoSrvH     = 60
	topoSrvGap   = 22
	topoMaxSrv   = 6
)

// buildTopology derives the wiring diagram from the assembled page. It
// reads only p, so it can invent nothing: with no observed servers it
// returns nil and the Overview simply omits the diagram.
func buildTopology(p *Page) *TopologyView {
	if p.Cluster == nil || p.Cluster.Absent {
		return nil
	}
	servers := topoServers(p)
	if len(servers) == 0 {
		return nil
	}

	view := &TopologyView{Width: topoWidth}

	// Servers column, stacked and vertically centred; the primary leads.
	stackH := len(servers)*topoSrvH + (len(servers)-1)*topoSrvGap
	height := stackH + 76
	if height < 236 {
		height = 236
	}
	view.Height = height
	mid := height / 2
	srvTop := mid - stackH/2

	// Position the server nodes, primary first.
	serverNodes := make([]TopoNode, 0, len(servers))
	for i, s := range servers {
		s.X, s.W, s.H = topoColSrv, topoWSrv, topoSrvH
		s.Y = srvTop + i*(topoSrvH+topoSrvGap)
		serverNodes = append(serverNodes, s)
	}

	// Infrastructure nodes: apps, the two endpoints, and whatever storage
	// is actually configured.
	head := []TopoNode{
		{Kind: "apps", Label: "Your applications", Sub: "clients that connect",
			X: topoColApps, Y: mid - 30, W: topoWApps, H: 60},
		{Kind: "endpoint", Label: "Write endpoint", Sub: p.ClusterName + "-rw",
			X: topoColSvc, Y: mid - 58, W: topoWSvc, H: 48},
		{Kind: "endpoint", Label: "Read endpoint", Sub: p.ClusterName + "-ro",
			X: topoColSvc, Y: mid + 10, W: topoWSvc, H: 48},
	}
	store, snapshot := topoStorage(p, mid)
	if store != nil {
		head = append(head, *store)
	}
	if snapshot != nil {
		head = append(head, *snapshot)
	}

	// Assemble once, then take every anchor from the final slice so no
	// pointer outlives a reallocation.
	view.Nodes = append(head, serverNodes...)
	var primary, storeN, snapshotN *TopoNode
	var replicas []*TopoNode
	for i := range view.Nodes {
		switch view.Nodes[i].Kind {
		case "primary":
			primary = &view.Nodes[i]
		case "replica":
			replicas = append(replicas, &view.Nodes[i])
		case "storage":
			storeN = &view.Nodes[i]
		case "snapshot":
			snapshotN = &view.Nodes[i]
		}
	}
	apps, rw, ro := view.Nodes[0], view.Nodes[1], view.Nodes[2]

	// Flows. Apps reach the two doors; the doors reach the servers; the
	// primary copies to the replicas and streams backups to storage.
	view.Edges = append(view.Edges,
		edge("write", "writes", apps, rw),
		edge("read", "reads", apps, ro),
	)
	if primary != nil {
		view.Edges = append(view.Edges, edge("write", "", rw, *primary))
	}
	for _, r := range replicas {
		view.Edges = append(view.Edges, edge("read", "", ro, *r))
		if primary != nil {
			view.Edges = append(view.Edges, edge("replicate", "", *primary, *r))
		}
	}
	if storeN != nil && primary != nil {
		view.Edges = append(view.Edges, edge("archive", "continuous backup", *primary, *storeN))
	}
	if snapshotN != nil && primary != nil {
		view.Edges = append(view.Edges, edge("archive", "snapshots", *primary, *snapshotN))
	}

	// The caption states what is observed versus fixed wiring, and only
	// promises a backup path when one is actually drawn.
	if store != nil || snapshot != nil {
		view.Caption = "The servers and their roles are observed; the endpoints and backup path follow the standard CloudNativePG wiring."
	} else {
		view.Caption = "The servers and their roles are observed; the read and write endpoints follow the standard CloudNativePG wiring. No backup destination is configured, so none is shown."
	}
	return view
}

// topoServers builds the server nodes: the primary first, then the
// replicas, from the observed pods. It falls back to the operator's
// instance count when no pod snapshot exists.
func topoServers(p *Page) []TopoNode {
	var nodes []TopoNode
	primaryName := ""
	if p.Cluster != nil {
		primaryName = p.Cluster.CurrentPrimary
	}
	if p.Pods != nil && len(p.Pods.Rows) > 0 {
		var primary *TopoNode
		var replicas []TopoNode
		for _, row := range p.Pods.Rows {
			node := TopoNode{Label: row.Name, State: podState(row), Disk: "keeps data on its own disk"}
			if row.Role == "primary" {
				node.Kind, node.Sub = "primary", "main server — takes writes"
				n := node
				primary = &n
				continue
			}
			node.Kind, node.Sub = "replica", "copy — read-only"
			replicas = append(replicas, node)
		}
		if primary != nil {
			nodes = append(nodes, *primary)
		}
		nodes = append(nodes, replicas...)
	} else {
		// No pod snapshot: draw the primary the operator names, and any
		// remaining desired instances as copies, all state-unknown.
		if primaryName != "" {
			nodes = append(nodes, TopoNode{Kind: "primary", Label: primaryName,
				Sub: "main server — takes writes", State: unknown, Disk: "keeps data on its own disk"})
		}
	}
	if len(nodes) > topoMaxSrv {
		extra := len(nodes) - (topoMaxSrv - 1)
		nodes = nodes[:topoMaxSrv-1]
		nodes = append(nodes, TopoNode{Kind: "replica", Label: fmt.Sprintf("+%d more copies", extra),
			Sub: "read-only", State: unknown})
	}
	return nodes
}

// podState maps a pod row to the diagram's presentation token.
func podState(row PodRowView) string {
	if strings.Contains(row.Phase, "deleting") {
		return "degraded"
	}
	switch row.Ready {
	case "true":
		return "current"
	case "false":
		return "degraded"
	default:
		return unknown
	}
}

// topoStorage builds the storage-lane nodes actually backed by evidence:
// a cloud-backup node when a repository or backup catalog exists, and a
// volume-snapshot node when a Backup used that method. Returns nils when
// nothing is configured, so the lane is simply absent.
func topoStorage(p *Page, mid int) (store, snapshot *TopoNode) {
	cloud := p.Repository != nil || (p.Backups != nil && len(p.Backups.Rows) > 0) ||
		(p.Backups != nil && p.Backups.EvidenceLink != nil)
	snap := false
	if p.Backups != nil {
		for _, b := range p.Backups.Rows {
			if strings.Contains(b.Method, "volumeSnapshot") || strings.Contains(b.SnapshotState, "not collected") {
				snap = true
				break
			}
		}
	}
	switch {
	case cloud && snap:
		store = &TopoNode{Kind: "storage", Label: "Cloud backup", Sub: "object storage (WAL + backups)",
			X: topoColStore, Y: mid - 58, W: topoWStore, H: 48}
		snapshot = &TopoNode{Kind: "snapshot", Label: "Volume snapshots", Sub: "disk-level copies",
			X: topoColStore, Y: mid + 10, W: topoWStore, H: 48}
	case cloud:
		store = &TopoNode{Kind: "storage", Label: "Cloud backup", Sub: "object storage (WAL + backups)",
			X: topoColStore, Y: mid - 26, W: topoWStore, H: 52}
	case snap:
		snapshot = &TopoNode{Kind: "snapshot", Label: "Volume snapshots", Sub: "disk-level copies",
			X: topoColStore, Y: mid - 26, W: topoWStore, H: 52}
	}
	return store, snapshot
}

// edge resolves a flow between two nodes into a smooth horizontal bezier
// and a midpoint for its label. Sources exit the right edge; targets
// enter the left edge.
func edge(kind, label string, from, to TopoNode) TopoEdge {
	sx, sy := from.Right(), from.CenterY()
	tx, ty := to.X, to.CenterY()
	dx := (tx - sx) / 2
	return TopoEdge{
		Kind:   kind,
		Label:  label,
		Path:   fmt.Sprintf("M%d %d C%d %d %d %d %d %d", sx, sy, sx+dx, sy, tx-dx, ty, tx, ty),
		LabelX: (sx + tx) / 2,
		LabelY: (sy+ty)/2 - 6,
	}
}
