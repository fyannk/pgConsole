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
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/fyannk/pgConsole/internal/redact"
)

// fullCluster is an unstructured cluster with every consumed field set.
func fullCluster() map[string]any {
	return map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name":            "orders",
			"namespace":       "payments",
			"uid":             "2f12b7d1-7e8d-4c37-a68f-233efc5f3191",
			"resourceVersion": "42",
		},
		"spec": map[string]any{
			"instances": int64(3),
		},
		"status": map[string]any{
			"phase":          "Cluster in healthy state",
			"phaseReason":    "healthy",
			"currentPrimary": "orders-1",
			"targetPrimary":  "orders-1",
			"readyInstances": int64(3),
			"instances":      int64(3),
			"timelineID":     int64(4),
			"image":          "ghcr.io/cloudnative-pg/postgresql:16.4",
			"pgDataImageInfo": map[string]any{
				"image":        "ghcr.io/cloudnative-pg/postgresql:16.4",
				"majorVersion": int64(16),
			},
			"conditions": []any{
				map[string]any{
					"type":               "Ready",
					"status":             "True",
					"reason":             "ClusterIsReady",
					"message":            "Cluster is Ready",
					"lastTransitionTime": "2026-07-28T10:00:00Z",
				},
			},
		},
	}
}

func TestConvertClusterFullObject(t *testing.T) {
	t.Parallel()
	facts, err := convertCluster(fullCluster())
	if err != nil {
		t.Fatalf("convertCluster: %v", err)
	}
	if !facts.Present {
		t.Fatal("converted cluster not present")
	}
	if facts.Phase != "Cluster in healthy state" || facts.CurrentPrimary != "orders-1" {
		t.Errorf("core fields wrong: %+v", facts)
	}
	if facts.UID != "2f12b7d1-7e8d-4c37-a68f-233efc5f3191" {
		t.Errorf("UID not retained for correlation: %q", facts.UID)
	}
	if facts.DesiredInstances == nil || *facts.DesiredInstances != 3 {
		t.Error("DesiredInstances not converted")
	}
	if facts.ReadyInstances == nil || *facts.ReadyInstances != 3 {
		t.Error("ReadyInstances not converted")
	}
	if facts.TimelineID == nil || *facts.TimelineID != 4 {
		t.Error("TimelineID not converted")
	}
	if facts.PostgresMajorVersion == nil || *facts.PostgresMajorVersion != 16 {
		t.Error("PostgresMajorVersion not converted")
	}
	if len(facts.Conditions) != 1 || facts.Conditions[0].Reason != "ClusterIsReady" {
		t.Errorf("conditions wrong: %+v", facts.Conditions)
	}
}

// TestConvertClusterMinimalObject proves fields absent in older
// CloudNativePG versions stay unknown rather than becoming values.
func TestConvertClusterMinimalObject(t *testing.T) {
	t.Parallel()
	facts, err := convertCluster(map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": "orders", "namespace": "payments"},
		"spec":       map[string]any{"instances": int64(1)},
		"status":     map[string]any{"phase": "Setting up primary"},
	})
	if err != nil {
		t.Fatalf("convertCluster: %v", err)
	}
	if facts.PhaseReason != "" || facts.CurrentPrimary != "" || facts.Image != "" {
		t.Errorf("absent string fields must stay empty: %+v", facts)
	}
	if facts.ReadyInstances != nil || facts.TimelineID != nil || facts.PostgresMajorVersion != nil {
		t.Errorf("absent numeric fields must stay nil: %+v", facts)
	}
	if len(facts.Conditions) != 0 {
		t.Error("absent conditions must stay empty")
	}
}

// TestConvertClusterBoundsHostileStatus proves the boundary bounds
// condition count and message length.
func TestConvertClusterBoundsHostileStatus(t *testing.T) {
	t.Parallel()
	conditions := make([]any, 100)
	long := strings.Repeat("m", 10_000)
	for i := range conditions {
		conditions[i] = map[string]any{
			"type": "Ready", "status": "True",
			"reason": "r", "message": long,
			"lastTransitionTime": "2026-07-28T10:00:00Z",
		}
	}
	obj := fullCluster()
	obj["status"].(map[string]any)["conditions"] = conditions
	facts, err := convertCluster(obj)
	if err != nil {
		t.Fatalf("convertCluster: %v", err)
	}
	if len(facts.Conditions) != maxConditions {
		t.Errorf("conditions = %d, want capped at %d", len(facts.Conditions), maxConditions)
	}
	for _, c := range facts.Conditions {
		if len(c.Message) > maxConditionMessage {
			t.Fatalf("message length %d exceeds bound", len(c.Message))
		}
	}
}

func TestCategorizeTranslatesClientErrors(t *testing.T) {
	t.Parallel()
	gr := schema.GroupResource{Group: "postgresql.cnpg.io", Resource: "clusters"}
	cases := []struct {
		name string
		err  error
		want redact.Category
	}{
		{"forbidden", apierrors.NewForbidden(gr, "orders", errors.New("rbac")), redact.CategoryForbidden},
		{"unauthorized", apierrors.NewUnauthorized("no"), redact.CategoryForbidden},
		{"notfound", apierrors.NewNotFound(gr, "orders"), redact.CategoryNotFound},
		{"server timeout", apierrors.NewServerTimeout(gr, "get", 1), redact.CategoryTimeout},
		{"context deadline", context.DeadlineExceeded, redact.CategoryTimeout},
		{"context canceled", context.Canceled, redact.CategoryCanceled},
		{"plain", errors.New("boom"), redact.CategoryInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := categorize("op", tc.err)
			if redact.Categorize(got) != tc.want {
				t.Errorf("category = %q, want %q", redact.Categorize(got), tc.want)
			}
			if !strings.HasPrefix(got.Error(), "op: ") {
				t.Errorf("error text = %q, want op and category only", got.Error())
			}
		})
	}
}

// TestCategorizeNeverLeaksRequestDetail proves the categorized error's
// text stays free of the raw client error, which can embed URLs.
func TestCategorizeNeverLeaksRequestDetail(t *testing.T) {
	t.Parallel()
	raw := errors.New("GET https://10.0.0.1:6443/apis/postgresql.cnpg.io/v1/namespaces/payments/clusters/orders: sekret-canary")
	got := categorize("cluster get", raw)
	if strings.Contains(got.Error(), "sekret-canary") || strings.Contains(got.Error(), "10.0.0.1") {
		t.Fatalf("categorized error leaks raw detail: %q", got.Error())
	}
}

func TestProberReadinessMeaning(t *testing.T) {
	t.Parallel()
	gr := schema.GroupResource{Group: "postgresql.cnpg.io", Resource: "clusters"}
	cases := []struct {
		name      string
		err       error
		wantReady bool
	}{
		{"reachable", nil, true},
		{"cluster absent is an answer", apierrors.NewNotFound(gr, "orders"), true},
		{"forbidden is an answer", apierrors.NewForbidden(gr, "orders", errors.New("rbac")), true},
		{"timeout is unreachable", context.DeadlineExceeded, false},
		{"transport failure is unreachable", errors.New("connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newProberForTest(func(_ context.Context) error { return tc.err })
			err := p.Ready(context.Background())
			if (err == nil) != tc.wantReady {
				t.Errorf("Ready = %v, want ready=%v", err, tc.wantReady)
			}
		})
	}
}
