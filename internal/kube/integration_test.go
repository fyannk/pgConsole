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

//go:build integration

package kube

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	rbacv1 "k8s.io/api/rbac/v1"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

// integrationEnv is the shared test API server of this file.
type integrationEnv struct {
	env      *envtest.Environment
	adminDyn dynamic.Interface
	adminCfg *rest.Config
	userCfg  *rest.Config
}

// startEnv boots envtest with the minimal Cluster CRD, creates the
// payments namespace, and provisions a restricted user holding exactly
// the access model's read shape: pinned get plus watch, no list.
func startEnv(t *testing.T) *integrationEnv {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS unset; run through make test-integration")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths: []string{"testdata/crd"},
	}
	adminCfg, err := env.Start()
	if err != nil {
		t.Fatalf("envtest start: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	adminSet, err := kubernetes.NewForConfig(adminCfg)
	if err != nil {
		t.Fatalf("admin clientset: %v", err)
	}
	adminDyn, err := dynamic.NewForConfig(adminCfg)
	if err != nil {
		t.Fatalf("admin dynamic: %v", err)
	}

	ctx := context.Background()
	ns := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Namespace",
		"metadata": map[string]any{"name": "payments"},
	}}
	nsGVR := clusterGVR
	nsGVR.Group, nsGVR.Version, nsGVR.Resource = "", "v1", "namespaces"
	if _, err := adminDyn.Resource(nsGVR).Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	// The restricted user mirrors the deployed ServiceAccount: cluster
	// get pinned by resourceNames, watch unpinned, no cluster list; pods
	// namespace-scoped with list and watch.
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: "pgconsole-orders-read", Namespace: "payments"},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"postgresql.cnpg.io"},
				Resources:     []string{"clusters"},
				Verbs:         []string{"get"},
				ResourceNames: []string{"orders"},
			},
			{
				APIGroups: []string{"postgresql.cnpg.io"},
				Resources: []string{"clusters"},
				Verbs:     []string{"watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"events"},
				Verbs:     []string{"list", "watch"},
			},
		},
	}
	if _, err := adminSet.RbacV1().Roles("payments").Create(ctx, role, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create role: %v", err)
	}
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "pgconsole-orders-read", Namespace: "payments"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "pgconsole-orders-read"},
		Subjects:   []rbacv1.Subject{{Kind: "User", APIGroup: "rbac.authorization.k8s.io", Name: "pgconsole"}},
	}
	if _, err := adminSet.RbacV1().RoleBindings("payments").Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create rolebinding: %v", err)
	}

	user, err := env.AddUser(envtest.User{Name: "pgconsole"}, adminCfg)
	if err != nil {
		t.Fatalf("add user: %v", err)
	}

	return &integrationEnv{env: env, adminDyn: adminDyn, adminCfg: adminCfg, userCfg: user.Config()}
}

// createCluster creates the cluster and sets its status phase through
// the status subresource, which strips status supplied at creation.
func createCluster(t *testing.T, ie *integrationEnv, phase string) {
	t.Helper()
	ctx := context.Background()
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": "orders", "namespace": "payments"},
		"spec":       map[string]any{"instances": int64(3)},
	}}
	if _, err := ie.adminDyn.Resource(clusterGVR).Namespace("payments").Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create cluster: %v", err)
	}
	setStatus(t, ie, map[string]any{"phase": phase})
}

// setStatus replaces the cluster status through the status subresource.
func setStatus(t *testing.T, ie *integrationEnv, status map[string]any) {
	t.Helper()
	ctx := context.Background()
	obj, err := ie.adminDyn.Resource(clusterGVR).Namespace("payments").Get(ctx, "orders", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("admin get: %v", err)
	}
	obj.Object["status"] = status
	if _, err := ie.adminDyn.Resource(clusterGVR).Namespace("payments").UpdateStatus(ctx, obj, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("admin update status: %v", err)
	}
}

// waitFor polls until the condition holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", what)
		case <-tick.C:
		}
	}
}

// TestAccessShapeAgainstRealRBAC is the entry-gate integration test: under a Role
// granting only pinned get plus watch, the client's fetch and
// name-scoped watch work, and list is denied — the console operates
// without the list verb.
func TestAccessShapeAgainstRealRBAC(t *testing.T) {
	ie := startEnv(t)
	ctx := context.Background()

	client, err := New(ie.userCfg, Options{
		Namespace:      "payments",
		ClusterName:    "orders",
		RequestTimeout: 10 * time.Second,
	}, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Absent cluster: fetch succeeds as an absence observation.
	state, err := client.Fetch(ctx)
	if err != nil {
		t.Fatalf("Fetch absent: %v", err)
	}
	if state.Facts.Present {
		t.Fatal("absent cluster fetched as present")
	}

	// List is forbidden for the restricted user: the check that the
	// console's access shape never depends on it.
	userDyn, err := dynamic.NewForConfig(ie.userCfg)
	if err != nil {
		t.Fatalf("user dynamic: %v", err)
	}
	_, err = userDyn.Resource(clusterGVR).Namespace("payments").List(ctx, metav1.ListOptions{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("List = %v, want forbidden", err)
	}

	// A get of a differently named cluster is denied by the pinning.
	_, err = userDyn.Resource(clusterGVR).Namespace("payments").Get(ctx, "other", metav1.GetOptions{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("unpinned Get = %v, want forbidden", err)
	}

	// Present cluster: pinned fetch converts it.
	createCluster(t, ie, "Setting up primary")
	state, err = client.Fetch(ctx)
	if err != nil {
		t.Fatalf("Fetch present: %v", err)
	}
	if !state.Facts.Present || state.Facts.Phase != "Setting up primary" {
		t.Fatalf("fetched facts wrong: %+v", state.Facts)
	}

	// The name-scoped watch delivers updates under the same Role.
	w, err := client.Watch(ctx, state.ResourceVersion)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer w.Stop()

	setStatus(t, ie, map[string]any{"phase": "Cluster in healthy state"})

	select {
	case got, ok := <-w.Results():
		if !ok {
			t.Fatal("watch closed before delivering the update")
		}
		if got.Facts.Phase != "Cluster in healthy state" {
			t.Fatalf("watched phase = %q", got.Facts.Phase)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("watch delivered no event")
	}
}

// TestCollectorAgainstRealAPIServer proves the whole loop: seed, watch,
// status update, deletion as explicit absence.
func TestCollectorAgainstRealAPIServer(t *testing.T) {
	ie := startEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := New(ie.userCfg, Options{
		Namespace:      "payments",
		ClusterName:    "orders",
		RequestTimeout: 10 * time.Second,
	}, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	createCluster(t, ie, "Setting up primary")

	store := observe.NewStore()
	collector := observe.NewCollector(client, store, observe.RealClock{}, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	done := make(chan struct{})
	go func() { defer close(done); _ = collector.Run(ctx) }()

	waitFor(t, "seed snapshot", func() bool {
		snap, ok := store.Current()
		return ok && snap.Cluster.Present && snap.Cluster.Phase == "Setting up primary" &&
			snap.Cluster.UID != ""
	})

	setStatus(t, ie, map[string]any{"phase": "Cluster in healthy state", "currentPrimary": "orders-1"})

	waitFor(t, "watched status update", func() bool {
		snap, ok := store.Current()
		return ok && snap.Cluster.CurrentPrimary == "orders-1" && !snap.Stale
	})

	if err := ie.adminDyn.Resource(clusterGVR).Namespace("payments").Delete(ctx, "orders", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("admin delete: %v", err)
	}

	waitFor(t, "explicit absence", func() bool {
		snap, ok := store.Current()
		return ok && !snap.Cluster.Present
	})

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("collector did not stop on cancellation")
	}
}

// memberPodObject builds an unstructured pod, owned by the cluster when
// owned is true.
func memberPodObject(name, role string, owned bool) *unstructured.Unstructured {
	meta := map[string]any{
		"name":      name,
		"namespace": "payments",
		"labels": map[string]any{
			"cnpg.io/cluster":      "orders",
			"cnpg.io/instanceRole": role,
		},
	}
	if owned {
		meta["ownerReferences"] = []any{map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Cluster",
			"name":       "orders",
			"uid":        "cluster-uid",
			"controller": true,
		}}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   meta,
		"spec": map[string]any{
			"containers": []any{map[string]any{
				"name":  "postgres",
				"image": "ghcr.io/cloudnative-pg/postgresql:16.4",
			}},
		},
	}}
}

// TestPodCollectorAgainstRealAPIServer proves label selection plus
// membership verification under the deployed Role shape: an owned pod
// appears, a labeled-but-unowned pod never does, and deletion removes
// the member.
func TestPodCollectorAgainstRealAPIServer(t *testing.T) {
	ie := startEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := New(ie.userCfg, Options{
		Namespace:      "payments",
		ClusterName:    "orders",
		RequestTimeout: 10 * time.Second,
	}, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	podsGVR := clusterGVR
	podsGVR.Group, podsGVR.Version, podsGVR.Resource = "", "v1", "pods"
	if _, err := ie.adminDyn.Resource(podsGVR).Namespace("payments").Create(ctx, memberPodObject("orders-1", "primary", true), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create member pod: %v", err)
	}
	if _, err := ie.adminDyn.Resource(podsGVR).Namespace("payments").Create(ctx, memberPodObject("intruder-1", "primary", false), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create intruder pod: %v", err)
	}

	store := observe.NewPodStore()
	collector := observe.NewPodCollector(client, store, observe.RealClock{}, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	done := make(chan struct{})
	go func() { defer close(done); _ = collector.Run(ctx) }()

	waitFor(t, "member-only pod set", func() bool {
		snap, ok := store.CurrentPods()
		return ok && len(snap.Pods) == 1 && snap.Pods[0].Name == "orders-1"
	})

	if _, err := ie.adminDyn.Resource(podsGVR).Namespace("payments").Create(ctx, memberPodObject("orders-2", "replica", true), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create second member: %v", err)
	}
	waitFor(t, "watched member addition", func() bool {
		snap, ok := store.CurrentPods()
		return ok && len(snap.Pods) == 2
	})

	zero := int64(0)
	if err := ie.adminDyn.Resource(podsGVR).Namespace("payments").Delete(ctx, "orders-2", metav1.DeleteOptions{GracePeriodSeconds: &zero}); err != nil {
		t.Fatalf("delete member: %v", err)
	}
	waitFor(t, "watched member removal", func() bool {
		snap, ok := store.CurrentPods()
		return ok && len(snap.Pods) == 1
	})

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("pod collector did not stop on cancellation")
	}
}

// TestOperationsBlockedByRBACAlone proves the misconfigured mode: with
// operations enabled but only the read-only Role, RBAC alone blocks
// every mutation — the writer's patch and create return forbidden, and
// nothing changes. This is the enforcement boundary independent of the
// flag.
func TestOperationsBlockedByRBACAlone(t *testing.T) {
	ie := startEnv(t)
	ctx := context.Background()
	createCluster(t, ie, "Cluster in healthy state")

	client, err := New(ie.userCfg, Options{
		Namespace:      "payments",
		ClusterName:    "orders",
		RequestTimeout: 10 * time.Second,
	}, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// The restricted user (the deployed read-only Role) has no write
	// rule on clusters or backups.
	if err := client.RestartCluster(ctx, time.Now()); !isForbidden(err) {
		t.Errorf("restart = %v, want forbidden by RBAC", err)
	}
	if err := client.ReloadCluster(ctx, time.Now()); !isForbidden(err) {
		t.Errorf("reload = %v, want forbidden by RBAC", err)
	}
	if err := client.PromoteInstance(ctx, "orders-2", time.Now()); !isForbidden(err) {
		t.Errorf("promote = %v, want forbidden by RBAC", err)
	}
	if err := client.CreateBackup(ctx, "ondemand-1"); !isForbidden(err) {
		t.Errorf("backup = %v, want forbidden by RBAC", err)
	}
}

// isForbidden reports the redact-categorized forbidden outcome.
func isForbidden(err error) bool {
	return err != nil && redact.Categorize(err) == redact.CategoryForbidden
}

// rawEventObject builds an unstructured v1 Event for an involved object.
func rawEventObject(name, kind, apiVersion, objectName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Event",
		"metadata":   map[string]any{"name": name, "namespace": "payments"},
		"involvedObject": map[string]any{
			"kind":       kind,
			"apiVersion": apiVersion,
			"name":       objectName,
			"namespace":  "payments",
		},
		"type":          "Normal",
		"reason":        "IntegrationTest",
		"message":       "event for the access-shape test",
		"count":         int64(1),
		"lastTimestamp": time.Now().UTC().Format(time.RFC3339),
	}}
}

// TestEventCollectorAgainstRealAPIServer proves the namespace-wide
// event list under the deployed Role shape retains only the cluster's
// candidates: the whole namespace transits the filter, the unrelated
// workload's event never leaves it.
func TestEventCollectorAgainstRealAPIServer(t *testing.T) {
	ie := startEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := New(ie.userCfg, Options{
		Namespace:      "payments",
		ClusterName:    "orders",
		RequestTimeout: 10 * time.Second,
	}, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	eventsGVR := clusterGVR
	eventsGVR.Group, eventsGVR.Version, eventsGVR.Resource = "", "v1", "events"
	for _, ev := range []*unstructured.Unstructured{
		rawEventObject("cluster-ev", "Cluster", "postgresql.cnpg.io/v1", "orders"),
		rawEventObject("stranger-ev", "Deployment", "apps/v1", "billing-api"),
	} {
		if _, err := ie.adminDyn.Resource(eventsGVR).Namespace("payments").Create(ctx, ev, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create event: %v", err)
		}
	}

	store := observe.NewEventStore()
	collector := observe.NewEventCollector(client, store, time.Hour, observe.RealClock{}, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	done := make(chan struct{})
	go func() { defer close(done); _ = collector.Run(ctx) }()

	waitFor(t, "candidate-only event set", func() bool {
		snap, ok := store.CurrentEvents()
		return ok && len(snap.Events) == 1 && snap.Events[0].Reason == "IntegrationTest" &&
			snap.Events[0].Object == "orders"
	})

	if _, err := ie.adminDyn.Resource(eventsGVR).Namespace("payments").Create(ctx, rawEventObject("pod-ev", "Pod", "v1", "orders-1"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod event: %v", err)
	}
	waitFor(t, "watched pod candidate", func() bool {
		snap, ok := store.CurrentEvents()
		return ok && len(snap.Events) == 2
	})

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("event collector did not stop on cancellation")
	}
}
