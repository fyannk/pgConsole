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
	"time"

	batchv1 "k8s.io/api/batch/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

// The cluster's remaining children: the Secrets holding its
// certificates and credentials, its ConfigMaps, its disruption budgets,
// the ServiceAccount/Role/RoleBinding triple the operator creates for
// it, and its bootstrap Jobs. Membership is the same controller
// owner-reference proof the pods, services and claims use.
//
// Two rules set these kinds apart from the rest of the adapter:
//
//   - A Secret is reduced to metadata on sight — its name, its type and
//     how many keys it holds. No key name and no value is ever copied
//     out of the API response.
//   - None of these kinds is recorded into the history journal. The
//     journal redacts what it knows about; rather than teach it to
//     redact Secret payloads, the whole set stays out of it. The
//     children are a live drawing, not a timeline.
//
// Every grant here is optional: a kind whose list is refused is
// reported as unobserved — "not granted" and "none exist" stay
// different claims — and the sweep carries on.
var childKinds = []struct {
	kind string
	gvr  schema.GroupVersionResource
	op   string
}{
	{"Secret", schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, "secrets"},
	{"ConfigMap", schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, "configmaps"},
	{"PodDisruptionBudget", schema.GroupVersionResource{
		Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}, "disruption budgets"},
	{"ServiceAccount", schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}, "service accounts"},
	{"Role", schema.GroupVersionResource{
		Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}, "roles"},
	{"RoleBinding", schema.GroupVersionResource{
		Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}, "role bindings"},
	{"Job", schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, "jobs"},
}

// fetchChildren lists every optional child kind into the state.
func (c *Client) fetchChildren(ctx context.Context, state *observe.InfrastructureState) error {
	state.ChildResourceVersions = map[string]string{}
	for _, spec := range childKinds {
		convert := c.childConverter(spec.kind)
		facts, rv, truncated, err := listOwned(ctx, c, spec.gvr, spec.op, false, convert)
		switch {
		case err == nil:
			state.Children = append(state.Children, facts...)
			state.ChildResourceVersions[spec.kind] = rv
			state.Truncated = state.Truncated || truncated
		case redact.Categorize(err) == redact.CategoryNotFound,
			redact.Categorize(err) == redact.CategoryForbidden:
			state.ChildrenUnobserved = append(state.ChildrenUnobserved, spec.kind)
			c.logChildUnobservable(spec.op, err)
		default:
			return err
		}
	}
	return nil
}

// childConverter picks the reducer for one kind. Each reducer returns
// the facts and the same owner-reference membership proof.
func (c *Client) childConverter(kind string) func(map[string]any) (observe.ChildFacts, bool, error) {
	switch kind {
	case "Secret":
		return c.convertSecret
	case "ConfigMap":
		return c.convertConfigMap
	case "PodDisruptionBudget":
		return c.convertDisruptionBudget
	case "ServiceAccount":
		return c.convertServiceAccount
	case "Role":
		return c.convertRole
	case "RoleBinding":
		return c.convertRoleBinding
	default:
		return c.convertJob
	}
}

// convertSecret keeps a Secret's metadata and nothing else. The raw
// content is read only for its meta fields, its type and its key count;
// the data map is never iterated, converted or copied.
func (c *Client) convertSecret(content map[string]any) (observe.ChildFacts, bool, error) {
	object := unstructured.Unstructured{Object: content}
	facts := childMeta("Secret", &object)
	facts.SecretType, _, _ = unstructured.NestedString(content, "type")
	if data, ok := content["data"].(map[string]any); ok {
		keys := len(data)
		facts.Keys = &keys
	}
	return facts, c.ownedRefs(object.GetOwnerReferences()), nil
}

// convertConfigMap keeps a ConfigMap's name and entry count.
func (c *Client) convertConfigMap(content map[string]any) (observe.ChildFacts, bool, error) {
	object := unstructured.Unstructured{Object: content}
	facts := childMeta("ConfigMap", &object)
	keys := 0
	if data, ok := content["data"].(map[string]any); ok {
		keys += len(data)
	}
	if data, ok := content["binaryData"].(map[string]any); ok {
		keys += len(data)
	}
	facts.Keys = &keys
	return facts, c.ownedRefs(object.GetOwnerReferences()), nil
}

// convertDisruptionBudget keeps the declared constraint and the
// reported headroom.
func (c *Client) convertDisruptionBudget(content map[string]any) (observe.ChildFacts, bool, error) {
	var pdb policyv1.PodDisruptionBudget
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &pdb); err != nil {
		return observe.ChildFacts{}, false, redact.NewError("disruption budget convert", redact.CategoryInternal, err)
	}
	facts := observe.ChildFacts{Kind: "PodDisruptionBudget", Name: pdb.Name, UID: string(pdb.UID)}
	setCreated(&facts, pdb.CreationTimestamp)
	if pdb.Spec.MinAvailable != nil {
		facts.MinAvailable = pdb.Spec.MinAvailable.String()
	}
	if pdb.Spec.MaxUnavailable != nil {
		facts.MaxUnavailable = pdb.Spec.MaxUnavailable.String()
	}
	allowed := pdb.Status.DisruptionsAllowed
	facts.DisruptionsAllowed = &allowed
	return facts, c.ownedByCluster(&pdb.ObjectMeta), nil
}

// convertServiceAccount keeps only the identity.
func (c *Client) convertServiceAccount(content map[string]any) (observe.ChildFacts, bool, error) {
	object := unstructured.Unstructured{Object: content}
	facts := childMeta("ServiceAccount", &object)
	return facts, c.ownedRefs(object.GetOwnerReferences()), nil
}

// convertRole keeps the rule count.
func (c *Client) convertRole(content map[string]any) (observe.ChildFacts, bool, error) {
	var role rbacv1.Role
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &role); err != nil {
		return observe.ChildFacts{}, false, redact.NewError("role convert", redact.CategoryInternal, err)
	}
	facts := observe.ChildFacts{Kind: "Role", Name: role.Name, UID: string(role.UID)}
	setCreated(&facts, role.CreationTimestamp)
	rules := len(role.Rules)
	facts.Rules = &rules
	return facts, c.ownedByCluster(&role.ObjectMeta), nil
}

// convertRoleBinding keeps the granted role and the subject count.
func (c *Client) convertRoleBinding(content map[string]any) (observe.ChildFacts, bool, error) {
	var binding rbacv1.RoleBinding
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &binding); err != nil {
		return observe.ChildFacts{}, false, redact.NewError("role binding convert", redact.CategoryInternal, err)
	}
	facts := observe.ChildFacts{Kind: "RoleBinding", Name: binding.Name, UID: string(binding.UID)}
	setCreated(&facts, binding.CreationTimestamp)
	facts.RoleRef = binding.RoleRef.Name
	subjects := len(binding.Subjects)
	facts.Subjects = &subjects
	return facts, c.ownedByCluster(&binding.ObjectMeta), nil
}

// convertJob keeps the reported pod counts.
func (c *Client) convertJob(content map[string]any) (observe.ChildFacts, bool, error) {
	var job batchv1.Job
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &job); err != nil {
		return observe.ChildFacts{}, false, redact.NewError("job convert", redact.CategoryInternal, err)
	}
	facts := observe.ChildFacts{Kind: "Job", Name: job.Name, UID: string(job.UID)}
	setCreated(&facts, job.CreationTimestamp)
	active, succeeded, failed := job.Status.Active, job.Status.Succeeded, job.Status.Failed
	facts.Active, facts.Succeeded, facts.Failed = &active, &succeeded, &failed
	// The operator injects its own image as this init container, which
	// makes a bootstrap Job the operator-version source of last resort
	// for a cluster that never got an instance pod.
	for _, container := range job.Spec.Template.Spec.InitContainers {
		if container.Name == "bootstrap-controller" {
			facts.BootstrapImage = container.Image
			break
		}
	}
	return facts, c.ownedByCluster(&job.ObjectMeta), nil
}

// childMeta reduces the shared identity fields of an unstructured child.
func childMeta(kind string, object *unstructured.Unstructured) observe.ChildFacts {
	facts := observe.ChildFacts{Kind: kind, Name: object.GetName(), UID: string(object.GetUID())}
	setCreated(&facts, object.GetCreationTimestamp())
	return facts
}

// setCreated keeps a non-zero creation time.
func setCreated(facts *observe.ChildFacts, stamp metav1.Time) {
	if stamp.IsZero() {
		return
	}
	at := stamp.Time.UTC().Truncate(time.Second)
	facts.CreatedAt = &at
}

// childPump reduces one kind's watch events. Not tapped: these kinds
// stay out of the history journal by design.
func (c *Client) childPump(kind string) pump[observe.InfrastructureChange] {
	convert := c.childConverter(kind)
	return func(event watch.Event) (observe.InfrastructureChange, bool, bool) {
		return infrastructurePump(event, kind, convert,
			func(f observe.ChildFacts) observe.InfrastructureChange {
				return observe.InfrastructureChange{Child: &f}
			},
			func(f observe.ChildFacts) (string, string) { return f.Name, f.UID })
	}
}

func (c *Client) logChildUnobservable(op string, err error) {
	c.logger.Info(op+" unobservable",
		slog.String("category", redact.Safe(err)))
}
