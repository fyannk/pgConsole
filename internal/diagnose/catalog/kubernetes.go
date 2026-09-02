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

package catalog

import "github.com/fyannk/pgConsole/internal/diagnose"

// kubernetesRules are the claims about Kubernetes itself: the kubelet
// and scheduler failures that keep a member pod from running, in the
// gaps the hand-written detectors leave. Quota refusals, unschedulable
// pods, image pulls and unbound volumes already have detectors; what is
// here is the rest of the ways a pod sits dead while every CloudNativePG
// status looks fine.
//
// The event reasons and container-state reasons are Kubernetes API
// conventions, stable across the versions this console meets, so these
// rules are unpinned — matching the barman file's reasoning, not an
// oversight. The one pinned rule rides on the observed server version,
// polled from the API server's own /version endpoint.
//
// NetworkPolicy problems have no rule here on purpose: a policy that
// drops traffic produces no event, no condition, and no state — only
// timeouts elsewhere. Its observable symptoms in this console are
// CloudNativePG's: the status-unreachable phase check and the
// instance-isolation log check.
func kubernetesRules() []diagnose.Rule {
	return []diagnose.Rule{
		{
			ID:        "k8s-volume-mount-failed",
			Component: diagnose.ComponentKubernetes,
			Severity:  diagnose.SeverityCritical,
			Describes: "a pod that cannot mount or attach one of its volumes",
			Summary:   "A member pod cannot mount or attach a volume, so it cannot start.",
			Detail: "The kubelet's message below names the piece that is missing — most " +
				"often a Secret or ConfigMap a projected volume references, or a " +
				"volume the storage driver cannot attach to the node.",
			When: diagnose.EventMatch{Reasons: []string{
				"FailedMount", "FailedAttachVolume", "FailedMapVolume"}},
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
		{
			ID:        "k8s-pod-evicted",
			Component: diagnose.ComponentKubernetes,
			Severity:  diagnose.SeverityWarning,
			Describes: "a member pod evicted from its node",
			Summary:   "A member pod was evicted from its node.",
			Detail: "Eviction is the kubelet reclaiming resources under pressure; the " +
				"message names which resource ran short. The operator replaces the " +
				"pod, but repeated evictions mean the node is undersized for what is " +
				"scheduled onto it.",
			When:      diagnose.EventMatch{Reasons: []string{"Evicted"}},
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
		{
			ID:        "k8s-container-crashloop",
			Component: diagnose.ComponentKubernetes,
			Severity:  diagnose.SeverityCritical,
			Describes: "a container in CrashLoopBackOff",
			Summary:   "A container is crash-looping: it keeps exiting and the kubelet is backing off restarting it.",
			Detail: "A crash-looping postgres shows its cause in the log checks; this " +
				"check also catches every other container — a plugin sidecar or a " +
				"pooler crash-looping breaks backups or connections while the " +
				"instance itself reads healthy.",
			When: diagnose.ContainerState{Reasons: []string{"CrashLoopBackOff"}},
			NextSteps: "Read that container's log tail: the last lines before each exit " +
				"name the reason the kubelet cannot.",
			// Every cause here is a fact about one pod — its log, its own
			// container states — so the relation holds only on that pod.
			ConsequenceOf: []diagnose.Relation{
				{Cause: "cnpg-wal-disk-full", Scope: diagnose.ScopePod},
				{Cause: "cnpg-postgres-exited", Scope: diagnose.ScopePod},
				{Cause: "k8s-container-oom", Scope: diagnose.ScopePod},
			},
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
		{
			ID:        "k8s-container-oom",
			Component: diagnose.ComponentKubernetes,
			Severity:  diagnose.SeverityCritical,
			Describes: "a container the kernel killed for exceeding its memory limit",
			Summary:   "A container was killed for exceeding its memory limit.",
			Detail: "OOMKilled is visible only while the termination state is current, " +
				"so one sighting is one kill, not a count. For postgres it usually " +
				"means the memory limit and the configured shared buffers plus work " +
				"memory do not fit together.",
			When: diagnose.ContainerState{Reasons: []string{"OOMKilled"}},
			NextSteps: "Raise the memory limit, or shrink what PostgreSQL is configured " +
				"to use — shared buffers and per-connection work memory are the " +
				"usual sum that no longer fits.",
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
		{
			ID:        "k8s-container-config-error",
			Component: diagnose.ComponentKubernetes,
			Severity:  diagnose.SeverityCritical,
			Describes: "a container the kubelet cannot construct",
			Summary:   "A container cannot be created, so its pod is stuck before starting.",
			Detail: "CreateContainerConfigError is almost always a reference that " +
				"resolves to nothing: a Secret or ConfigMap named in the container's " +
				"environment that does not exist.",
			When: diagnose.ContainerState{Reasons: []string{
				"CreateContainerConfigError", "CreateContainerError"}},
			Link:      "/cluster/pods",
			LinkLabel: "Pods",
		},
		{
			// The version-only rule, mirroring postgres-eol: the observed
			// server version is the finding. Kubernetes minors receive
			// patches for roughly fourteen months; 1.33, the last minor
			// before 1.34, left support in June 2026. Console-pinned
			// knowledge, current as of this console release.
			ID:        "k8s-eol",
			Component: diagnose.ComponentKubernetes,
			Requires: []diagnose.Requirement{
				{Component: diagnose.ComponentKubernetes, Constraint: "<1.34"}},
			Severity:  diagnose.SeverityWarning,
			Describes: "a Kubernetes version past upstream end of life",
			Summary:   "The Kubernetes server version no longer receives upstream patches.",
			Detail: "Versions before 1.34 left the Kubernetes support window by June " +
				"2026, so bug and security fixes are no longer published for them. " +
				"The boundary is console-pinned knowledge, current as of this console " +
				"release; the version it is applied to is quoted below.",
			Link:      "/",
			LinkLabel: "Overview",
		},
	}
}
