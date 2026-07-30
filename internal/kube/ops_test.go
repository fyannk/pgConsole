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
	"testing"
	"time"

	"bytes"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// findAction returns the first action matching verb and resource.
func findAction(t *testing.T, actions []k8stesting.Action, verb, resource string) k8stesting.Action {
	t.Helper()
	for _, a := range actions {
		if a.GetVerb() == verb && a.GetResource().Resource == resource {
			return a
		}
	}
	t.Fatalf("no %s on %s in %d actions", verb, resource, len(actions))
	return nil
}

// opTime is the fixed instant of the interaction golden tests.
var opTime = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

// TestOperationInteractionGoldens pins the exact patch each operation
// produces against the CloudNativePG release-1.30 plugin interactions.
// A change to any of these bytes is a change to what the operator is
// asked to do and must be re-verified against the plugin source.
func TestOperationInteractionGoldens(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  []byte
		want string
	}{
		{
			name: "reload",
			got:  reloadPatch(opTime),
			want: `{"metadata":{"annotations":{"cnpg.io/reloadedAt":"2026-07-28T12:00:00.000000Z"}}}`,
		},
		{
			name: "restart",
			got:  restartPatch(opTime),
			want: `{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"2026-07-28T12:00:00Z"}}}`,
		},
		{
			name: "promote",
			got:  promotePatch("orders-2", opTime),
			want: `{"status":{"phase":"Switchover in progress","phaseReason":"Switching over to orders-2","targetPrimary":"orders-2","targetPrimaryTimestamp":"2026-07-28T12:00:00.000000Z"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.got) != tc.want {
				t.Errorf("patch = %s\nwant   %s", tc.got, tc.want)
			}
		})
	}
}

// newOpsTestClient builds a Client over a fake dynamic client that
// records every action.
func newOpsTestClient(t *testing.T) (*Client, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			backupGVR:  "BackupList",
			clusterGVR: "ClusterList",
		},
	)
	return &Client{
		dyn:    dyn,
		opts:   Options{Namespace: "payments", ClusterName: "orders", RequestTimeout: time.Second},
		logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)),
	}, dyn
}

func TestCreateBackupReferencesTheCluster(t *testing.T) {
	t.Parallel()
	c, dyn := newOpsTestClient(t)
	if err := c.CreateBackup(context.Background(), "ondemand-1"); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	actions := dyn.Actions()
	create := findAction(t, actions, "create", "backups")
	obj := create.(k8stesting.CreateAction).GetObject()
	unst := obj.(interface{ UnstructuredContent() map[string]any }).UnstructuredContent()
	spec := unst["spec"].(map[string]any)["cluster"].(map[string]any)
	if spec["name"] != "orders" {
		t.Errorf("backup references %v, want orders", spec["name"])
	}
}

func TestPromotePatchesTheStatusSubresource(t *testing.T) {
	t.Parallel()
	c, dyn := newOpsTestClient(t)
	// Seed a cluster so the patch has an object to touch.
	seedCluster(t, dyn)
	if err := c.PromoteInstance(context.Background(), "orders-2", opTime); err != nil {
		t.Fatalf("PromoteInstance: %v", err)
	}
	patch := findAction(t, dyn.Actions(), "patch", "clusters").(k8stesting.PatchAction)
	if patch.GetSubresource() != "status" {
		t.Errorf("subresource = %q, want status", patch.GetSubresource())
	}
	if patch.GetPatchType() != types.MergePatchType {
		t.Errorf("patch type = %v, want merge", patch.GetPatchType())
	}
}

func TestReloadAndRestartPatchTheClusterNotStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		call func(c *Client) error
	}{
		{"reload", func(c *Client) error { return c.ReloadCluster(context.Background(), opTime) }},
		{"restart", func(c *Client) error { return c.RestartCluster(context.Background(), opTime) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, dyn := newOpsTestClient(t)
			seedCluster(t, dyn)
			if err := tc.call(c); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			patch := findAction(t, dyn.Actions(), "patch", "clusters").(k8stesting.PatchAction)
			if patch.GetSubresource() != "" {
				t.Errorf("%s touched subresource %q, want the main resource", tc.name, patch.GetSubresource())
			}
		})
	}
}

// seedCluster creates a minimal cluster so patches have a target.
func seedCluster(t *testing.T, dyn *dynamicfake.FakeDynamicClient) {
	t.Helper()
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": "orders", "namespace": "payments"},
	}}
	if _, err := dyn.Resource(clusterGVR).Namespace("payments").Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
}
