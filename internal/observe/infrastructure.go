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

package observe

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// The infrastructure snapshot is what a DBA reaches for when the
// question is physical rather than logical: which addresses clients
// actually connect to, what disk each instance actually holds, and
// which volume snapshots actually exist. The console read none of this
// before — the wiring diagram named endpoints by string-building from
// the cluster name and said nothing about storage at all, which reads
// as a claim about a cluster that was never observed.
//
// The three kinds travel together because they are read together and
// answer one question between them. One freshness line covers the set,
// exactly as the declarative-objects snapshot does for its four kinds.
//
// Volume snapshots are optional in a way the others are not: the API is
// a separate CRD that many clusters do not install. Its absence is a
// successful observation — SnapshotsObservable false — and never an
// error, because "no snapshot API here" and "no snapshots here" are
// different claims.

// MaxInfrastructureObjects bounds each rendered list.
const MaxInfrastructureObjects = 50

// ServiceFacts is one observed Service that fronts the cluster.
type ServiceFacts struct {
	// Name is the service name.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// Role is the CloudNativePG service role — rw, ro or r — taken from
	// the operator's own label, empty when it carries none.
	Role string
	// Type is the service type, such as ClusterIP.
	Type string
	// ClusterIP is the allocated address; empty for a headless service,
	// which is a fact rather than an absence.
	ClusterIP string
	// Headless reports the explicit None address.
	Headless bool
	// Port is the first reported port; nil when none is reported.
	Port *int32
	// TargetSelector is the label selector the service routes on, as
	// reported, sorted for a stable rendering.
	TargetSelector []string
}

// VolumeFacts is one observed PersistentVolumeClaim of the cluster.
type VolumeFacts struct {
	// Name is the claim name, which for CloudNativePG matches the
	// instance it belongs to (or carries a role suffix, such as -wal).
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// Instance is the instance the claim serves, from the operator's
	// label; empty when unlabelled.
	Instance string
	// Role is the claim's purpose as the operator labels it, such as
	// PG_DATA or PG_WAL; empty when unlabelled.
	Role string
	// Phase is the claim phase, such as Bound.
	Phase string
	// Capacity is the reported capacity, empty when unreported.
	Capacity string
	// StorageClass is the class the claim was provisioned from; empty
	// when the cluster uses the default and reports none.
	StorageClass string
	// VolumeName is the bound PersistentVolume; empty until bound.
	VolumeName string
}

// SnapshotFacts is one observed VolumeSnapshot.
type SnapshotFacts struct {
	// Name is the snapshot name.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// SourceClaim is the claim the snapshot was taken from.
	SourceClaim string
	// Ready is the reported readiness; nil when unreported, which is
	// not the same as not ready.
	Ready *bool
	// RestoreSize is the reported restore size, empty when unreported.
	RestoreSize string
	// CreatedAt is the reported creation time; nil when unreported.
	CreatedAt *time.Time
}

// ChildFacts is one further object the Cluster owns: a Secret, a
// ConfigMap, a PodDisruptionBudget, a ServiceAccount, a Role, a
// RoleBinding or a Job. These kinds share one facts shape because each
// contributes a name and at most two observed details; the per-kind
// fields below are nil or empty for every other kind.
//
// For a Secret only the metadata is retained: its name, its type and
// how many keys it holds. No key name and no value ever leaves the
// adapter, and none of these objects is recorded into history.
type ChildFacts struct {
	// Kind is the Kubernetes kind: Secret, ConfigMap,
	// PodDisruptionBudget, ServiceAccount, Role, RoleBinding, Job.
	Kind string
	// Name is the object name.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// CreatedAt is the object's creation time; nil when unreported.
	CreatedAt *time.Time
	// SecretType is the Secret's reported type.
	SecretType string
	// Keys counts a Secret's or ConfigMap's entries; nil elsewhere.
	Keys *int
	// MinAvailable and MaxUnavailable are the PodDisruptionBudget's
	// constraint exactly as declared; empty when the side is unset.
	MinAvailable   string
	MaxUnavailable string
	// DisruptionsAllowed is the PodDisruptionBudget's reported headroom.
	DisruptionsAllowed *int32
	// Rules counts a Role's rules; Subjects a RoleBinding's subjects.
	Rules    *int
	Subjects *int
	// RoleRef is the role a RoleBinding grants.
	RoleRef string
	// Active, Succeeded and Failed are a Job's reported pod counts.
	Active    *int32
	Succeeded *int32
	Failed    *int32
	// BootstrapImage is the operator's own image on a Job's
	// bootstrap-controller init container, empty elsewhere. It exists
	// for the cluster that breaks before its first instance pod: the
	// bootstrap Jobs are then the only place the operator's version is
	// observable at all.
	BootstrapImage string
}

// InfrastructureState is one complete observation of the set.
type InfrastructureState struct {
	Services  []ServiceFacts
	Volumes   []VolumeFacts
	Snapshots []SnapshotFacts
	// Children are the further owned kinds, gathered as one list and
	// distinguished by their Kind field.
	Children []ChildFacts
	// SnapshotsObservable reports that the VolumeSnapshot API answered
	// at all. False means the cluster does not install it, which the
	// rendering states rather than reading as "no snapshots".
	SnapshotsObservable bool
	// ChildrenUnobserved lists the child kinds whose API refused or
	// lacked the resource, sorted. "Not granted" and "none exist" are
	// different claims, and the rendering states the first.
	ChildrenUnobserved []string
	// Truncated reports a source or display ceiling on any list.
	Truncated bool
	// ServiceResourceVersion and the rest resume each watch.
	ServiceResourceVersion  string
	VolumeResourceVersion   string
	SnapshotResourceVersion string
	// ChildResourceVersions resumes each observed child kind's watch.
	ChildResourceVersions map[string]string
}

// InfrastructureChange is one watch event, already reduced.
type InfrastructureChange struct {
	Service  *ServiceFacts
	Volume   *VolumeFacts
	Snapshot *SnapshotFacts
	Child    *ChildFacts
	// Deleted names the object removed, when the event was a deletion.
	Deleted *InfrastructureDeletion
}

// InfrastructureDeletion identifies a removed object.
type InfrastructureDeletion struct {
	Kind string
	Name string
	UID  string
}

// InfrastructureWatch is one live subscription.
type InfrastructureWatch interface {
	// Changes yields reduced events until the watch ends.
	Changes() <-chan InfrastructureChange
	// Stop releases the subscription.
	Stop()
}

// InfrastructureSource seeds and follows the set.
type InfrastructureSource interface {
	// FetchInfrastructure lists the current set.
	FetchInfrastructure(ctx context.Context) (InfrastructureState, error)
	// WatchInfrastructure follows it from the seeded versions.
	WatchInfrastructure(ctx context.Context, state InfrastructureState) (InfrastructureWatch, error)
}

// InfrastructureSnapshot is the immutable published view.
type InfrastructureSnapshot struct {
	Generation          uint64
	ObservedAt          time.Time
	Stale               bool
	Services            []ServiceFacts
	Volumes             []VolumeFacts
	Snapshots           []SnapshotFacts
	Children            []ChildFacts
	SnapshotsObservable bool
	ChildrenUnobserved  []string
	Truncated           bool
}

// InfrastructureStore holds the current snapshot for concurrent readers.
type InfrastructureStore struct {
	mu      sync.RWMutex
	current InfrastructureSnapshot
	present bool
}

// NewInfrastructureStore builds an empty store.
func NewInfrastructureStore() *InfrastructureStore { return &InfrastructureStore{} }

// CurrentInfrastructure returns the snapshot and whether one exists.
func (s *InfrastructureStore) CurrentInfrastructure() (InfrastructureSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current, s.present
}

// publish replaces the snapshot, bounding every list.
func (s *InfrastructureStore) publish(state InfrastructureState, observedAt time.Time) {
	services, cutServices := bounded(state.Services,
		func(a, b ServiceFacts) bool { return a.Name < b.Name }, MaxInfrastructureObjects)
	volumes, cutVolumes := bounded(state.Volumes,
		func(a, b VolumeFacts) bool { return a.Name < b.Name }, MaxInfrastructureObjects)
	snapshots, cutSnapshots := bounded(state.Snapshots,
		func(a, b SnapshotFacts) bool { return a.Name < b.Name }, MaxInfrastructureObjects)
	children, cutChildren := boundedChildren(state.Children)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = InfrastructureSnapshot{
		Generation:          s.current.Generation + 1,
		ObservedAt:          observedAt,
		Services:            services,
		Volumes:             volumes,
		Snapshots:           snapshots,
		Children:            children,
		SnapshotsObservable: state.SnapshotsObservable,
		ChildrenUnobserved:  append([]string(nil), state.ChildrenUnobserved...),
		Truncated:           state.Truncated || cutServices || cutVolumes || cutSnapshots || cutChildren,
	}
	s.present = true
}

// boundedChildren sorts the children by kind then name and bounds each
// kind on its own, so a burst of one kind never pushes another out.
func boundedChildren(in []ChildFacts) ([]ChildFacts, bool) {
	kept, _ := bounded(in, func(a, b ChildFacts) bool {
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	}, len(in))
	out := kept[:0:0]
	cut := false
	perKind := 0
	for i, c := range kept {
		if i > 0 && kept[i-1].Kind != c.Kind {
			perKind = 0
		}
		if perKind >= MaxInfrastructureObjects {
			cut = true
			continue
		}
		perKind++
		out = append(out, c)
	}
	return out, cut
}

// markStale flags lost contact while keeping the last-good set.
func (s *InfrastructureStore) markStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.present {
		s.current.Stale = true
	}
}

// InfrastructureCollector keeps the store current.
type InfrastructureCollector struct {
	source InfrastructureSource
	store  *InfrastructureStore
	state  InfrastructureState
	loop   *loop[InfrastructureState, InfrastructureChange]
}

// NewInfrastructureCollector builds the collector.
func NewInfrastructureCollector(source InfrastructureSource, store *InfrastructureStore, clock Clock, logger *slog.Logger) *InfrastructureCollector {
	c := &InfrastructureCollector{source: source, store: store}
	c.loop = newLoop[InfrastructureState, InfrastructureChange](c, clock, logger)
	return c
}

// Run follows the set until the context ends.
func (c *InfrastructureCollector) Run(ctx context.Context) error { return c.loop.Run(ctx) }

func (c *InfrastructureCollector) op() string { return "infrastructure" }

func (c *InfrastructureCollector) seed(ctx context.Context) (InfrastructureState, error) {
	state, err := c.source.FetchInfrastructure(ctx)
	if err != nil {
		return InfrastructureState{}, err
	}
	c.state = state
	return state, nil
}

func (c *InfrastructureCollector) follow(ctx context.Context, from InfrastructureState) (<-chan InfrastructureChange, func(), error) {
	w, err := c.source.WatchInfrastructure(ctx, from)
	if err != nil {
		return nil, nil, err
	}
	return w.Changes(), w.Stop, nil
}

// apply folds one change into the pending state.
func (c *InfrastructureCollector) apply(change InfrastructureChange) bool {
	switch {
	case change.Service != nil:
		c.state.Services = upsertNamed(c.state.Services, *change.Service,
			func(s ServiceFacts) string { return s.Name })
	case change.Volume != nil:
		c.state.Volumes = upsertNamed(c.state.Volumes, *change.Volume,
			func(v VolumeFacts) string { return v.Name })
	case change.Snapshot != nil:
		c.state.Snapshots = upsertNamed(c.state.Snapshots, *change.Snapshot,
			func(s SnapshotFacts) string { return s.Name })
	case change.Child != nil:
		// Kinds share the list, so the key is kind and name together —
		// a PodDisruptionBudget and a ServiceAccount may share a name.
		c.state.Children = upsertNamed(c.state.Children, *change.Child,
			func(f ChildFacts) string { return f.Kind + "/" + f.Name })
	case change.Deleted != nil:
		switch change.Deleted.Kind {
		case "Service":
			c.state.Services = removeNamed(c.state.Services, change.Deleted,
				func(s ServiceFacts) (string, string) { return s.Name, s.UID })
		case "PersistentVolumeClaim":
			c.state.Volumes = removeNamed(c.state.Volumes, change.Deleted,
				func(v VolumeFacts) (string, string) { return v.Name, v.UID })
		case "VolumeSnapshot":
			c.state.Snapshots = removeNamed(c.state.Snapshots, change.Deleted,
				func(s SnapshotFacts) (string, string) { return s.Name, s.UID })
		default:
			c.state.Children = removeChild(c.state.Children, change.Deleted)
		}
	default:
		return false
	}
	return true
}

func (c *InfrastructureCollector) publish(observedAt time.Time) {
	c.store.publish(c.state, observedAt)
}

func (c *InfrastructureCollector) markStale() { c.store.markStale() }

// upsertNamed replaces an entry sharing the name, or appends it.
func upsertNamed[T any](in []T, item T, name func(T) string) []T {
	for i := range in {
		if name(in[i]) == name(item) {
			in[i] = item
			return in
		}
	}
	return append(in, item)
}

// removeChild drops the child whose kind, name and UID all match, so a
// deletion of one kind never removes a same-named object of another.
func removeChild(in []ChildFacts, gone *InfrastructureDeletion) []ChildFacts {
	for i := range in {
		if in[i].Kind == gone.Kind && in[i].Name == gone.Name &&
			(gone.UID == "" || in[i].UID == gone.UID) {
			return append(in[:i], in[i+1:]...)
		}
	}
	return in
}

// removeNamed drops the entry whose name and UID both match, so a
// deletion never removes a newer incarnation that reused the name.
func removeNamed[T any](in []T, gone *InfrastructureDeletion, id func(T) (string, string)) []T {
	for i := range in {
		name, uid := id(in[i])
		if name == gone.Name && (gone.UID == "" || uid == gone.UID) {
			return append(in[:i], in[i+1:]...)
		}
	}
	return in
}
