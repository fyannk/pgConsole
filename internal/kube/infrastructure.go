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

package kube

import (
	"context"
	"log/slog"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

// The cluster's physical resources: the Services clients dial, the
// claims each instance keeps its data on, and the volume snapshots
// taken of them. Membership is the same proof the pods use — the
// controller owner reference must name the configured Cluster — so a
// claim or service belonging to a neighbouring cluster in the same
// namespace is never presented as this one's.
var (
	serviceGVR = schema.GroupVersionResource{Version: "v1", Resource: "services"}
	pvcGVR     = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	// The snapshot API is a separate CRD many clusters do not install.
	snapshotGVR = schema.GroupVersionResource{
		Group: "snapshot.storage.k8s.io", Version: "v1", Resource: "volumesnapshots"}
)

const (
	// pvcRoleLabel is the operator's label for a claim's purpose.
	pvcRoleLabel = "cnpg.io/pvcRole"
	// pvcInstanceLabel names the instance a claim serves.
	pvcInstanceLabel = "cnpg.io/instanceName"
	// infrastructurePageSize bounds a single list page.
	infrastructurePageSize = 100
)

// FetchInfrastructure lists the cluster's services, claims and volume
// snapshots. A missing snapshot API is recorded as unobservable rather
// than failing the sweep: "the CRD is not installed" and "there are no
// snapshots" are different claims and must not be blended.
func (c *Client) FetchInfrastructure(ctx context.Context) (observe.InfrastructureState, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	var state observe.InfrastructureState
	var truncated bool
	var err error

	if state.Services, state.ServiceResourceVersion, truncated, err = listOwned(
		ctx, c, serviceGVR, scopeServices, true, c.convertService); err != nil {
		return observe.InfrastructureState{}, err
	}
	state.Truncated = state.Truncated || truncated

	if state.Volumes, state.VolumeResourceVersion, truncated, err = listOwned(
		ctx, c, pvcGVR, scopeVolumes, true, c.convertVolume); err != nil {
		return observe.InfrastructureState{}, err
	}
	state.Truncated = state.Truncated || truncated

	state.Snapshots, state.SnapshotResourceVersion, truncated, err = listOwned(
		ctx, c, snapshotGVR, scopeSnapshots, true, c.convertSnapshot)
	switch {
	case err == nil:
		state.SnapshotsObservable = true
		state.Truncated = state.Truncated || truncated
	case redact.Categorize(err) == redact.CategoryNotFound,
		redact.Categorize(err) == redact.CategoryForbidden:
		// No snapshot API here, or no grant for it. Either way the
		// console has not observed snapshots and says exactly that.
		c.logSnapshotsUnobservable(err)
	default:
		return observe.InfrastructureState{}, err
	}

	if err := c.fetchChildren(ctx, &state); err != nil {
		return observe.InfrastructureState{}, err
	}

	return state, nil
}

// listOwned pages one kind, keeping only the objects this cluster owns.
// record=false keeps the listing out of the history journal — the child
// kinds are never recorded, because the journal must never see a
// Secret's payload and the set travels together.
func listOwned[T any](
	ctx context.Context,
	c *Client,
	gvr schema.GroupVersionResource,
	op string,
	record bool,
	convert func(map[string]any) (T, bool, error),
) ([]T, string, bool, error) {
	var kept []T
	examined := 0
	opts := metav1.ListOptions{Limit: infrastructurePageSize}
	rv := ""
	var seed *seedRecorder
	if record {
		seed = c.seedRecord(op)
	}
	for {
		list, err := c.dyn.Resource(gvr).Namespace(c.opts.Namespace).List(ctx, opts)
		if err != nil {
			return nil, "", false, categorize(op+" list", err)
		}
		rv = list.GetResourceVersion()
		for i := range list.Items {
			facts, member, err := convert(list.Items[i].Object)
			if err != nil {
				return nil, "", false, err
			}
			if member {
				seed.add(list.Items[i].Object)
				kept = append(kept, facts)
			}
		}
		examined += len(list.Items)
		if list.GetContinue() == "" {
			break
		}
		if len(kept) > observe.MaxInfrastructureObjects || examined >= 10*infrastructurePageSize {
			seed.commit(false)
			return kept, rv, true, nil
		}
		opts.Continue = list.GetContinue()
	}
	seed.commit(true)
	return kept, rv, len(kept) > observe.MaxInfrastructureObjects, nil
}

// WatchInfrastructure merges one watch per kind. Any stream ending
// terminates the merged watch, so the collector re-lists and republishes
// only a complete generation. The snapshot watch is opened only when the
// seed observed the API at all.
func (c *Client) WatchInfrastructure(ctx context.Context, state observe.InfrastructureState) (observe.InfrastructureWatch, error) {
	specs := []struct {
		gvr  schema.GroupVersionResource
		rv   string
		op   string
		pump pump[observe.InfrastructureChange]
	}{
		{serviceGVR, state.ServiceResourceVersion, "services watch", tap(c, scopeServices, c.pumpService)},
		{pvcGVR, state.VolumeResourceVersion, "volumes watch", tap(c, scopeVolumes, c.pumpVolume)},
	}
	if state.SnapshotsObservable {
		specs = append(specs, struct {
			gvr  schema.GroupVersionResource
			rv   string
			op   string
			pump pump[observe.InfrastructureChange]
		}{snapshotGVR, state.SnapshotResourceVersion, "snapshots watch", tap(c, scopeSnapshots, c.pumpSnapshot)})
	}
	// One stream per child kind the seed actually observed; a kind that
	// was refused has no resource version and gets no watch. Untapped:
	// the children stay out of the history journal.
	for _, child := range childKinds {
		rv, ok := state.ChildResourceVersions[child.kind]
		if !ok {
			continue
		}
		specs = append(specs, struct {
			gvr  schema.GroupVersionResource
			rv   string
			op   string
			pump pump[observe.InfrastructureChange]
		}{child.gvr, rv, child.op + " watch", c.childPump(child.kind)})
	}

	var started []watch.Interface
	stopStarted := func() {
		for _, w := range started {
			w.Stop()
		}
	}
	var pumps []pump[observe.InfrastructureChange]
	for _, spec := range specs {
		w, err := c.dyn.Resource(spec.gvr).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{
			ResourceVersion: spec.rv,
		})
		if err != nil {
			stopStarted()
			return nil, categorize(spec.op, err)
		}
		started = append(started, w)
		pumps = append(pumps, spec.pump)
	}

	items, stop := fanIn(ctx, started, pumps)
	return changeStream[observe.InfrastructureChange]{
		stream[observe.InfrastructureChange]{items: items, stop: stop}}, nil
}

// convertService reduces one Service and reports membership.
func (c *Client) convertService(content map[string]any) (observe.ServiceFacts, bool, error) {
	var svc corev1.Service
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &svc); err != nil {
		return observe.ServiceFacts{}, false, redact.NewError("service convert", redact.CategoryInternal, err)
	}
	facts := observe.ServiceFacts{
		Name:      svc.Name,
		UID:       string(svc.UID),
		Type:      string(svc.Spec.Type),
		ClusterIP: svc.Spec.ClusterIP,
	}
	// The operator does not label the read/write split, so it is read
	// from the name suffix the operator itself assigns.
	facts.Role = serviceRole(svc.Name, c.opts.ClusterName)
	if svc.Spec.ClusterIP == corev1.ClusterIPNone {
		facts.Headless = true
		facts.ClusterIP = ""
	}
	if len(svc.Spec.Ports) > 0 {
		port := svc.Spec.Ports[0].Port
		facts.Port = &port
	}
	for key, value := range svc.Spec.Selector {
		facts.TargetSelector = append(facts.TargetSelector, key+"="+value)
	}
	sort.Strings(facts.TargetSelector)
	return facts, c.ownedByCluster(&svc.ObjectMeta), nil
}

// serviceRole names the service's job from the suffix CloudNativePG
// gives it. An unrecognised name keeps no role rather than a guess.
func serviceRole(name, cluster string) string {
	switch name {
	case cluster + "-rw":
		return "read-write"
	case cluster + "-ro":
		return "read-only"
	case cluster + "-r":
		return "any instance"
	default:
		return ""
	}
}

// convertVolume reduces one PersistentVolumeClaim.
func (c *Client) convertVolume(content map[string]any) (observe.VolumeFacts, bool, error) {
	var pvc corev1.PersistentVolumeClaim
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &pvc); err != nil {
		return observe.VolumeFacts{}, false, redact.NewError("volume convert", redact.CategoryInternal, err)
	}
	facts := observe.VolumeFacts{
		Name:       pvc.Name,
		UID:        string(pvc.UID),
		Instance:   pvc.Labels[pvcInstanceLabel],
		Role:       pvc.Labels[pvcRoleLabel],
		Phase:      string(pvc.Status.Phase),
		VolumeName: pvc.Spec.VolumeName,
	}
	if pvc.Spec.StorageClassName != nil {
		facts.StorageClass = *pvc.Spec.StorageClassName
	}
	// The status capacity is what the volume actually provides; the
	// request is only what was asked for, so it is the fallback.
	if size, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
		facts.Capacity = size.String()
	} else if size, ok := pvc.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		facts.Capacity = size.String()
	}
	if facts.Instance == "" {
		facts.Instance = pvc.Name
	}
	return facts, c.ownedByCluster(&pvc.ObjectMeta), nil
}

// convertSnapshot reduces one VolumeSnapshot. The type is a CRD this
// binary does not import, so it is read from the unstructured content.
func (c *Client) convertSnapshot(content map[string]any) (observe.SnapshotFacts, bool, error) {
	object := unstructured.Unstructured{Object: content}
	facts := observe.SnapshotFacts{Name: object.GetName(), UID: string(object.GetUID())}
	facts.SourceClaim, _, _ = unstructured.NestedString(content, "spec", "source", "persistentVolumeClaimName")
	facts.RestoreSize, _, _ = unstructured.NestedString(content, "status", "restoreSize")
	if ready, found, err := unstructured.NestedBool(content, "status", "readyToUse"); err == nil && found {
		value := ready
		facts.Ready = &value
	}
	if stamp, _, _ := unstructured.NestedString(content, "status", "creationTime"); stamp != "" {
		if at, err := time.Parse(time.RFC3339, stamp); err == nil {
			facts.CreatedAt = &at
		}
	}
	// A snapshot is the cluster's when the operator labels it so: its
	// controller owner reference names the Backup, not the Cluster.
	return facts, object.GetLabels()[clusterLabel] == c.opts.ClusterName, nil
}

// ownedByCluster verifies the controller owner reference names the
// configured Cluster, which is the same proof the pod roster uses.
func (c *Client) ownedByCluster(meta *metav1.ObjectMeta) bool {
	return c.ownedRefs(meta.OwnerReferences)
}

// ownedRefs is the same proof over a bare reference list, for objects
// read without a typed conversion.
func (c *Client) ownedRefs(refs []metav1.OwnerReference) bool {
	for i := range refs {
		ref := &refs[i]
		if ref.Controller == nil || !*ref.Controller {
			continue
		}
		group, _, ok := splitAPIVersion(ref.APIVersion)
		if !ok || group != clusterGVR.Group {
			return false
		}
		return ref.Kind == "Cluster" && ref.Name == c.opts.ClusterName
	}
	return false
}

func (c *Client) pumpService(event watch.Event) (observe.InfrastructureChange, bool, bool) {
	return infrastructurePump(event, "Service", c.convertService,
		func(f observe.ServiceFacts) observe.InfrastructureChange {
			return observe.InfrastructureChange{Service: &f}
		},
		func(f observe.ServiceFacts) (string, string) { return f.Name, f.UID })
}

func (c *Client) pumpVolume(event watch.Event) (observe.InfrastructureChange, bool, bool) {
	return infrastructurePump(event, "PersistentVolumeClaim", c.convertVolume,
		func(f observe.VolumeFacts) observe.InfrastructureChange {
			return observe.InfrastructureChange{Volume: &f}
		},
		func(f observe.VolumeFacts) (string, string) { return f.Name, f.UID })
}

func (c *Client) pumpSnapshot(event watch.Event) (observe.InfrastructureChange, bool, bool) {
	return infrastructurePump(event, "VolumeSnapshot", c.convertSnapshot,
		func(f observe.SnapshotFacts) observe.InfrastructureChange {
			return observe.InfrastructureChange{Snapshot: &f}
		},
		func(f observe.SnapshotFacts) (string, string) { return f.Name, f.UID })
}

// infrastructurePump builds one kind's pump. An object belonging to
// another cluster is skipped, not fatal: these watches are
// namespace-scoped because RBAC cannot pin a watch by name.
func infrastructurePump[T any](
	event watch.Event,
	kind string,
	convert func(map[string]any) (T, bool, error),
	put func(T) observe.InfrastructureChange,
	identity func(T) (name, uid string),
) (observe.InfrastructureChange, bool, bool) {
	var none observe.InfrastructureChange
	obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
	if !ok {
		return none, false, true
	}
	facts, member, err := convert(obj.UnstructuredContent())
	if err != nil {
		return none, false, true
	}
	if !member {
		return none, false, false
	}
	switch event.Type {
	case watch.Added, watch.Modified:
		return put(facts), true, false
	case watch.Deleted:
		name, uid := identity(facts)
		return observe.InfrastructureChange{Deleted: &observe.InfrastructureDeletion{
			Kind: kind, Name: name, UID: uid,
		}}, true, false
	case watch.Bookmark:
		return none, false, false
	default:
		return none, false, true
	}
}

func (c *Client) logSnapshotsUnobservable(err error) {
	c.logger.Info("volume snapshots unobservable",
		slog.String("category", redact.Safe(err)))
}
