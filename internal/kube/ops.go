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
	"encoding/json"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// The pinned CloudNativePG interaction constants. These reproduce the
// exact operations the kubectl cnpg plugin performs at CloudNativePG
// release-1.30 (verified against the plugin source: restart/reload set
// these annotations, promote patches the status subresource). A minor
// version bump re-verifies them at the operations entry gate.
const (
	// reloadAnnotation triggers a configuration reload.
	reloadAnnotation = "cnpg.io/reloadedAt"
	// restartAnnotation triggers a rolling restart.
	restartAnnotation = "kubectl.kubernetes.io/restartedAt"
	// switchoverPhase is the status phase promote sets.
	switchoverPhase = "Switchover in progress"
)

// cnpgTimestamp is the microsecond RFC3339 format the plugin's reload
// and promote use; restart uses plain RFC3339. Any fresh value triggers
// the operator, but the format is pinned for byte-fidelity to the
// plugin.
const cnpgTimestamp = "2006-01-02T15:04:05.000000Z07:00"

// reloadPatch builds the exact reload merge patch.
func reloadPatch(at time.Time) []byte {
	return annotationPatch(reloadAnnotation, at.UTC().Format(cnpgTimestamp))
}

// restartPatch builds the exact rolling-restart merge patch.
func restartPatch(at time.Time) []byte {
	return annotationPatch(restartAnnotation, at.UTC().Format(time.RFC3339))
}

// annotationPatch builds a minimal merge patch adding one annotation.
// A merge patch adds the key without disturbing other annotations.
func annotationPatch(key, value string) []byte {
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{key: value},
		},
	}
	out, _ := json.Marshal(patch)
	return out
}

// promotePatch builds the exact promote status merge patch.
func promotePatch(instance string, at time.Time) []byte {
	patch := map[string]any{
		"status": map[string]any{
			"targetPrimary":          instance,
			"targetPrimaryTimestamp": at.UTC().Format(cnpgTimestamp),
			"phase":                  switchoverPhase,
			"phaseReason":            "Switching over to " + instance,
		},
	}
	out, _ := json.Marshal(patch)
	return out
}

// CreateBackup creates one Backup resource referencing the target
// cluster. It is the on-demand backup operation.
func (c *Client) CreateBackup(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Backup",
		"metadata":   map[string]any{"name": name, "namespace": c.opts.Namespace},
		"spec":       map[string]any{"cluster": map[string]any{"name": c.opts.ClusterName}},
	}}
	_, err := c.dyn.Resource(backupGVR).Namespace(c.opts.Namespace).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return categorize("backup create", err)
	}
	return nil
}

// ReloadCluster triggers a configuration reload.
func (c *Client) ReloadCluster(ctx context.Context, at time.Time) error {
	return c.patchCluster(ctx, "cluster reload", reloadPatch(at))
}

// RestartCluster triggers a rolling restart of the cluster.
func (c *Client) RestartCluster(ctx context.Context, at time.Time) error {
	return c.patchCluster(ctx, "cluster restart", restartPatch(at))
}

// PromoteInstance requests a switchover to the named instance through
// the status subresource.
func (c *Client) PromoteInstance(ctx context.Context, instance string, at time.Time) error {
	return c.patchCluster(ctx, "cluster promote", promotePatch(instance, at), "status")
}

// patchCluster applies one pinned merge patch to the target cluster,
// optionally to a subresource. It is the only patch path; the patch
// bytes are built by the pinned interaction functions above.
func (c *Client) patchCluster(ctx context.Context, op string, patch []byte, subresources ...string) error {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	_, err := c.dyn.Resource(clusterGVR).Namespace(c.opts.Namespace).Patch(
		ctx, c.opts.ClusterName, types.MergePatchType, patch, metav1.PatchOptions{}, subresources...)
	if err != nil {
		return categorize(op, err)
	}
	return nil
}

// WriteAccessRequestStatus records a reviewer's decision on the named
// PgToolBoxAccessRequest by merge-patching only its status subresource:
// the state, the chosen role (approvals only), and the reviewer identity
// and time. It never creates or modifies users, roles, or spec — the
// operator's controller materializes the PgToolBoxUser after approval.
func (c *Client) WriteAccessRequestStatus(ctx context.Context, name, state, roleName, decidedBy string, decidedAt time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	status := map[string]any{
		"state":     state,
		"decidedBy": decidedBy,
		"decidedAt": decidedAt.UTC().Format(time.RFC3339),
	}
	if roleName != "" {
		status["requestedRoleRef"] = map[string]any{"name": roleName}
	}
	patch, _ := json.Marshal(map[string]any{"status": status})
	_, err := c.dyn.Resource(accessRequestGVR).Namespace(c.opts.Namespace).Patch(
		ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}, "status")
	if err != nil {
		return categorize("access request decide", err)
	}
	return nil
}
