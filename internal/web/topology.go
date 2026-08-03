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
	// Title heads the panel and Aria labels the drawing for a reader
	// who never sees it.
	Title, Aria string
	// Width and Height are the SVG viewBox extent.
	Width, Height int
	// Nodes are the boxes, in render order.
	Nodes []TopoNode
	// Edges are the connecting flows, drawn under the nodes.
	Edges []TopoEdge
	// Caption states what is observed versus standard wiring.
	Caption string
	// Legend keys the line styles actually drawn, so the wires carry no
	// labels of their own.
	Legend []LegendItem
	// Graph is the same diagram with the layout removed: which boxes
	// exist, which tier each sits in, and what connects to what.
	//
	// The geometry above stays the served state, so the diagram renders
	// without a single line of script and the enhancement layer is
	// exactly that — an enhancement. When it runs it re-lays the diagram
	// out from this graph, which lets boxes and edge labels push each
	// other apart instead of sitting where a fixed column put them.
	Graph TopoGraph
}

// TopoGraph is the wiring diagram as a graph rather than a drawing.
type TopoGraph struct {
	// TierX overrides the enhancement layer's default tier columns;
	// empty keeps the Overview's four-tier default.
	TierX []int `json:"tierX,omitempty"`
	// Nodes are the boxes, each pinned to a tier on the horizontal axis.
	Nodes []TopoGraphNode `json:"nodes"`
	// Links are the flows between them.
	Links []TopoGraphLink `json:"links"`
}

// TopoGraphText is one row of a lines[]-style node in the graph: the
// class shorthand the enhancement layer maps back to the drawing's text
// treatments, and the text itself.
type TopoGraphText struct {
	C string `json:"c"`
	T string `json:"t"`
}

// TopoGraphNode is one box, without a position.
type TopoGraphNode struct {
	// ID is the link endpoint identifier.
	ID string `json:"id"`
	// Layer is the tier: applications, endpoints, servers, storage.
	Layer int `json:"layer"`
	// Cls is the CSS treatment, matching TopoNode.Kind.
	Cls string `json:"cls"`
	// Label, Sub and Disk are the box's lines of text.
	Label string `json:"label,omitempty"`
	Sub   string `json:"sub,omitempty"`
	Disk  string `json:"disk,omitempty"`
	// Lines replaces the fixed label/sub/disk triple for a node that
	// carries as many rows as its facts need.
	Lines []TopoGraphText `json:"lines,omitempty"`
	// State is the health token, empty for infrastructure nodes.
	State string `json:"state,omitempty"`
	// W and H are the box extent; the layout decides only x and y.
	W int `json:"w,omitempty"`
	H int `json:"h,omitempty"`
}

// TopoGraphLink is one flow.
type TopoGraphLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Label  string `json:"label,omitempty"`
}

// GraphJSON renders the graph for the enhancement layer.
//
// It returns a plain string, and the template puts it in an attribute
// rather than a script block. That keeps html/template's contextual
// escaping in charge: no template.JS conversion, so no place where the
// safety rests on this function having reasoned correctly about which
// characters json.Marshal escapes.
func (v TopologyView) GraphJSON() (string, error) {
	raw, err := json.Marshal(v.Graph)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// TopoNode is one box in the diagram.
type TopoNode struct {
	// ID names the box for the graph's links. It never reaches the SVG.
	ID string
	// Layer is the tier the box sits in, for the re-layout.
	Layer int
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
	// Lines replaces the fixed label/sub/disk triple in the served
	// drawing, already placed inside the box; empty keeps the triple.
	Lines []TopoText
	// X, Y, W, H place the box in the viewBox.
	X, Y, W, H int
}

// TopoText is one placed row of a lines[]-style node.
type TopoText struct {
	// Class selects the text treatment: label, sub, or disk.
	Class string
	// Text is the row itself.
	Text string
	// X and Y place the row in the viewBox.
	X, Y int
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

// Box extents, in viewBox units. Placement is the layout engine's.
const (
	topoWApps  = 150
	topoWSvc   = 158
	topoWSrv   = 176
	topoWStore = 184
	topoSrvH   = 60
	topoMaxSrv = 6
)

// buildTopology derives the wiring diagram from the assembled page. It
// reads only p, so it can invent nothing: with no observed servers it
// returns nil and the Overview simply omits the diagram.
func buildTopology(ctx context.Context, p *Page) *TopologyView {
	if p.Cluster == nil || p.Cluster.Absent {
		return nil
	}
	servers := topoServers(p)
	if len(servers) == 0 {
		return nil
	}

	view := &TopologyView{
		Title: "How your database is wired",
		Aria:  "How applications reach the database and where the data is stored",
	}

	// Nodes carry only their extent and tier; the layout engine places
	// them. Order within a tier is ours — the primary leads.
	serverNodes := make([]TopoNode, 0, len(servers))
	for i, s := range servers {
		s.W, s.H = topoWSrv, topoSrvH
		s.ID, s.Layer = fmt.Sprintf("srv-%d", i), 2
		serverNodes = append(serverNodes, s)
	}

	// Infrastructure nodes: apps, the two endpoints, and whatever storage
	// is actually configured.
	head := []TopoNode{
		{ID: "apps", Layer: 0, Kind: "apps", Label: "Your applications", Sub: "clients that connect",
			W: topoWApps, H: 60},
		{ID: "rw", Layer: 1, Kind: "endpoint", Label: "Write endpoint", Sub: p.ClusterName + "-rw",
			W: topoWSvc, H: 48},
		{ID: "ro", Layer: 1, Kind: "endpoint", Label: "Read endpoint", Sub: p.ClusterName + "-ro",
			W: topoWSvc, H: 48},
	}
	store, snapshot := topoStorage(p)
	if store != nil {
		store.ID, store.Layer = "store", 3
		head = append(head, *store)
	}
	if snapshot != nil {
		snapshot.ID, snapshot.Layer = "snapshot", 3
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
	link := func(kind string, from, to TopoNode) {
		view.Graph.Links = append(view.Graph.Links,
			TopoGraphLink{Source: from.ID, Target: to.ID, Kind: kind})
	}

	link("write", apps, rw)
	link("read", apps, ro)
	if primary != nil {
		link("write", rw, *primary)
	}
	for _, r := range replicas {
		link("read", ro, *r)
		if primary != nil {
			link("replicate", *primary, *r)
		}
	}
	if storeN != nil && primary != nil {
		link("archive", *primary, *storeN)
	}
	if snapshotN != nil && primary != nil {
		link("archive", *primary, *snapshotN)
	}

	// A layout that cannot be trusted is no diagram at all: the page
	// omits it rather than drawing boxes in the wrong places.
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
func topoStorage(p *Page) (store, snapshot *TopoNode) {
	cloud, snap := topoStorageConfigured(p)
	switch {
	case cloud && snap:
		store = &TopoNode{Kind: "storage", Label: "Cloud backup", Sub: "object storage (WAL + backups)",
			W: topoWStore, H: 48}
		snapshot = &TopoNode{Kind: "snapshot", Label: "Volume snapshots", Sub: "disk-level copies",
			W: topoWStore, H: 48}
	case cloud:
		store = &TopoNode{Kind: "storage", Label: "Cloud backup", Sub: "object storage (WAL + backups)",
			W: topoWStore, H: 52}
	case snap:
		snapshot = &TopoNode{Kind: "snapshot", Label: "Volume snapshots", Sub: "disk-level copies",
			W: topoWStore, H: 52}
	}
	return store, snapshot
}

// topoStorageConfigured reports which storage lanes the page's evidence
// actually supports: a cloud lane when a repository or backup catalog
// exists, a snapshot lane when a Backup used that method.
func topoStorageConfigured(p *Page) (cloud, snap bool) {
	cloud = p.Repository != nil || (p.Backups != nil && len(p.Backups.Rows) > 0) ||
		(p.Backups != nil && p.Backups.EvidenceLink != nil)
	if p.Backups != nil {
		for _, b := range p.Backups.Rows {
			if strings.Contains(b.Method, "volumeSnapshot") || strings.Contains(b.SnapshotState, "not collected") {
				snap = true
				break
			}
		}
	}
	return cloud, snap
}
