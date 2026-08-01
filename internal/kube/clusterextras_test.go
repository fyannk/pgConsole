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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/fyannk/pgConsole/internal/observe"
)

func rawQuorum(primary string, standbys []any, number int64) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "FailoverQuorum",
		"metadata": map[string]any{"name": "orders", "namespace": "payments"},
		"status": map[string]any{
			"method": "any", "primary": primary,
			"standbyNames": standbys, "standbyNumber": number,
		},
	}}
}

func rawCatalog(name string, images []any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "ImageCatalog",
		"metadata": map[string]any{"name": name, "namespace": "payments", "uid": "u-" + name},
		"spec":     map[string]any{"images": images},
	}}
}

func extrasScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	for _, kind := range []string{"FailoverQuorumList", "ImageCatalogList"} {
		s.AddKnownTypeWithName(schema.GroupVersionKind{
			Group: "postgresql.cnpg.io", Version: "v1", Kind: kind,
		}, &unstructured.UnstructuredList{})
	}
	return s
}

// TestFetchFailoverQuorumTreatsAbsenceAsAnObservation proves a cluster
// without a quorum reports "not configured" rather than an error. Most
// clusters do not run one, and reporting an ordinary configuration as a
// fault would make the console cry wolf.
func TestFetchFailoverQuorumTreatsAbsenceAsAnObservation(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	c.dyn = fake.NewSimpleDynamicClientWithCustomListKinds(extrasScheme(),
		map[schema.GroupVersionResource]string{failoverQuorumGVR: "FailoverQuorumList"})

	state, err := c.FetchFailoverQuorum(context.Background())
	if err != nil {
		t.Fatalf("an absent quorum was reported as an error: %v", err)
	}
	if state.Facts.Present {
		t.Error("an absent quorum reported itself present")
	}
}

// TestFetchFailoverQuorumReportsTheSynchronousSet proves the quorum's
// reported shape reaches the facts.
func TestFetchFailoverQuorumReportsTheSynchronousSet(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	c.dyn = fake.NewSimpleDynamicClientWithCustomListKinds(extrasScheme(),
		map[schema.GroupVersionResource]string{failoverQuorumGVR: "FailoverQuorumList"},
		rawQuorum("orders-1", []any{"orders-2", "orders-3"}, 1))

	state, err := c.FetchFailoverQuorum(context.Background())
	if err != nil {
		t.Fatalf("FetchFailoverQuorum: %v", err)
	}
	f := state.Facts
	if !f.Present || f.Primary != "orders-1" || f.StandbyNumber != 1 || len(f.Standbys) != 2 {
		t.Fatalf("facts = %+v, want the reported quorum", f)
	}
}

// TestConvertFailoverQuorumBoundsTheStandbyList proves the list is cut
// at the adapter boundary, so no later layer can render an unbounded
// one.
func TestConvertFailoverQuorumBoundsTheStandbyList(t *testing.T) {
	t.Parallel()
	standbys := make([]any, observe.MaxQuorumStandbys+10)
	for i := range standbys {
		standbys[i] = "standby"
	}
	facts, err := convertFailoverQuorum(rawQuorum("orders-1", standbys, 2).Object)
	if err != nil {
		t.Fatalf("convertFailoverQuorum: %v", err)
	}
	if len(facts.Standbys) != observe.MaxQuorumStandbys || !facts.StandbysTruncated {
		t.Errorf("standbys len=%d truncated=%v, want the bound applied and visible", len(facts.Standbys), facts.StandbysTruncated)
	}
}

// TestPumpFailoverQuorumTreatsDeletionAsAnObservation proves switching
// quorum off is rendered as absence rather than tearing the watch down.
func TestPumpFailoverQuorumTreatsDeletionAsAnObservation(t *testing.T) {
	t.Parallel()
	state, ok, fatal := pumpFailoverQuorum(watch.Event{
		Type: watch.Deleted, Object: rawQuorum("orders-1", nil, 1),
	})
	if !ok || fatal {
		t.Fatalf("deletion: ok=%v fatal=%v, want a recognized observation", ok, fatal)
	}
	if state.Facts.Present {
		t.Error("a deleted quorum reported itself present")
	}
}

// TestFetchImageCatalogsOrdersImagesByMajor proves the catalog renders
// in the order an operator reads it, and that the bound is applied.
func TestFetchImageCatalogsOrdersImagesByMajor(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	c.dyn = fake.NewSimpleDynamicClientWithCustomListKinds(extrasScheme(),
		map[schema.GroupVersionResource]string{imageCatalogGVR: "ImageCatalogList"},
		rawCatalog("postgres", []any{
			map[string]any{"major": int64(17), "image": "pg:17"},
			map[string]any{"major": int64(16), "image": "pg:16"},
		}))

	state, err := c.FetchImageCatalogs(context.Background())
	if err != nil {
		t.Fatalf("FetchImageCatalogs: %v", err)
	}
	if state.Truncated || len(state.Catalogs) != 1 {
		t.Fatalf("catalogs=%d truncated=%v, want the one catalog", len(state.Catalogs), state.Truncated)
	}
	if got := state.Catalogs[0].Images; len(got) != 2 || got[0].Major != 16 || got[1].Major != 17 {
		t.Errorf("images = %+v, want major-ascending", got)
	}
}

var (
	_ observe.FailoverQuorumSource = (*Client)(nil)
	_ observe.ImageCatalogSource   = (*Client)(nil)
)

func rawClusterWithCatalogRef(kind, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]any{"name": "orders", "namespace": "payments"},
		"spec": map[string]any{"imageCatalogRef": map[string]any{
			"apiGroup": "postgresql.cnpg.io", "kind": kind, "name": name, "major": int64(17),
		}},
	}}
}

func clusterCatalogScheme() *runtime.Scheme {
	s := extrasScheme()
	s.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "postgresql.cnpg.io", Version: "v1", Kind: "ClusterImageCatalogList",
	}, &unstructured.UnstructuredList{})
	s.AddKnownTypeWithName(schema.GroupVersionKind{
		Group: "postgresql.cnpg.io", Version: "v1", Kind: "ClusterList",
	}, &unstructured.UnstructuredList{})
	return s
}

func clusterCatalogListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		imageCatalogGVR:        "ImageCatalogList",
		clusterImageCatalogGVR: "ClusterImageCatalogList",
		clusterGVR:             "ClusterList",
	}
}

// TestClusterCatalogNotReadWithoutTheOptIn proves the cluster-scoped
// read never happens unless the deployment asked for it. This is the one
// capability that reaches outside the namespace, so "off" must mean the
// request is not made at all, not merely that it fails.
func TestClusterCatalogNotReadWithoutTheOptIn(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	c.opts.AllowClusterCatalogs = false
	c.dyn = fake.NewSimpleDynamicClientWithCustomListKinds(clusterCatalogScheme(), clusterCatalogListKinds(),
		rawClusterWithCatalogRef("ClusterImageCatalog", "shared"),
		rawClusterCatalog("shared"),
	)

	state, err := c.FetchImageCatalogs(context.Background())
	if err != nil {
		t.Fatalf("FetchImageCatalogs: %v", err)
	}
	if state.ClusterCatalogState != observe.ClusterCatalogDisabled {
		t.Errorf("state = %q, want disabled when the deployment did not opt in", state.ClusterCatalogState)
	}
	if len(state.ClusterCatalog.Images) != 0 {
		t.Error("the cluster-scoped catalog was read without the opt-in")
	}
}

func rawClusterCatalog(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "ClusterImageCatalog",
		"metadata": map[string]any{"name": name, "uid": "u-" + name},
		"spec": map[string]any{"images": []any{
			map[string]any{"major": int64(17), "image": "pg:17"},
		}},
	}}
}

// TestClusterCatalogReadWithTheOptIn proves the referenced cluster-scoped
// catalog is read, and only when the reference names that kind.
func TestClusterCatalogReadWithTheOptIn(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	c.opts.AllowClusterCatalogs = true
	c.dyn = fake.NewSimpleDynamicClientWithCustomListKinds(clusterCatalogScheme(), clusterCatalogListKinds(),
		rawClusterWithCatalogRef("ClusterImageCatalog", "shared"),
		rawClusterCatalog("shared"),
	)

	state, err := c.FetchImageCatalogs(context.Background())
	if err != nil {
		t.Fatalf("FetchImageCatalogs: %v", err)
	}
	if state.ClusterCatalogState != observe.ClusterCatalogPresent {
		t.Fatalf("state = %q, want present", state.ClusterCatalogState)
	}
	if len(state.ClusterCatalog.Images) != 1 || state.ClusterCatalog.Images[0].Major != 17 {
		t.Errorf("images = %+v, want the catalog's content", state.ClusterCatalog.Images)
	}
}

// TestClusterCatalogNotReferencedWhenTheRefIsNamespaced proves a
// namespaced reference does not trigger the cluster-scoped read.
func TestClusterCatalogNotReferencedWhenTheRefIsNamespaced(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	c.opts.AllowClusterCatalogs = true
	c.dyn = fake.NewSimpleDynamicClientWithCustomListKinds(clusterCatalogScheme(), clusterCatalogListKinds(),
		rawClusterWithCatalogRef("ImageCatalog", "postgres"),
	)

	state, err := c.FetchImageCatalogs(context.Background())
	if err != nil {
		t.Fatalf("FetchImageCatalogs: %v", err)
	}
	if state.ClusterCatalogState != observe.ClusterCatalogNotReferenced {
		t.Errorf("state = %q, want not-referenced for a namespaced reference", state.ClusterCatalogState)
	}
}

// TestClusterCatalogAbsentIsDistinctFromUnreadable proves the console
// separates "the API server says it is not there" from "I could not
// look". Collapsing them would let a denied binding read as a missing
// catalog, which is a claim the console has no basis for.
func TestClusterCatalogAbsentIsDistinctFromUnreadable(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	c.opts.AllowClusterCatalogs = true
	c.dyn = fake.NewSimpleDynamicClientWithCustomListKinds(clusterCatalogScheme(), clusterCatalogListKinds(),
		rawClusterWithCatalogRef("ClusterImageCatalog", "missing"),
	)

	state, err := c.FetchImageCatalogs(context.Background())
	if err != nil {
		t.Fatalf("FetchImageCatalogs: %v", err)
	}
	if state.ClusterCatalogState != observe.ClusterCatalogAbsent {
		t.Errorf("state = %q, want absent when the API server confirms not-found", state.ClusterCatalogState)
	}
	if state.ClusterCatalog.Name != "missing" {
		t.Errorf("name = %q, want the reference still named", state.ClusterCatalog.Name)
	}
}

// TestClusterCatalogDeniedReadsAsUnknownNotAbsent proves an unbound
// ClusterRole degrades to "could not look" rather than "not there". The
// difference matters: one is a deployment choice, the other is a claim
// about the cluster that the console has no basis to make.
func TestClusterCatalogDeniedReadsAsUnknownNotAbsent(t *testing.T) {
	t.Parallel()
	c, logs := newTestClient(t)
	c.opts.AllowClusterCatalogs = true
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(clusterCatalogScheme(), clusterCatalogListKinds(),
		rawClusterWithCatalogRef("ClusterImageCatalog", "shared"),
	)
	dyn.PrependReactor("get", "clusterimagecatalogs", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "postgresql.cnpg.io", Resource: "clusterimagecatalogs"},
			"shared", errors.New("no ClusterRole bound"))
	})
	c.dyn = dyn

	state, err := c.FetchImageCatalogs(context.Background())
	if err != nil {
		t.Fatalf("a denied cluster-scoped read failed the whole fetch: %v", err)
	}
	if state.ClusterCatalogState != observe.ClusterCatalogUnknown {
		t.Errorf("state = %q, want unknown when the read is refused", state.ClusterCatalogState)
	}
	// The namespaced half must be unaffected.
	if state.Catalogs != nil && len(state.Catalogs) != 0 {
		t.Errorf("namespaced catalogs = %+v, want the namespaced listing unaffected", state.Catalogs)
	}
	if !strings.Contains(logs.String(), "forbidden") {
		t.Errorf("the refusal category was not logged: %s", logs.String())
	}
}
