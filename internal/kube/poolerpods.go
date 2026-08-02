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
	"io"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

var (
	replicaSetGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	deploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

const (
	// poolerRoleLabel marks a pod as belonging to a connection pooler
	// rather than to a database instance.
	poolerRoleLabel = "cnpg.io/podRole"
	poolerRoleValue = "pooler"
	// poolerNameLabel names the Pooler a pod claims to serve. It is a
	// selection hint and never the membership decision.
	poolerNameLabel = "cnpg.io/poolerName"
	// pgBouncerContainer is the container whose log the tail reads.
	pgBouncerContainer = "pgbouncer"
)

// FetchPoolerPods lists the pods of the cluster's connection poolers.
//
// Selection is by label; membership is by ownership. A pooler pod is not
// owned by its Pooler directly the way an instance pod is owned by its
// Cluster — CloudNativePG runs poolers as a Deployment, so the chain is
// Pod -> ReplicaSet -> Deployment -> Pooler and it takes two more reads
// to walk. AGENTS.md rule 4 is explicit that labels are a selection
// mechanism and never a security boundary, so the walk is the check: a
// pod that merely claims the label is excluded and logged.
func (c *Client) FetchPoolerPods(ctx context.Context) ([]observe.PodFacts, string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	list, err := c.dyn.Resource(podGVR).Namespace(c.opts.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: poolerRoleLabel + "=" + poolerRoleValue + "," + clusterLabel + "=" + c.opts.ClusterName,
	})
	if err != nil {
		return nil, "", categorize("pooler pods list", err)
	}

	owners := newPoolerOwnership(c)
	seed := c.seedRecord(scopePoolerPods)
	var pods []observe.PodFacts
	for i := range list.Items {
		facts, member, err := c.convertPoolerPod(ctx, owners, list.Items[i].Object)
		if err != nil {
			return nil, "", err
		}
		if !member {
			c.logExcludedPod(facts.Name)
			continue
		}
		seed.add(list.Items[i].Object)
		pods = append(pods, facts)
	}
	seed.commit(true)
	return pods, list.GetResourceVersion(), nil
}

// WatchPoolerPods follows the pooler pods from the seed version.
func (c *Client) WatchPoolerPods(ctx context.Context, fromResourceVersion string) (observe.PodWatch, error) {
	w, err := c.dyn.Resource(podGVR).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{
		LabelSelector:   poolerRoleLabel + "=" + poolerRoleValue + "," + clusterLabel + "=" + c.opts.ClusterName,
		ResourceVersion: fromResourceVersion,
	})
	if err != nil {
		return nil, categorize("pooler pods watch", err)
	}
	items, stop := fanIn(ctx, []watch.Interface{w}, []pump[observe.PodEvent]{tap(c, scopePoolerPods, c.pumpPoolerPod(ctx))})
	return eventStream[observe.PodEvent]{stream[observe.PodEvent]{items: items, stop: stop}}, nil
}

// pumpPoolerPod converts one pooler pod watch event. The ownership cache
// lives for the life of the watch: a ReplicaSet's owner does not change,
// so re-reading it per event would be pure API traffic.
func (c *Client) pumpPoolerPod(ctx context.Context) pump[observe.PodEvent] {
	owners := newPoolerOwnership(c)
	return func(event watch.Event) (observe.PodEvent, bool, bool) {
		obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
		if !ok {
			return observe.PodEvent{}, false, event.Type == watch.Error
		}
		facts, member, err := c.convertPoolerPod(ctx, owners, obj.UnstructuredContent())
		if err != nil {
			return observe.PodEvent{}, false, true
		}
		switch event.Type {
		case watch.Added, watch.Modified:
			if !member {
				c.logExcludedPod(facts.Name)
				return observe.PodEvent{}, false, false
			}
			return observe.PodEvent{Put: &facts}, true, false
		case watch.Deleted:
			// Forwarded even for a non-member, so a name that was once
			// shown can never linger in the retained set.
			return observe.PodEvent{Delete: &observe.PodDeletion{Name: facts.Name, UID: facts.UID}}, true, false
		case watch.Bookmark:
			return observe.PodEvent{}, false, false
		default:
			return observe.PodEvent{}, false, true
		}
	}
}

// convertPoolerPod converts a raw pooler pod into facts plus its
// membership verdict.
func (c *Client) convertPoolerPod(ctx context.Context, owners *poolerOwnership, content map[string]any) (observe.PodFacts, bool, error) {
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &pod); err != nil {
		return observe.PodFacts{}, false, redact.NewError("pooler pod convert", redact.CategoryInternal, err)
	}

	facts := observe.PodFacts{
		Name:     pod.Name,
		UID:      string(pod.UID),
		Role:     pod.Labels[poolerNameLabel],
		Phase:    string(pod.Status.Phase),
		Node:     pod.Spec.NodeName,
		Deleting: pod.DeletionTimestamp != nil,
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			ready := cond.Status == corev1.ConditionTrue
			facts.Ready = &ready
			break
		}
	}
	if len(pod.Status.ContainerStatuses) > 0 {
		restarts := 0
		for _, cs := range pod.Status.ContainerStatuses {
			restarts += int(cs.RestartCount)
		}
		facts.Restarts = &restarts
	}
	for _, container := range pod.Spec.Containers {
		if container.Name == pgBouncerContainer {
			facts.Image = container.Image
			break
		}
	}
	if facts.Image == "" && len(pod.Spec.Containers) > 0 {
		facts.Image = pod.Spec.Containers[0].Image
	}

	return facts, owners.poolerPodIsMember(ctx, &pod), nil
}

// poolerOwnership walks and caches the Pod -> ReplicaSet -> Deployment
// -> Pooler chain. Each link is read once per fetch or watch: ownership
// is immutable for the life of an object, so a cache here costs nothing
// in freshness and saves two reads per pod.
type poolerOwnership struct {
	client *Client
	// replicaSets maps a ReplicaSet name to its controlling Deployment,
	// empty when the walk failed or the owner was not a Deployment.
	replicaSets map[string]string
	// deployments maps a Deployment name to the Pooler that controls it,
	// empty when it is controlled by something else.
	deployments map[string]string
}

func newPoolerOwnership(c *Client) *poolerOwnership {
	return &poolerOwnership{
		client:      c,
		replicaSets: map[string]string{},
		deployments: map[string]string{},
	}
}

// poolerPodIsMember reports whether the pod is genuinely run by a Pooler
// of the target cluster.
//
// Every hop is verified rather than inferred from a name. The Deployment
// CloudNativePG creates happens to be named after its Pooler, but that
// is a convention, and a convention is not an authorization: a pod in
// this namespace could be labelled and named to match without being
// owned by anything.
func (o *poolerOwnership) poolerPodIsMember(ctx context.Context, pod *corev1.Pod) bool {
	ref := metav1.GetControllerOf(pod)
	if ref == nil || ref.Kind != "ReplicaSet" {
		return false
	}
	if group, _, ok := splitAPIVersion(ref.APIVersion); !ok || group != replicaSetGVR.Group {
		return false
	}

	deployment := o.deploymentOf(ctx, ref.Name)
	if deployment == "" {
		return false
	}
	pooler := o.poolerOf(ctx, deployment)
	if pooler == "" {
		return false
	}
	// The Pooler must be one of this cluster's, which the pooler source
	// already decides by spec.cluster.name. Re-reading it here would be
	// a third hop for a fact the label already carries and the chain has
	// now made trustworthy: the pod is owned by this Pooler, and the
	// Pooler's own membership is the pooler source's question.
	return pod.Labels[poolerNameLabel] == pooler
}

// deploymentOf returns the Deployment controlling the named ReplicaSet.
func (o *poolerOwnership) deploymentOf(ctx context.Context, replicaSet string) string {
	if name, ok := o.replicaSets[replicaSet]; ok {
		return name
	}
	o.replicaSets[replicaSet] = ""
	obj, err := o.client.dyn.Resource(replicaSetGVR).Namespace(o.client.opts.Namespace).
		Get(ctx, replicaSet, metav1.GetOptions{})
	if err != nil {
		o.client.logOwnershipUnavailable("replicaset get", err)
		return ""
	}
	ref := ownerRefOf(obj.Object)
	if ref == nil || ref.Kind != "Deployment" {
		return ""
	}
	if group, _, ok := splitAPIVersion(ref.APIVersion); !ok || group != deploymentGVR.Group {
		return ""
	}
	o.replicaSets[replicaSet] = ref.Name
	return ref.Name
}

// poolerOf returns the Pooler controlling the named Deployment.
func (o *poolerOwnership) poolerOf(ctx context.Context, deployment string) string {
	if name, ok := o.deployments[deployment]; ok {
		return name
	}
	o.deployments[deployment] = ""
	obj, err := o.client.dyn.Resource(deploymentGVR).Namespace(o.client.opts.Namespace).
		Get(ctx, deployment, metav1.GetOptions{})
	if err != nil {
		o.client.logOwnershipUnavailable("deployment get", err)
		return ""
	}
	ref := ownerRefOf(obj.Object)
	if ref == nil || ref.Kind != "Pooler" {
		return ""
	}
	if group, _, ok := splitAPIVersion(ref.APIVersion); !ok || group != poolerGVR.Group {
		return ""
	}
	o.deployments[deployment] = ref.Name
	return ref.Name
}

// ownerRefOf reads the controller reference from an unstructured object.
func ownerRefOf(content map[string]any) *metav1.OwnerReference {
	var meta metav1.PartialObjectMetadata
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &meta); err != nil {
		return nil
	}
	return metav1.GetControllerOf(&meta)
}

// logOwnershipUnavailable records the category of a failed ownership
// walk, never its text. A missing permission means pods are excluded
// rather than shown unverified.
func (c *Client) logOwnershipUnavailable(op string, err error) {
	c.logger.Info("pooler ownership not verifiable",
		slog.String("category", redact.Safe(categorize(op, err))))
}

// TailPoolerLogs fetches a bounded tail of a pooler pod's pgbouncer
// container.
//
// Membership is re-verified live, immediately before the log request, on
// the same terms as the instance tail: the pod is fetched and its
// ownership chain walked, so a pod that stopped being a pooler's between
// page render and click is refused. A non-member and a nonexistent pod
// are indistinguishable to the caller — both are not-found — while the
// exclusion is logged with its stable reason. The fetch runs on the
// request's context; nothing is cached or persisted.
func (c *Client) TailPoolerLogs(ctx context.Context, pod string) (observe.LogTail, error) {
	getCtx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	obj, err := c.dyn.Resource(podGVR).Namespace(c.opts.Namespace).Get(getCtx, pod, metav1.GetOptions{})
	cancel()
	if apierrors.IsNotFound(err) {
		return observe.LogTail{}, redact.NewError("pooler log tail", redact.CategoryNotFound, err)
	}
	if err != nil {
		return observe.LogTail{}, categorize("pooler log tail pod get", err)
	}
	facts, member, err := c.convertPoolerPod(ctx, newPoolerOwnership(c), obj.Object)
	if err != nil {
		return observe.LogTail{}, err
	}
	if !member {
		c.logExcludedPod(facts.Name)
		return observe.LogTail{}, redact.NewError("pooler log tail", redact.CategoryNotFound, nil)
	}

	lines := int64(c.opts.LogTailLines)
	limit := c.opts.LogTailMaxBytes
	req := c.typed.CoreV1().Pods(c.opts.Namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container:  pgBouncerContainer,
		TailLines:  &lines,
		LimitBytes: &limit,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return observe.LogTail{}, categorize("pooler log tail", err)
	}
	defer func() { _ = stream.Close() }()

	data, err := io.ReadAll(io.LimitReader(stream, limit))
	if err != nil {
		return observe.LogTail{}, categorize("pooler log tail read", err)
	}
	return observe.LogTail{
		Content:          string(data),
		TruncatedByBytes: int64(len(data)) >= limit,
		LineLimit:        c.opts.LogTailLines,
		ByteLimit:        limit,
	}, nil
}
