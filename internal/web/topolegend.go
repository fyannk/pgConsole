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

// LegendItem keys one line style below a wiring diagram. The wires
// carry no labels of their own — the boxes name themselves — so the
// legend is what says which style means what.
type LegendItem struct {
	// Kind matches the edge's CSS treatment, so a swatch can never
	// drift from the line it names.
	Kind string
	// Label is the plain word for the flow.
	Label string
}

// legendOrder is every style the diagrams can draw, in reading order.
var legendOrder = []LegendItem{
	{Kind: "write", Label: "writes"},
	{Kind: "read", Label: "reads"},
	{Kind: "replicate", Label: "replication"},
	// The two object-store flows are separate everywhere but the box
	// they end in: continuous WAL shipping leaves the primary, periodic
	// base backups leave whichever instance the operator picked. One
	// wire for both would assert a shared origin the resources deny.
	{Kind: "wal", Label: "WAL streaming"},
	{Kind: "archive", Label: "base backup"},
	{Kind: "disk", Label: "volume"},
	// The declared-objects diagram: a database names the role that
	// owns it, and a publication or subscription names the database
	// it lives in. Both are declarations read off the objects, never
	// a reading of PostgreSQL.
	{Kind: "owner", Label: "declared owner"},
	{Kind: "owns", Label: "declared in"},
}

// topoLegend lists the entries for the styles a diagram actually drew.
func topoLegend(links []TopoGraphLink) []LegendItem {
	present := make(map[string]bool, len(links))
	for _, l := range links {
		present[l.Kind] = true
	}
	var items []LegendItem
	for _, entry := range legendOrder {
		if present[entry.Kind] {
			items = append(items, entry)
		}
	}
	return items
}
