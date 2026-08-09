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
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"

	"github.com/fyannk/pgConsole/internal/observe"
)

// poolerPodObject is a pooler pod as CloudNativePG labels and owns one.
func poolerPodObject(name, pooler, replicaSet string) *unstructured.Unstructured {
	owners := []any{}
	if replicaSet != "" {
		owners = append(owners, map[string]any{
			"apiVersion": "apps/v1", "kind": "ReplicaSet",
			"name": replicaSet, "uid": "rs-" + replicaSet, "controller": true,
		})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": name, "namespace": "payments", "uid": "u-" + name,
			"labels": map[string]any{
				clusterLabel:    "orders",
				poolerRoleLabel: poolerRoleValue,
				poolerNameLabel: pooler,
			},
			"ownerReferences": owners,
		},
		"spec":   map[string]any{"nodeName": "node-a", "containers": []any{map[string]any{"name": "pgbouncer", "image": "pgbouncer:1.24"}}},
		"status": map[string]any{"phase": "Running"},
	}}
}

func ownedObject(kind, name, ownerKind, ownerName, apiVersion string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": kind,
		"metadata": map[string]any{
			"name": name, "namespace": "payments",
			"ownerReferences": []any{map[string]any{
				"apiVersion": apiVersion, "kind": ownerKind,
				"name": ownerName, "uid": "u-" + ownerName, "controller": true,
			}},
		},
	}}
}

func poolerPodScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "PodList"}, &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSetList"}, &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "DeploymentList"}, &unstructured.UnstructuredList{})
	return s
}

func poolerPodListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		podGVR:        "PodList",
		replicaSetGVR: "ReplicaSetList",
		deploymentGVR: "DeploymentList",
	}
}

// wholeChain is a correctly owned pooler pod plus the ReplicaSet and
// Deployment that prove it.
func wholeChain() []runtime.Object {
	return []runtime.Object{
		poolerPodObject("orders-rw-pooler-abc-1", "orders-rw-pooler", "orders-rw-pooler-abc"),
		ownedObject("ReplicaSet", "orders-rw-pooler-abc", "Deployment", "orders-rw-pooler", "apps/v1"),
		ownedObject("Deployment", "orders-rw-pooler", "Pooler", "orders-rw-pooler", "postgresql.cnpg.io/v1"),
	}
}

// TestPoolerPodsRequireTheWholeOwnershipChain proves the label selects
// but never decides. This is the rule AGENTS.md states outright — labels
// are a selection mechanism, never a security boundary — and a pooler
// pod is two hops from its Pooler, so it is the rule most easily lost.
func TestPoolerPodsRequireTheWholeOwnershipChain(t *testing.T) {
	t.Parallel()
	c, logs := newTestClient(t)
	c.dyn = fake.NewSimpleDynamicClientWithCustomListKinds(poolerPodScheme(), poolerPodListKinds(),
		append(wholeChain(),
			// Correctly labelled, owned by nothing at all.
			poolerPodObject("impostor-1", "orders-rw-pooler", ""),
			// Owned by a ReplicaSet that no Deployment controls.
			poolerPodObject("impostor-2", "orders-rw-pooler", "orphan-rs"),
			ownedObject("ReplicaSet", "orphan-rs", "Job", "some-job", "batch/v1"),
			// Owned through a Deployment that no Pooler controls.
			poolerPodObject("impostor-3", "orders-rw-pooler", "unowned-rs"),
			ownedObject("ReplicaSet", "unowned-rs", "Deployment", "unowned-deploy", "apps/v1"),
			ownedObject("Deployment", "unowned-deploy", "StatefulSet", "something", "apps/v1"),
		)...)

	pods, _, err := c.FetchPoolerPods(context.Background())
	if err != nil {
		t.Fatalf("FetchPoolerPods: %v", err)
	}
	if len(pods) != 1 || pods[0].Name != "orders-rw-pooler-abc-1" {
		t.Fatalf("selected %+v, want only the pod with a whole chain to a Pooler", pods)
	}
	for _, name := range []string{"impostor-1", "impostor-2", "impostor-3"} {
		if !strings.Contains(logs.String(), name) {
			t.Errorf("exclusion of %s was not logged", name)
		}
	}
}

// TestPoolerPodLabelMustMatchTheOwningPooler proves a pod cannot borrow
// another pooler's identity: the chain says which Pooler owns it, and
// the label has to agree.
func TestPoolerPodLabelMustMatchTheOwningPooler(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	c.dyn = fake.NewSimpleDynamicClientWithCustomListKinds(poolerPodScheme(), poolerPodListKinds(),
		poolerPodObject("liar-1", "orders-ro-pooler", "orders-rw-pooler-abc"),
		ownedObject("ReplicaSet", "orders-rw-pooler-abc", "Deployment", "orders-rw-pooler", "apps/v1"),
		ownedObject("Deployment", "orders-rw-pooler", "Pooler", "orders-rw-pooler", "postgresql.cnpg.io/v1"),
	)

	pods, _, err := c.FetchPoolerPods(context.Background())
	if err != nil {
		t.Fatalf("FetchPoolerPods: %v", err)
	}
	if len(pods) != 0 {
		t.Errorf("a pod labelled for one pooler but owned by another was accepted: %+v", pods)
	}
}

// TestPoolerPodsReportThePgBouncerImage proves the roster describes the
// pooler's container rather than whichever container happens to be first.
func TestPoolerPodsReportThePgBouncerImage(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	c.dyn = fake.NewSimpleDynamicClientWithCustomListKinds(poolerPodScheme(), poolerPodListKinds(), wholeChain()...)

	pods, _, err := c.FetchPoolerPods(context.Background())
	if err != nil {
		t.Fatalf("FetchPoolerPods: %v", err)
	}
	if len(pods) != 1 {
		t.Fatalf("got %d pods", len(pods))
	}
	if pods[0].Image != "pgbouncer:1.24" {
		t.Errorf("image = %q, want the pgbouncer container's", pods[0].Image)
	}
	if pods[0].Role != "orders-rw-pooler" {
		t.Errorf("role = %q, want the owning pooler's name", pods[0].Role)
	}
}

// TestTailPoolerLogsRefusesAnUnverifiedPod proves the tail re-checks
// ownership live rather than trusting the roster the page was rendered
// from. A non-member and a missing pod are the same answer to the
// caller.
func TestTailPoolerLogsRefusesAnUnverifiedPod(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	c.dyn = fake.NewSimpleDynamicClientWithCustomListKinds(poolerPodScheme(), poolerPodListKinds(),
		poolerPodObject("impostor-1", "orders-rw-pooler", ""))

	if _, err := c.TailPoolerLogs(context.Background(), "impostor-1", ""); err == nil {
		t.Fatal("a pod with no ownership chain was tailed")
	}
}

var _ observe.PoolerPodSource = (*Client)(nil)
