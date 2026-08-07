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
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic/fake"

	"github.com/fyannk/pgConsole/internal/observe"
)

func rawPooler(name, cluster, poolerType string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Pooler",
		"metadata": map[string]any{
			"name": name, "namespace": "payments", "uid": "u-" + name,
			"creationTimestamp": "2026-07-28T09:00:00Z",
		},
		"spec": map[string]any{
			"cluster":   map[string]any{"name": cluster},
			"type":      poolerType,
			"instances": int64(2),
			"pgbouncer": map[string]any{"poolMode": "transaction"},
		},
		"status": map[string]any{
			"instances":   int64(2),
			"phase":       "active",
			"phaseReason": "the pooler is running",
			"image":       "ghcr.io/cloudnative-pg/pgbouncer:1.24",
		},
	}}
}

func poolerScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "postgresql.cnpg.io", Version: "v1", Kind: "PoolerList",
	}, &unstructured.UnstructuredList{})
	return s
}

// TestConvertPoolerRequiresTheExactClusterReference proves selection is
// the spec.cluster.name reference and not the namespace. The watch is
// namespace-scoped because RBAC cannot pin one by name, so another
// cluster's pooler is expected traffic that must not be adopted.
func TestConvertPoolerRequiresTheExactClusterReference(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)

	facts, member, err := c.convertPooler(rawPooler("orders-rw", "orders", "rw").Object)
	if err != nil || !member {
		t.Fatalf("target Pooler conversion: member=%v err=%v", member, err)
	}
	if facts.Name != "orders-rw" || facts.Type != "rw" || facts.PoolMode != "transaction" {
		t.Errorf("facts = %+v, want the reported name, type and pool mode", facts)
	}
	if facts.Phase != "active" || facts.ReadyInstances != 2 {
		t.Errorf("facts = %+v, want the operator-reported phase and instance count", facts)
	}
	if facts.DesiredInstances == nil || *facts.DesiredInstances != 2 {
		t.Errorf("desired instances = %v, want the requested count", facts.DesiredInstances)
	}

	if _, member, err := c.convertPooler(rawPooler("other-rw", "other", "rw").Object); err != nil || member {
		t.Errorf("a pooler of another cluster was adopted: member=%v err=%v", member, err)
	}
}

// TestConvertPoolerDoesNotReadSecretReferences proves the credential
// wiring never crosses the adapter boundary. The console holds no Secret
// permission, and a pooler's Secret names are not review data.
func TestConvertPoolerDoesNotReadSecretReferences(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	raw := rawPooler("orders-rw", "orders", "rw")
	status, _ := raw.Object["status"].(map[string]any)
	status["secrets"] = map[string]any{
		"pgBouncerSecrets": map[string]any{
			"authQuery": map[string]any{"name": "orders-pooler-auth-canary"},
		},
	}

	facts, member, err := c.convertPooler(raw.Object)
	if err != nil || !member {
		t.Fatalf("conversion: member=%v err=%v", member, err)
	}
	for _, field := range []string{facts.Name, facts.Type, facts.PoolMode, facts.Phase, facts.PhaseReason, facts.Image} {
		if strings.Contains(field, "canary") {
			t.Fatalf("a Secret reference reached the facts: %q", field)
		}
	}
}

// TestFetchPoolersSelectsOnlyTheTargetCluster proves the listing keeps
// what the reference selects and nothing else.
func TestFetchPoolersSelectsOnlyTheTargetCluster(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	c.dyn = fake.NewSimpleDynamicClientWithCustomListKinds(poolerScheme(),
		map[schema.GroupVersionResource]string{poolerGVR: "PoolerList"},
		rawPooler("orders-rw", "orders", "rw"),
		rawPooler("orders-ro", "orders", "ro"),
		rawPooler("other-rw", "other", "rw"),
	)

	poolers, _, truncated, err := c.FetchPoolers(context.Background())
	if err != nil {
		t.Fatalf("FetchPoolers: %v", err)
	}
	if truncated {
		t.Error("three poolers reported as truncated")
	}
	if len(poolers) != 2 {
		t.Fatalf("selected %d poolers, want the two referencing orders", len(poolers))
	}
	for _, p := range poolers {
		if p.Name == "other-rw" {
			t.Error("a pooler of another cluster was selected")
		}
	}
}

// TestPumpPoolerSkipsOtherClustersWithoutEndingTheStream proves a
// non-member is skipped rather than treated as a reason to re-seed. A
// namespace-scoped watch delivers other clusters' poolers constantly;
// tearing the watch down for each one would never converge.
func TestPumpPoolerSkipsOtherClustersWithoutEndingTheStream(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)

	_, ok, fatal := c.pumpPooler(watch.Event{Type: watch.Added, Object: rawPooler("other-rw", "other", "rw")})
	if ok || fatal {
		t.Errorf("another cluster's pooler: ok=%v fatal=%v, want skipped and not fatal", ok, fatal)
	}

	change, ok, fatal := c.pumpPooler(watch.Event{Type: watch.Added, Object: rawPooler("orders-rw", "orders", "rw")})
	if !ok || fatal || change.Put == nil {
		t.Fatalf("target pooler: ok=%v fatal=%v change=%+v", ok, fatal, change)
	}

	change, ok, fatal = c.pumpPooler(watch.Event{Type: watch.Deleted, Object: rawPooler("orders-rw", "orders", "rw")})
	if !ok || fatal || change.Delete == nil || change.Delete.Name != "orders-rw" {
		t.Fatalf("deletion: ok=%v fatal=%v change=%+v", ok, fatal, change)
	}

	if _, ok, fatal := c.pumpPooler(watch.Event{Type: watch.Bookmark, Object: rawPooler("orders-rw", "orders", "rw")}); ok || fatal {
		t.Errorf("bookmark: ok=%v fatal=%v, want skipped and not fatal", ok, fatal)
	}
}

// TestConvertPoolerBoundsTheOperatorPhaseReason proves free text is cut
// at the adapter boundary, so no later layer can render or retain an
// unbounded operator message.
func TestConvertPoolerBoundsTheOperatorPhaseReason(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	raw := rawPooler("orders-rw", "orders", "rw")
	status, _ := raw.Object["status"].(map[string]any)
	status["phaseReason"] = strings.Repeat("x", maxConditionMessage*3)

	facts, _, err := c.convertPooler(raw.Object)
	if err != nil {
		t.Fatalf("convertPooler: %v", err)
	}
	if len(facts.PhaseReason) != maxConditionMessage {
		t.Errorf("phase reason length %d, want it cut at %d", len(facts.PhaseReason), maxConditionMessage)
	}
}

var _ observe.PoolerSource = (*Client)(nil)
