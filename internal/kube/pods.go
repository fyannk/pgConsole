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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

// podGVR is the core pods resource.
var podGVR = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

// clusterLabel is the CloudNativePG label selecting a cluster's
// instance pods. Labels are the selection mechanism, never a security
// boundary — the namespace-scoped Role is the boundary.
const clusterLabel = "cnpg.io/cluster"

// roleLabel is the CloudNativePG instance role label.
const roleLabel = "cnpg.io/instanceRole"

// postgresContainer is the CNPG instance container name whose image is
// reported.
const postgresContainer = "postgres"

// podListPageSize bounds a single list page.
const podListPageSize = 200

// FetchPods lists the cluster's member pods through the label selector
// and membership verification. The result is bounded by observe.MaxPods;
// the store flags any truncation.
func (c *Client) FetchPods(ctx context.Context) ([]observe.PodFacts, string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	var pods []observe.PodFacts
	opts := metav1.ListOptions{
		LabelSelector: clusterLabel + "=" + c.opts.ClusterName,
		Limit:         podListPageSize,
	}
	rv := ""
	seed := c.seedRecord(scopePods)
	complete := false
	for {
		list, err := c.dyn.Resource(podGVR).Namespace(c.opts.Namespace).List(ctx, opts)
		if err != nil {
			return nil, "", categorize("pods list", err)
		}
		rv = list.GetResourceVersion()
		for i := range list.Items {
			facts, member, err := c.convertPod(list.Items[i].Object)
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
		if list.GetContinue() == "" {
			complete = true
			break
		}
		if len(pods) > observe.MaxPods {
			break
		}
		opts.Continue = list.GetContinue()
	}
	seed.commit(complete)
	return pods, rv, nil
}

// WatchPods starts a label-selected watch on the cluster's pods.
func (c *Client) WatchPods(ctx context.Context, fromResourceVersion string) (observe.PodWatch, error) {
	w, err := c.dyn.Resource(podGVR).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{
		LabelSelector:   clusterLabel + "=" + c.opts.ClusterName,
		ResourceVersion: fromResourceVersion,
	})
	if err != nil {
		return nil, categorize("pods watch", err)
	}
	items, stop := fanIn(ctx,
		[]watch.Interface{w},
		[]pump[observe.PodEvent]{tap(c, scopePods, c.pumpPod)})
	return eventStream[observe.PodEvent]{stream[observe.PodEvent]{items: items, stop: stop}}, nil
}

// pumpPod converts one pod watch event. A pod that fails membership
// verification is excluded and logged; its deletion is still forwarded,
// so a formerly excluded name can never linger in the retained set.
func (c *Client) pumpPod(event watch.Event) (observe.PodEvent, bool, bool) {
	obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
	if !ok {
		// A non-object carried by anything but an error event is not
		// itself a reason to re-seed.
		return observe.PodEvent{}, false, event.Type == watch.Error
	}
	facts, member, err := c.convertPod(obj.UnstructuredContent())
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
		return observe.PodEvent{Delete: &observe.PodDeletion{Name: facts.Name, UID: facts.UID}}, true, false
	case watch.Bookmark:
		return observe.PodEvent{}, false, false
	default:
		return observe.PodEvent{}, false, true
	}
}

// convertPod converts a raw pod into facts plus its membership verdict.
// Membership requires the controller owner reference to point at the
// configured CloudNativePG Cluster: the label already selected the pod,
// but labels are freely settable, so ownership is verified before the
// pod is presented as a cluster member.
func (c *Client) convertPod(content map[string]any) (observe.PodFacts, bool, error) {
	var pod corev1.Pod
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &pod); err != nil {
		return observe.PodFacts{}, false, redact.NewError("pod convert", redact.CategoryInternal, err)
	}

	facts := observe.PodFacts{
		Name:     pod.Name,
		UID:      string(pod.UID),
		Role:     pod.Labels[roleLabel],
		Phase:    string(pod.Status.Phase),
		Node:     pod.Spec.NodeName,
		IP:       pod.Status.PodIP,
		Deleting: pod.DeletionTimestamp != nil,
	}

	if pod.Status.StartTime != nil {
		started := pod.Status.StartTime.Time
		facts.Started = &started
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			ready := cond.Status == corev1.ConditionTrue
			facts.Ready = &ready
			break
		}
	}
	facts.Containers = convertContainers(&pod)
	for _, container := range facts.Containers {
		if container.Name == postgresContainer && !container.Init {
			facts.Restarts = container.Restarts
			facts.Image = container.Image
			break
		}
	}
	// A pod with no container named postgres is not one this console
	// expects, but membership has already been proven, so it is shown
	// rather than hidden. The first regular container's image is the
	// least misleading stand-in available.
	if facts.Image == "" {
		for _, container := range facts.Containers {
			if !container.Init {
				facts.Image = container.Image
				break
			}
		}
	}

	return facts, c.podIsMember(&pod), nil
}

// convertContainers pairs each declared container with the kubelet's
// status for it, init containers first and each group in spec order.
//
// The spec is the source of the list, not the status: a container that
// has never started has no status at all, and dropping it would hide
// exactly the pod that cannot start. Such a container reports its name
// and image with an empty state, which renders as unknown rather than as
// healthy.
func convertContainers(pod *corev1.Pod) []observe.ContainerFacts {
	total := len(pod.Spec.InitContainers) + len(pod.Spec.Containers)
	if total == 0 {
		return nil
	}
	status := make(map[string]corev1.ContainerStatus, total)
	for _, cs := range pod.Status.InitContainerStatuses {
		status["init/"+cs.Name] = cs
	}
	for _, cs := range pod.Status.ContainerStatuses {
		status["run/"+cs.Name] = cs
	}

	facts := make([]observe.ContainerFacts, 0, total)
	add := func(spec corev1.Container, init bool) {
		key := "run/"
		if init {
			key = "init/"
		}
		container := observe.ContainerFacts{Name: spec.Name, Image: spec.Image, Init: init}
		if cs, ok := status[key+spec.Name]; ok {
			ready := cs.Ready
			restarts := int(cs.RestartCount)
			container.Ready = &ready
			container.Restarts = &restarts
			container.State, container.Reason, container.ExitCode = containerState(cs.State)
		}
		facts = append(facts, container)
	}
	for _, spec := range pod.Spec.InitContainers {
		add(spec, true)
	}
	for _, spec := range pod.Spec.Containers {
		add(spec, false)
	}
	return facts
}

// containerState reduces the kubelet's three-way state union to a state
// name, its machine reason, and an exit code where one exists. A running
// container has no reason: the reason field exists to name a fault, and
// filling it for a healthy container would make "has a reason" useless
// as a signal.
func containerState(state corev1.ContainerState) (name, reason string, exitCode *int32) {
	switch {
	case state.Running != nil:
		return "running", "", nil
	case state.Waiting != nil:
		return "waiting", state.Waiting.Reason, nil
	case state.Terminated != nil:
		code := state.Terminated.ExitCode
		return "terminated", state.Terminated.Reason, &code
	default:
		return "", "", nil
	}
}

// podIsMember verifies the controller owner reference names the
// configured Cluster in the CloudNativePG group.
func (c *Client) podIsMember(pod *corev1.Pod) bool {
	ref := metav1.GetControllerOf(pod)
	if ref == nil {
		return false
	}
	group, _, ok := splitAPIVersion(ref.APIVersion)
	if !ok || group != clusterGVR.Group {
		return false
	}
	return ref.Kind == "Cluster" && ref.Name == c.opts.ClusterName
}

// splitAPIVersion splits "group/version"; a core "version" has no group.
func splitAPIVersion(apiVersion string) (group, version string, ok bool) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return "", "", false
	}
	return gv.Group, gv.Version, true
}

// logExcludedPod records a membership exclusion with a stable reason.
func (c *Client) logExcludedPod(name string) {
	c.logger.Info("pod excluded",
		slog.String("reason", "membership"),
		slog.String("pod", name))
}

// logExcludedContainer records a log tail refused because the pod does
// not declare the container. The pod was a proven member, so this is a
// bad request rather than a boundary breach; it is logged because a
// legitimate reader hitting it means the page offered a container the
// pod no longer has.
func (c *Client) logExcludedContainer(pod, container string) {
	c.logger.Info("log tail container excluded",
		slog.String("reason", "not declared by the pod"),
		slog.String("pod", pod),
		slog.String("container", container))
}
