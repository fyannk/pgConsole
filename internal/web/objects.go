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

// The Objects screen is the inventory question the other screens answer
// only in passing: what does this console actually see, and how old is
// each answer. It groups by the custom resource the objects belong to —
// the Cluster, its Backup and ScheduledBackup records, its Poolers, its
// declarative Database objects — because that is the boundary an
// operator reasons about when something is missing.
//
// Freshness is stated per kind rather than per screen, and that is not
// tidiness: the kinds come from separate watches with separate
// generations, so one number over the whole page would be a claim no
// single source supports. A Pooler list observed two seconds ago and a
// Secret list observed two minutes ago are different evidence, and the
// screen says so.
//
// Nothing here re-reads Kubernetes. Every row restates a view another
// section already built, so the inventory cannot disagree with the
// screen it summarises.

// ObjectsView is the whole inventory in render order.
type ObjectsView struct {
	// Groups are the parent resources, in reading order.
	Groups []ObjectGroupView
}

// ObjectGroupView is one parent custom resource and everything observed
// under it.
type ObjectGroupView struct {
	// Parent is the resource the kinds below belong to.
	Parent string
	// Detail says what the grouping means, so the boundary is stated
	// rather than inferred from the heading.
	Detail string
	// Kinds are the observed object kinds, each with its own freshness.
	Kinds []ObjectKindView
}

// ObjectKindView is one kind of object, carrying the freshness of the
// watch that reported it.
type ObjectKindView struct {
	// Kind names the objects listed.
	Kind string
	// APIKind is the Kubernetes kind behind the display name, used to
	// resolve a row against the retained revisions. Empty for a kind
	// the journal never sees, which is most of the supporting objects —
	// the journal deliberately does not record them, so no amount of
	// looking would find one.
	APIKind string
	// Origin attributes the claim.
	Origin Origin
	// Meta is this kind's own freshness, from its own source.
	Meta SectionMeta
	// Observed reports that the source answered. False means the
	// console has no word on this kind — which is not the same claim as
	// an observed empty list, and the screen must not merge them.
	Observed bool
	// Note explains an unobserved or empty kind in the source's terms.
	Note string
	// Truncated reports that the source hit a safety ceiling, so the
	// rows below are a prefix and not the whole set.
	Truncated bool
	// Rows are the objects themselves.
	Rows []ObjectRowView
}

// ObjectRowView is one observed object.
type ObjectRowView struct {
	// Name is the resource name.
	Name string
	// State is the presentation token for the row's condition, empty
	// when the kind reports no condition worth colouring.
	State string
	// Detail is the one line that distinguishes this object from its
	// neighbours of the same kind.
	Detail string
	// RawSeq is the retained revision holding this object's scrubbed
	// definition, zero when the journal has none or the reader is below
	// the gate. It is the only definition the console holds: captured
	// from the watch it already runs, scrubbed at that boundary, never
	// re-read from the API server on a viewer's behalf.
	RawSeq uint64
}

// buildObjectsView assembles the inventory from the views the page has
// already built. It reads no snapshot directly, so a kind that a
// section renders as unobserved is unobserved here too.
func buildObjectsView(p *Page) *ObjectsView {
	view := &ObjectsView{}
	for _, g := range []ObjectGroupView{
		objectsClusterGroup(p),
		objectsBackupGroup(p),
		objectsPoolerGroup(p),
		objectsDatabaseGroup(p),
	} {
		if len(g.Kinds) > 0 {
			view.Groups = append(view.Groups, g)
		}
	}
	return view
}

// objectsClusterGroup lists what the Cluster owns: the instances it
// runs, the endpoints and storage it declares, and the supporting
// objects the operator creates alongside it.
func objectsClusterGroup(p *Page) ObjectGroupView {
	g := ObjectGroupView{
		Parent: "Cluster",
		Detail: "The objects the Cluster owns through a controller owner reference, plus the instances it runs.",
	}

	// The Cluster itself leads, and it is what carries the cluster
	// watch's own freshness. That freshness used to sit in the topbar on
	// every screen; here it stands beside the sections it must never be
	// confused with, which is the point — a stale Backup catalog says
	// nothing about the cluster snapshot, and this screen is where the
	// two are read side by side.
	if p.SnapshotState != "" {
		g.Kinds = append(g.Kinds, ObjectKindView{
			Kind: "Cluster", APIKind: "Cluster", Origin: OriginOperator, Observed: true,
			Meta: SectionMeta{State: p.SnapshotState, Age: p.SnapshotAge, Generation: p.Generation},
			Rows: []ObjectRowView{{Name: p.ClusterName, Detail: objectsClusterDetail(p)}},
		})
	}

	if p.Pods != nil {
		k := ObjectKindView{Kind: "Instance pods", APIKind: "Pod", Origin: p.Pods.Origin, Meta: p.Pods.Meta,
			Observed: true, Truncated: p.Pods.Truncated}
		for _, pod := range p.Pods.Rows {
			k.Rows = append(k.Rows, ObjectRowView{
				Name: pod.Name, State: podState(pod),
				Detail: strings.TrimSpace(pod.Role + " · " + pod.Phase),
			})
		}
		k.Note = objectsEmptyNote(len(k.Rows), "the operator reports no instance pods")
		g.Kinds = append(g.Kinds, k)
	}

	if infra := p.Infrastructure; infra != nil {
		services := ObjectKindView{Kind: "Services", APIKind: "Service", Origin: infra.Origin, Meta: infra.Meta,
			Observed: true, Truncated: infra.Truncated}
		for _, s := range infra.Services {
			detail := s.Type
			if s.Headless {
				detail = "headless"
			} else if s.ClusterIP != "" {
				detail = s.Type + " " + s.ClusterIP
			}
			if s.Port != "" {
				detail += ":" + s.Port
			}
			services.Rows = append(services.Rows, ObjectRowView{
				Name: s.Name, Detail: strings.TrimSpace(detail + " · " + s.Role),
			})
		}
		services.Note = objectsEmptyNote(len(services.Rows), "no services were observed for this cluster")
		g.Kinds = append(g.Kinds, services)

		claims := ObjectKindView{Kind: "Volume claims", APIKind: "PersistentVolumeClaim", Origin: infra.Origin, Meta: infra.Meta,
			Observed: true, Truncated: infra.Truncated}
		for _, v := range infra.Volumes {
			detail := v.Purpose
			if v.Capacity != "" {
				detail = strings.TrimSpace(detail + " " + v.Capacity)
			}
			if v.StorageClass != "" {
				detail += " · " + v.StorageClass
			}
			claims.Rows = append(claims.Rows, ObjectRowView{
				Name: v.Name, State: v.PhaseState,
				Detail: strings.TrimSpace(detail + " · held by " + v.Instance),
			})
		}
		claims.Note = objectsEmptyNote(len(claims.Rows), "no persistent volume claims were observed")
		g.Kinds = append(g.Kinds, claims)

		// Volume snapshots degrade on their own: the CRD may not be
		// installed at all, which is a different claim from none taken.
		snaps := ObjectKindView{Kind: "Volume snapshots", APIKind: "VolumeSnapshot", Origin: infra.Origin, Meta: infra.Meta,
			Observed: infra.SnapshotsObservable}
		if infra.SnapshotsObservable {
			for _, s := range infra.Snapshots {
				snaps.Rows = append(snaps.Rows, ObjectRowView{
					Name: s.Name, Detail: strings.TrimSpace("of " + s.SourceClaim + " · " + s.RestoreSize),
				})
			}
			snaps.Note = objectsEmptyNote(len(snaps.Rows), "no volume snapshots were observed")
		} else {
			snaps.Note = "the VolumeSnapshot resource is not observable in this cluster, so the console has no word either way"
		}
		g.Kinds = append(g.Kinds, snaps)

		// The supporting objects arrive as one list keyed by kind; each
		// kind the adapter could not read is named rather than shown
		// empty, because permission denied is not absence.
		byKind := map[string][]ObjectRowView{}
		var order []string
		for _, c := range infra.Children {
			if _, seen := byKind[c.Kind]; !seen {
				order = append(order, c.Kind)
			}
			byKind[c.Kind] = append(byKind[c.Kind], ObjectRowView{
				Name: c.Name, Detail: strings.TrimSpace(strings.Trim(c.Detail+" · "+c.Extra, " ·")),
			})
		}
		unobserved := map[string]bool{}
		for _, kind := range infra.ChildrenUnobserved {
			unobserved[kind] = true
			if _, seen := byKind[kind]; !seen {
				order = append(order, kind)
			}
		}
		for _, kind := range order {
			k := ObjectKindView{Kind: kind, Origin: infra.Origin, Meta: infra.Meta,
				Observed: !unobserved[kind], Rows: byKind[kind]}
			if unobserved[kind] {
				k.Note = "this kind could not be read — absent CRD or no permission, not proof that none exist"
			} else {
				k.Note = objectsEmptyNote(len(k.Rows), "none observed for this cluster")
			}
			g.Kinds = append(g.Kinds, k)
		}
	}

	if cat := p.ImageCatalog; cat != nil && cat.Referenced {
		k := ObjectKindView{Kind: cat.Kind, Origin: cat.Origin, Meta: cat.Meta, Observed: cat.Found}
		if cat.Found {
			k.Rows = append(k.Rows, ObjectRowView{
				Name: cat.Name, Detail: strings.TrimSpace("major " + cat.Major),
			})
		} else if cat.Unobservable != "" {
			k.Note = cat.Unobservable
		} else {
			k.Note = "the cluster names this catalog, but it was not found"
		}
		g.Kinds = append(g.Kinds, k)
	}
	return g
}

// objectsBackupGroup lists the operator's own backup records and the
// repository they name.
func objectsBackupGroup(p *Page) ObjectGroupView {
	g := ObjectGroupView{
		Parent: "Backup / ScheduledBackup",
		Detail: "The operator's backup records for this cluster, and the object store they name. These are claims about what ran, not proof of what the repository holds.",
	}
	if p.Backups == nil {
		return g
	}
	b := p.Backups

	schedules := ObjectKindView{Kind: "ScheduledBackups", APIKind: "ScheduledBackup", Origin: b.Origin, Meta: b.Meta,
		Observed: true, Truncated: b.SchedulesTruncated}
	for _, s := range b.ScheduledRows {
		schedules.Rows = append(schedules.Rows, ObjectRowView{
			Name: s.Name, Detail: strings.TrimSpace(s.Schedule + " · " + s.Method + " · next " + s.NextSchedule.Text),
		})
	}
	schedules.Note = objectsEmptyNote(len(schedules.Rows), "no backup schedule references this cluster")
	g.Kinds = append(g.Kinds, schedules)

	backups := ObjectKindView{Kind: "Backups", APIKind: "Backup", Origin: b.Origin, Meta: b.Meta,
		Observed: true, Truncated: b.BackupsTruncated}
	for _, r := range b.Rows {
		// The catalog's phase carries "— operator-reported claim", which
		// the card already says twice: in the group's own words and in
		// the footer's attribution. Here it only pushes the useful half
		// of the line out of view.
		phase, _, _ := strings.Cut(r.Phase, " — ")
		detail := r.Method + " · " + phase
		if r.SourceInstance != "" {
			detail += " · from " + r.SourceInstance
		}
		backups.Rows = append(backups.Rows, ObjectRowView{Name: r.Name, Detail: detail})
	}
	backups.Note = objectsEmptyNote(len(backups.Rows), "no Backup record references this cluster")
	g.Kinds = append(g.Kinds, backups)

	if store := p.ObjectStoreDetail; store != nil {
		k := ObjectKindView{Kind: "ObjectStore", Origin: store.Origin, Meta: b.Meta,
			Observed: store.Observed}
		if store.Observed {
			detail := store.Destination
			if store.Retention != "" {
				detail += " · retention " + store.Retention
			}
			k.Rows = append(k.Rows, ObjectRowView{Name: store.Name, Detail: detail})
		} else {
			k.Note = "the cluster names this object store, but its metadata was not read"
		}
		g.Kinds = append(g.Kinds, k)
	}
	return g
}

// objectsPoolerGroup lists the Poolers referencing the cluster and the
// pods they run, which come from a second watch of their own.
func objectsPoolerGroup(p *Page) ObjectGroupView {
	g := ObjectGroupView{
		Parent: "Pooler",
		Detail: "The Poolers that reference this cluster, and the PgBouncer pods they run. The pods come from a separate watch, so their freshness stands on its own.",
	}
	if p.Poolers != nil {
		k := ObjectKindView{Kind: "Poolers", APIKind: "Pooler", Origin: p.Poolers.Origin, Meta: p.Poolers.Meta,
			Observed: true, Truncated: p.Poolers.Truncated}
		for _, pl := range p.Poolers.Poolers {
			k.Rows = append(k.Rows, ObjectRowView{
				Name: pl.Name, State: pl.TypeToken,
				Detail: strings.TrimSpace(pl.Type + " · " + pl.PoolMode + " · " + pl.Instances),
			})
		}
		k.Note = objectsEmptyNote(len(k.Rows), "no Pooler references this cluster")
		g.Kinds = append(g.Kinds, k)
	}
	if p.PoolerPods != nil {
		k := ObjectKindView{Kind: "Pooler pods", APIKind: "Pod", Origin: p.PoolerPods.Origin, Meta: p.PoolerPods.Meta,
			Observed: true, Truncated: p.PoolerPods.Truncated}
		for _, pod := range p.PoolerPods.Rows {
			k.Rows = append(k.Rows, ObjectRowView{
				Name: pod.Name, State: podState(pod),
				Detail: strings.TrimSpace(pod.Phase + " · " + pod.Node),
			})
		}
		k.Note = objectsEmptyNote(len(k.Rows), "no pooler pods were observed")
		g.Kinds = append(g.Kinds, k)
	}
	return g
}

// objectsDatabaseGroup lists the declarative database objects. They all
// share one watch, so they share one freshness — stated on each kind
// anyway, so no kind reads as fresher than the evidence behind it.
func objectsDatabaseGroup(p *Page) ObjectGroupView {
	g := ObjectGroupView{
		Parent: "Database",
		Detail: "The declarative database objects targeting this cluster. All four kinds come from one watch and share its freshness.",
	}
	d := p.Databases
	if d == nil {
		return g
	}
	add := func(kind, apiKind string, rows []ObjectRowView, empty string) {
		k := ObjectKindView{Kind: kind, APIKind: apiKind, Origin: d.Origin, Meta: d.Meta,
			Observed: true, Truncated: d.Truncated, Rows: rows}
		k.Note = objectsEmptyNote(len(rows), empty)
		g.Kinds = append(g.Kinds, k)
	}

	var databases []ObjectRowView
	for _, r := range d.Databases {
		databases = append(databases, ObjectRowView{
			Name: r.Name, Detail: strings.TrimSpace(r.Database + " · owner " + r.Owner + " · " + r.Ensure),
		})
	}
	add("Databases", "Database", databases, "no Database object targets this cluster")

	var roles []ObjectRowView
	for _, r := range d.Roles {
		roles = append(roles, ObjectRowView{
			Name: r.Name, Detail: strings.TrimSpace(r.Role + " · " + r.Attributes),
		})
	}
	// DatabaseRole, not empty: the journal taps this scope like the
	// other three, so a row here does resolve to a retained
	// revision. Leaving it blank silently withheld the raw
	// definition from the one declarative kind that had it.
	add("Roles", "DatabaseRole", roles, "no declared roles were observed")

	var pubs []ObjectRowView
	for _, r := range d.Publications {
		pubs = append(pubs, ObjectRowView{
			Name: r.Name, Detail: strings.TrimSpace(r.Publication + " · in " + r.Database),
		})
	}
	add("Publications", "Publication", pubs, "no Publication object targets this cluster")

	var subs []ObjectRowView
	for _, r := range d.Subscriptions {
		subs = append(subs, ObjectRowView{
			Name: r.Name, Detail: strings.TrimSpace(r.Subscription + " · in " + r.Database + " · from " + r.Publication),
		})
	}
	add("Subscriptions", "Subscription", subs, "no Subscription object targets this cluster")
	return g
}

// objectsClusterDetail states the cluster row in the operator's own
// words, or says plainly that the resource was not present.
func objectsClusterDetail(p *Page) string {
	if p.Cluster == nil || p.Cluster.Absent {
		return "not present in this namespace"
	}
	detail := p.Cluster.Phase
	if p.Cluster.CurrentPrimary != "" {
		detail += " · primary " + p.Cluster.CurrentPrimary
	}
	return strings.TrimSpace(detail)
}

// objectsEmptyNote states an observed empty list in the source's own
// terms. A kind with rows needs no note; the rows are the answer.
func objectsEmptyNote(rows int, empty string) string {
	if rows > 0 {
		return ""
	}
	return empty
}

// Count reports how many objects the group holds, so the heading can
// say the size without the template counting across kinds.
func (g ObjectGroupView) Count() int {
	n := 0
	for _, k := range g.Kinds {
		n += len(k.Rows)
	}
	return n
}

// Summary is the group heading's count, phrased so an unobserved kind
// never reads as a confirmed zero.
func (g ObjectGroupView) Summary() string {
	n := g.Count()
	unobserved := 0
	for _, k := range g.Kinds {
		if !k.Observed {
			unobserved++
		}
	}
	s := fmt.Sprintf("%d observed", n)
	if unobserved > 0 {
		s += fmt.Sprintf(", %d kind(s) unread", unobserved)
	}
	return s
}
