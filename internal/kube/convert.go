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
	"sort"
	"time"

	apiv1 "github.com/cloudnative-pg/api/pkg/api/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

// Conversion bounds. Operator-reported free text is bounded here, at
// the boundary, so no later layer can accidentally render or retain an
// unbounded message.
const (
	maxConditions       = 32
	maxConditionMessage = 1024
)

// boundOperatorMessage cuts one piece of operator free text at the same
// ceiling the conditions use. Bounding happens at this boundary so no
// later layer can accidentally render or retain an unbounded message.
func boundOperatorMessage(s string) string {
	if len(s) > maxConditionMessage {
		return s[:maxConditionMessage]
	}
	return s
}

// convertCluster converts a raw cluster object into source-neutral
// facts. Fields absent from the object — older CloudNativePG versions
// report fewer fields — stay empty or nil and render as unknown; they
// are never invented.
func convertCluster(content map[string]any) (observe.ClusterFacts, error) {
	var cluster apiv1.Cluster
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &cluster); err != nil {
		return observe.ClusterFacts{}, redact.NewError("cluster convert", redact.CategoryInternal, err)
	}

	facts := observe.ClusterFacts{
		Present:        true,
		UID:            string(cluster.UID),
		Phase:          cluster.Status.Phase,
		PhaseReason:    cluster.Status.PhaseReason,
		CurrentPrimary: cluster.Status.CurrentPrimary,
		TargetPrimary:  cluster.Status.TargetPrimary,
		Image:          cluster.Status.Image,
	}

	// The operator writes the timestamp as RFC3339 text; an unparseable
	// value stays nil rather than becoming a wrong instant.
	if raw := cluster.Status.TargetPrimaryTimestamp; raw != "" {
		if at, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			facts.TargetPrimaryTimestamp = &at
		}
	}

	if ref := cluster.Spec.ImageCatalogRef; ref != nil {
		facts.ImageCatalogRef = &observe.ImageCatalogRef{
			Kind:  ref.Kind,
			Name:  ref.Name,
			Major: ref.Major,
		}
	}

	if cluster.Spec.Instances > 0 {
		v := cluster.Spec.Instances
		facts.DesiredInstances = &v
	}
	if cluster.Status.ReadyInstances > 0 || cluster.Status.Instances > 0 {
		v := cluster.Status.ReadyInstances
		facts.ReadyInstances = &v
	}
	if cluster.Status.TimelineID > 0 {
		v := cluster.Status.TimelineID
		facts.TimelineID = &v
	}
	if info := cluster.Status.PGDataImageInfo; info != nil && info.MajorVersion > 0 {
		v := info.MajorVersion
		facts.PostgresMajorVersion = &v
	}

	for i, cond := range cluster.Status.Conditions {
		if i == maxConditions {
			break
		}
		msg := cond.Message
		if len(msg) > maxConditionMessage {
			msg = msg[:maxConditionMessage]
		}
		converted := observe.Condition{
			Type:    cond.Type,
			Status:  string(cond.Status),
			Reason:  cond.Reason,
			Message: msg,
		}
		if !cond.LastTransitionTime.IsZero() {
			at := cond.LastTransitionTime.Time.UTC()
			converted.LastTransition = &at
		}
		facts.Conditions = append(facts.Conditions, converted)
	}

	convertClusterStatusLists(&cluster, &facts)
	return facts, nil
}

// Bounds on the operator's status lists. Each is far above what a
// cluster this console watches can hold — an instance count is small
// by construction — and exists so a hostile status cannot grow the
// snapshot without limit.
const (
	maxStatusNames  = 64
	maxRoleProblems = 32
	maxRoleErrors   = 4
)

// boundNames copies at most maxStatusNames of the operator's names,
// sorted so two publications of the same state compare equal.
func boundNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(names), maxStatusNames))
	for i, name := range names {
		if i == maxStatusNames {
			break
		}
		out = append(out, boundOperatorMessage(name))
	}
	sort.Strings(out)
	return out
}

// operatorInstant parses one of the RFC3339 timestamps the operator
// writes as text; nil for an absent or unparseable value, never a
// wrong instant.
func operatorInstant(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil
	}
	at = at.UTC()
	return &at
}

// convertClusterStatusLists reads the status fields that are lists,
// maps and nested records rather than scalars: the operator's own
// classification of instances and claims, the certificates it manages,
// the roles and tablespaces it could not reconcile, and what each
// instance reported back. Every one is bounded here, and every map is
// flattened into a sorted slice so the facts compare and render the
// same way twice.
func convertClusterStatusLists(cluster *apiv1.Cluster, facts *observe.ClusterFacts) {
	status := &cluster.Status
	if status.Instances > 0 {
		v := status.Instances
		facts.ReportedInstances = &v
	}
	facts.FailedInstances = boundNames(status.InstancesStatus[apiv1.PodFailed])
	facts.UnusablePVCs = boundNames(status.UnusablePVC)
	facts.DanglingPVCs = boundNames(status.DanglingPVC)
	facts.ResizingPVCs = boundNames(status.ResizingPVC)
	facts.InitializingPVCs = boundNames(status.InitializingPVC)
	facts.PrimaryFailingSince = operatorInstant(status.CurrentPrimaryFailingSinceTimestamp)
	facts.ReplicaSwitchInProgress = status.SwitchReplicaClusterStatus.InProgress

	for secret, raw := range status.Certificates.Expirations {
		if len(facts.CertificateExpirations) == maxStatusNames {
			break
		}
		if at := operatorInstant(raw); at != nil {
			facts.CertificateExpirations = append(facts.CertificateExpirations,
				observe.CertificateExpiry{Secret: boundOperatorMessage(secret), ExpiresAt: *at})
		}
	}
	sort.Slice(facts.CertificateExpirations, func(a, b int) bool {
		return facts.CertificateExpirations[a].Secret < facts.CertificateExpirations[b].Secret
	})

	for role, errs := range status.ManagedRolesStatus.CannotReconcile {
		if len(facts.UnreconcilableRoles) == maxRoleProblems {
			break
		}
		problem := observe.RoleProblem{Role: boundOperatorMessage(role)}
		for i, msg := range errs {
			if i == maxRoleErrors {
				break
			}
			problem.Errors = append(problem.Errors, boundOperatorMessage(msg))
		}
		facts.UnreconcilableRoles = append(facts.UnreconcilableRoles, problem)
	}
	sort.Slice(facts.UnreconcilableRoles, func(a, b int) bool {
		return facts.UnreconcilableRoles[a].Role < facts.UnreconcilableRoles[b].Role
	})

	for i, ts := range status.TablespacesStatus {
		if i == maxStatusNames {
			break
		}
		facts.Tablespaces = append(facts.Tablespaces, observe.TablespaceFacts{
			Name:  boundOperatorMessage(ts.Name),
			State: string(ts.State),
			Error: boundOperatorMessage(ts.Error),
		})
	}
	sort.Slice(facts.Tablespaces, func(a, b int) bool {
		return facts.Tablespaces[a].Name < facts.Tablespaces[b].Name
	})

	for instance, reported := range status.InstancesReportedState {
		if len(facts.InstanceTimelines) == maxStatusNames {
			break
		}
		facts.InstanceTimelines = append(facts.InstanceTimelines, observe.InstanceTimeline{
			Instance:   boundOperatorMessage(string(instance)),
			TimelineID: reported.TimeLineID,
			IsPrimary:  reported.IsPrimary,
		})
	}
	sort.Slice(facts.InstanceTimelines, func(a, b int) bool {
		return facts.InstanceTimelines[a].Instance < facts.InstanceTimelines[b].Instance
	})
}
