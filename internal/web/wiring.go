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
	"strings"
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
	wireWSvc   = 210
	wireWSrv   = 230
	wireWStore = 265
	wireWPool  = 210
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
	serverRows := wireServers(p, true)
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

	// Endpoints, the left column: the services clients actually dial,
	// as observed. Their addresses and selectors are read from the
	// Service objects, not built from the cluster's name.
	rwRows := endpointRows(p, "read-write", p.ClusterName+"-rw", "routes to the current primary")
	roRows := endpointRows(p, "read-only", p.ClusterName+"-ro", "routes to the read-only copies")
	rw := addNode("rw", 1, wireNode("endpoint", "", wireWSvc, rwRows), rwRows)
	ro := addNode("ro", 1, wireNode("endpoint", "", wireWSvc, roRows), roRows)

	// Connection poolers sit in front of the services: a client that
	// uses one never dials the cluster directly, so a wiring diagram
	// without them describes a path nobody takes.
	type pooled struct {
		node *TopoNode
		to   *TopoNode
	}
	var poolers []pooled
	if p.Poolers != nil {
		for i, pooler := range p.Poolers.Poolers {
			rows := poolerRows(pooler)
			node := addNode(fmt.Sprintf("pool-%d", i), 0,
				wireNode("pooler", poolerState(pooler), wireWPool, rows), rows)
			// A read-only pooler fronts the read service; every other
			// type fronts the write one. Matched against the operator's
			// token, never against the prose spelling of it.
			target := rw
			if pooler.TypeToken == "ro" {
				target = ro
			}
			poolers = append(poolers, pooled{node: node, to: target})
		}
	}

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
	storeRows := objectStoreRows(p)
	snapRows := snapshotRows(p)
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
	for _, pooler := range poolers {
		kind := "write"
		if pooler.to == ro {
			kind = "read"
		}
		link(kind, pooler.node, pooler.to)
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

	// The same diagram as a graph, for the Cytoscape panel: the boxes and
	// what connects them, without the geometry settled above. It carries
	// no fact the SVG does not already show — a reader who never runs a
	// script has lost nothing.
	view.Graph.Nodes = wireGraphNodes(view.Nodes, rowsByID)

	view.Caption = "Instances, roles, placement, claims, services and snapshots are observed; the timeline and quorum membership are operator-reported; the object store is read from its own resource."
	return view
}

// wireGraphNodes restates the placed boxes as a positionless graph.
func wireGraphNodes(nodes []TopoNode, rows map[string][]TopoGraphText) []TopoGraphNode {
	out := make([]TopoGraphNode, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, TopoGraphNode{
			ID: n.ID, Layer: n.Layer, Cls: n.Kind, State: n.State,
			Lines: rows[n.ID], W: n.W, H: n.H,
		})
	}
	return out
}

// wireServer is one server node before layout: its facts as rows.
type wireServer struct {
	name  string
	kind  string
	state string
	rows  []TopoGraphText
}

// wireServers builds the server rows from the observed pods, primary
// first. Each row is an observed or operator-reported fact; a fact the
// snapshot does not carry simply has no row. withVolumes keeps the
// claim line inside the box; the grouped drawing drops it because its
// PVC boxes state the same facts with more room.
func wireServers(p *Page, withVolumes bool) []wireServer {
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
		s := wireServer{name: row.Name, state: podState(row)}
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
		s.rows = append(s.rows, TopoGraphText{C: "sub", T: instanceCondition(row)})
		s.rows = append(s.rows, TopoGraphText{C: "disk", T: "node " + row.Node})
		if withVolumes {
			if disk := volumeLine(p, row.Name); disk != "" {
				s.rows = append(s.rows, TopoGraphText{C: "disk", T: disk})
			}
		}
		if image := shortImage(row.Image); image != "" {
			s.rows = append(s.rows, TopoGraphText{C: "disk", T: image})
		}
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

// endpointRows describes one service box. The observed Service supplies
// the address, port and selector; when none was observed the box keeps
// the standard CloudNativePG name and says the routing is the standard
// wiring rather than an observation.
func endpointRows(p *Page, role, fallbackName, standardRouting string) []TopoGraphText {
	svc := findService(p, role)
	if svc == nil {
		return []TopoGraphText{
			{C: "label", T: fallbackName},
			{C: "sub", T: standardRouting},
			{C: "disk", T: "service not observed"},
		}
	}
	rows := []TopoGraphText{{C: "label", T: svc.Name}}
	switch {
	case svc.Headless:
		rows = append(rows, TopoGraphText{C: "sub", T: "headless" + portSuffix(svc)})
	case svc.ClusterIP != "":
		rows = append(rows, TopoGraphText{C: "sub", T: svc.Type + " " + svc.ClusterIP + portSuffix(svc)})
	default:
		rows = append(rows, TopoGraphText{C: "sub", T: svc.Type})
	}
	// The diagram shows the term that distinguishes this service; the
	// cluster term is the same for every box on the drawing, and the
	// services table below carries the selector in full.
	if distinguishing := distinguishingSelector(svc.Selector); distinguishing != "" {
		rows = append(rows, TopoGraphText{C: "disk", T: "selector: " + distinguishing})
	}
	return rows
}

// distinguishingSelector drops the cluster term every service shares
// and joins what is left, so the box says what makes this service
// different rather than repeating the diagram's subject.
func distinguishingSelector(selector []string) string {
	var kept []string
	for _, term := range selector {
		if strings.HasPrefix(term, "cnpg.io/cluster=") {
			continue
		}
		kept = append(kept, strings.TrimPrefix(term, "cnpg.io/"))
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, ", ")
}

// portSuffix renders ":port" when a port was reported.
func portSuffix(svc *ServiceRowView) string {
	if svc.Port == "" {
		return ""
	}
	return ":" + svc.Port
}

// findService locates one observed service by its role.
func findService(p *Page, role string) *ServiceRowView {
	if p.Infrastructure == nil {
		return nil
	}
	for i := range p.Infrastructure.Services {
		if p.Infrastructure.Services[i].Role == role {
			return &p.Infrastructure.Services[i]
		}
	}
	return nil
}

// volumeLine describes an instance's claims in one line, so a box says
// what disk the instance actually holds.
func volumeLine(p *Page, instance string) string {
	if p.Infrastructure == nil {
		return ""
	}
	var parts []string
	for _, v := range p.Infrastructure.Volumes {
		if v.Instance != instance {
			continue
		}
		part := v.Capacity
		if part == "" {
			part = unknown
		}
		if v.Purpose != "" {
			part = v.Purpose + " " + part
		}
		if v.StorageClass != "" {
			part += " · " + v.StorageClass
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " | ")
}

// objectStoreRows describes the cloud backup box from the ObjectStore
// the console actually read.
func objectStoreRows(p *Page) []TopoGraphText {
	rows := []TopoGraphText{{C: "label", T: "Cloud backup"}}
	store := p.ObjectStoreDetail
	if store == nil || store.Destination == "" {
		rows = append(rows, TopoGraphText{C: "sub", T: "object storage (WAL + backups)"})
		if store != nil && store.Name != "" {
			rows = append(rows, TopoGraphText{C: "disk", T: "ObjectStore/" + store.Name})
		}
		return rows
	}
	rows = append(rows, TopoGraphText{C: "disk", T: store.Destination})
	if store.Endpoint != "" {
		rows = append(rows, TopoGraphText{C: "sub", T: "endpoint " + store.Endpoint})
	}
	if store.Retention != "" {
		rows = append(rows, TopoGraphText{C: "sub", T: "retention " + store.Retention})
	}
	return rows
}

// snapshotRows describes the volume-snapshot box from what was observed.
func snapshotRows(p *Page) []TopoGraphText {
	rows := []TopoGraphText{{C: "label", T: "Volume snapshots"}}
	if p.Infrastructure == nil || !p.Infrastructure.SnapshotsObservable {
		rows = append(rows, TopoGraphText{C: "sub", T: "disk-level copies"})
		return rows
	}
	count := len(p.Infrastructure.Snapshots)
	rows = append(rows, TopoGraphText{C: "sub", T: fmt.Sprintf("%d observed", count)})
	if count > 0 {
		newest := p.Infrastructure.Snapshots[0]
		if newest.RestoreSize != "" {
			rows = append(rows, TopoGraphText{C: "disk", T: "newest " + newest.RestoreSize})
		}
	}
	return rows
}

// instanceCondition states the pod's phase and readiness in one line,
// with the restart count only when there is one to report — a zero
// restart count is noise, a non-zero one is the first thing a DBA
// looks for.
func instanceCondition(row PodRowView) string {
	line := row.Phase
	switch row.Ready {
	case "true":
		line += " · ready"
	case "false":
		line += " · not ready"
	default:
		line += " · readiness " + row.Ready
	}
	if row.Restarts != "" && row.Restarts != "0" && row.Restarts != unknown {
		line += " · " + row.Restarts + " restart"
		if row.Restarts != "1" {
			line += "s"
		}
	}
	return line
}

// shortImage keeps the image's final element, which is the repository
// and tag a reader recognises without the registry path pushing every
// other fact out of the box.
func shortImage(image string) string {
	if image == "" || image == unknown {
		return ""
	}
	if cut := strings.LastIndex(image, "/"); cut >= 0 {
		return image[cut+1:]
	}
	return image
}

// poolerRows describes one connection pooler. The type is the operator's
// own token, unglossed: this drawing is read by people who know what "rw"
// fronts, and the box has room for facts, not for a definition.
func poolerRows(pooler PoolerRowView) []TopoGraphText {
	rows := []TopoGraphText{{C: "label", T: pooler.Name}}
	routing := "pgbouncer"
	if pooler.TypeToken != "" {
		routing += " — " + pooler.TypeToken
	}
	if pooler.PoolMode != "" && pooler.PoolMode != unknown {
		routing += " · " + pooler.PoolMode
	}
	rows = append(rows, TopoGraphText{C: "sub", T: routing})
	if pooler.Instances != "" && pooler.Instances != unknown {
		rows = append(rows, TopoGraphText{C: "disk", T: pooler.Instances + " instances"})
	}
	return rows
}

// poolerState reads the pooler's phase through the same conservative
// classifier every other screen uses: a phase the console does not
// recognise is unknown, never promoted to healthy and never demoted to
// a fault the operator did not report.
func poolerState(pooler PoolerRowView) string {
	return stateToken(pooler.Phase)
}
