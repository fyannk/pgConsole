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
	"encoding/json"
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
	// Frames are the dotted category boxes of the grouped drawing,
	// drawn under everything else; empty for the ungrouped diagrams.
	Frames []TopoFrame
	// Dots mark the tee of a trunk that splits: the point where one
	// flow leaves its bus for one destination.
	Dots []TopoDot
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

// TopoFrame is one dotted category box: a labelled region the reader
// can take in at a glance before reading the boxes inside it.
type TopoFrame struct {
	// Label names the category: Poolers, Cluster, Backups, storage.
	Label string
	// Note is an optional plain detail after the label — the object
	// store frame carries its endpoint here, where it describes the
	// whole region rather than one box.
	Note string
	// Kind selects the frame's domain colour: pool, cluster, backup,
	// store. Empty keeps the neutral frame style.
	Kind string
	// X, Y, W, H place the frame in the viewBox.
	X, Y, W, H int
}

// TopoDot is one tee: the point where a trunk splits toward one of its
// destinations. Its kind matches the flow it belongs to, so the dot is
// drawn in the same colour as its wire.
type TopoDot struct {
	Kind string
	X, Y int
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

// topoMaxSrv bounds the drawn servers; more become one "+N more" box.
const topoMaxSrv = 6

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
