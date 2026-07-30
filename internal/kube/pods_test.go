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
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// newTestClient builds a Client without a live connection, sufficient
// for conversion and membership tests, logging into the returned
// buffer.
func newTestClient(t *testing.T) (*Client, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	return &Client{
		opts:   Options{Namespace: "payments", ClusterName: "orders", RequestTimeout: time.Second},
		logger: slog.New(slog.NewJSONHandler(logs, nil)),
	}, logs
}

// ownedPod is an unstructured member pod with every consumed field set.
func ownedPod(name, uid, role string) map[string]any {
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      name,
			"namespace": "payments",
			"uid":       uid,
			"labels": map[string]any{
				"cnpg.io/cluster":      "orders",
				"cnpg.io/instanceRole": role,
			},
			"ownerReferences": []any{
				map[string]any{
					"apiVersion": "postgresql.cnpg.io/v1",
					"kind":       "Cluster",
					"name":       "orders",
					"uid":        "cluster-uid",
					"controller": true,
				},
			},
		},
		"spec": map[string]any{
			"nodeName": "node-a",
			"containers": []any{
				map[string]any{"name": "postgres", "image": "ghcr.io/cloudnative-pg/postgresql:16.4"},
			},
		},
		"status": map[string]any{
			"phase": "Running",
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "True"},
			},
			"containerStatuses": []any{
				map[string]any{"name": "postgres", "restartCount": int64(2), "image": "x", "imageID": "y", "ready": true, "state": map[string]any{}},
			},
		},
	}
}

func TestConvertPodFullObject(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	facts, member, err := c.convertPod(ownedPod("orders-1", "u1", "primary"))
	if err != nil {
		t.Fatalf("convertPod: %v", err)
	}
	if !member {
		t.Fatal("owned, labeled pod not a member")
	}
	if facts.Name != "orders-1" || facts.Role != "primary" || facts.Phase != "Running" {
		t.Errorf("core facts wrong: %+v", facts)
	}
	if facts.Ready == nil || !*facts.Ready {
		t.Error("Ready not converted")
	}
	if facts.Restarts == nil || *facts.Restarts != 2 {
		t.Error("Restarts not converted")
	}
	if facts.Node != "node-a" || facts.Image != "ghcr.io/cloudnative-pg/postgresql:16.4" {
		t.Errorf("placement facts wrong: %+v", facts)
	}
}

// TestConvertPodMinimalObject proves unreported pod facts stay unknown.
func TestConvertPodMinimalObject(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	obj := ownedPod("orders-2", "u2", "")
	meta := obj["metadata"].(map[string]any)
	delete(meta["labels"].(map[string]any), "cnpg.io/instanceRole")
	obj["spec"] = map[string]any{}
	obj["status"] = map[string]any{}
	facts, member, err := c.convertPod(obj)
	if err != nil {
		t.Fatalf("convertPod: %v", err)
	}
	if !member {
		t.Fatal("ownership alone must establish membership")
	}
	if facts.Role != "" || facts.Node != "" || facts.Image != "" || facts.Phase != "" {
		t.Errorf("absent string facts must stay empty: %+v", facts)
	}
	if facts.Ready != nil || facts.Restarts != nil {
		t.Errorf("absent numeric facts must stay nil: %+v", facts)
	}
}

// TestPodMembershipVerification proves labels select but ownership
// decides: a labeled pod without the controller owner reference to the
// configured Cluster is excluded and logged with a stable reason.
func TestPodMembershipVerification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(obj map[string]any)
		member bool
	}{
		{"owned by the cluster", func(_ map[string]any) {}, true},
		{"no owner reference", func(obj map[string]any) {
			delete(obj["metadata"].(map[string]any), "ownerReferences")
		}, false},
		{"owned by another cluster", func(obj map[string]any) {
			ref := obj["metadata"].(map[string]any)["ownerReferences"].([]any)[0].(map[string]any)
			ref["name"] = "other"
		}, false},
		{"owned by a different kind", func(obj map[string]any) {
			ref := obj["metadata"].(map[string]any)["ownerReferences"].([]any)[0].(map[string]any)
			ref["kind"] = "StatefulSet"
			ref["apiVersion"] = "apps/v1"
		}, false},
		{"owner not the controller", func(obj map[string]any) {
			ref := obj["metadata"].(map[string]any)["ownerReferences"].([]any)[0].(map[string]any)
			ref["controller"] = false
		}, false},
		{"wrong owner group", func(obj map[string]any) {
			ref := obj["metadata"].(map[string]any)["ownerReferences"].([]any)[0].(map[string]any)
			ref["apiVersion"] = "example.com/v1"
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := newTestClient(t)
			obj := ownedPod("orders-1", "u1", "primary")
			tc.mutate(obj)
			_, member, err := c.convertPod(obj)
			if err != nil {
				t.Fatalf("convertPod: %v", err)
			}
			if member != tc.member {
				t.Errorf("member = %v, want %v", member, tc.member)
			}
		})
	}
}

func TestExcludedPodLogsStableReason(t *testing.T) {
	t.Parallel()
	c, logs := newTestClient(t)
	c.logExcludedPod("intruder-1")
	if !strings.Contains(logs.String(), `"reason":"membership"`) {
		t.Fatalf("exclusion log misses the stable reason: %s", logs.String())
	}
}
