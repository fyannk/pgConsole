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
	"sort"

	apiv1 "github.com/cloudnative-pg/api/pkg/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

var (
	imageCatalogGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "imagecatalogs"}
	// clusterImageCatalogGVR is cluster-scoped: the only resource this
	// console can be granted outside its namespace, and only when the
	// deployment opts in with a separate ClusterRole.
	clusterImageCatalogGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusterimagecatalogs"}
	// clusterImageCatalogKind is the kind a Cluster names when it draws
	// its image from a cluster-scoped catalog.
	clusterImageCatalogKind = "ClusterImageCatalog"
)

const (
	imageCatalogListPageSize  = 100
	maxImageCatalogCandidates = 500
)

// FetchImageCatalogs lists the namespace's ImageCatalog resources.
//
// Unlike every other collection this client lists, the set is not
// filtered to the target cluster: a catalog is namespace-shared
// infrastructure that clusters reference, so there is no ownership to
// filter on. The console renders only the catalog the cluster's
// spec.imageCatalogRef names, and resolves that at render.
func (c *Client) FetchImageCatalogs(ctx context.Context) (observe.ImageCatalogsState, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	clusterCatalog, clusterState := c.fetchClusterImageCatalog(ctx)

	var catalogs []observe.ImageCatalogFacts
	examined := 0
	opts := metav1.ListOptions{Limit: imageCatalogListPageSize}
	rv := ""
	// The cluster-scoped catalog get above is deliberately not captured:
	// history records the namespace's objects, and the one cross-scope
	// read stays a render-time resolution.
	seed := c.seedRecord(scopeImageCatalogs)
	for {
		list, err := c.dyn.Resource(imageCatalogGVR).Namespace(c.opts.Namespace).List(ctx, opts)
		if err != nil {
			return observe.ImageCatalogsState{}, categorize("image catalogs list", err)
		}
		rv = list.GetResourceVersion()
		for i := range list.Items {
			facts, err := convertImageCatalog(list.Items[i].Object)
			if err != nil {
				return observe.ImageCatalogsState{}, err
			}
			seed.add(list.Items[i].Object)
			catalogs = append(catalogs, facts)
		}
		examined += len(list.Items)
		if list.GetContinue() == "" {
			break
		}
		if len(catalogs) > observe.MaxImageCatalogs || examined >= maxImageCatalogCandidates {
			seed.commit(false)
			return observe.ImageCatalogsState{
				Catalogs: catalogs, ResourceVersion: rv, Truncated: true,
				ClusterCatalog: clusterCatalog, ClusterCatalogState: clusterState,
			}, nil
		}
		opts.Continue = list.GetContinue()
	}
	seed.commit(true)
	return observe.ImageCatalogsState{
		Catalogs:            catalogs,
		ResourceVersion:     rv,
		Truncated:           len(catalogs) > observe.MaxImageCatalogs,
		ClusterCatalog:      clusterCatalog,
		ClusterCatalogState: clusterState,
	}, nil
}

// fetchClusterImageCatalog reads the one cluster-scoped catalog the
// Cluster references, when the deployment opted in.
//
// It follows the ObjectStore precedent exactly: no error return, a
// metadata-and-content read of one named object, and every failure path
// degrading into a named state rather than failing the whole fetch. A
// denied ClusterRole binding and a missing CRD both land on unknown —
// the console says it could not look, never that the catalog is absent.
// Only the API server confirming not-found produces absent.
//
// There is no watch. A cluster-scoped watch cannot be pinned by name, so
// it would require list authority over every catalog in the cluster; a
// get needs only the one. The content is standing configuration, and it
// refreshes on re-seed like the ObjectStore reference does.
func (c *Client) fetchClusterImageCatalog(ctx context.Context) (observe.ImageCatalogFacts, observe.ClusterCatalogState) {
	if !c.opts.AllowClusterCatalogs {
		return observe.ImageCatalogFacts{}, observe.ClusterCatalogDisabled
	}
	cluster, err := c.dyn.Resource(clusterGVR).Namespace(c.opts.Namespace).Get(ctx, c.opts.ClusterName, metav1.GetOptions{})
	if err != nil {
		c.logClusterCatalogUnavailable(err)
		return observe.ImageCatalogFacts{}, observe.ClusterCatalogUnknown
	}
	name, kind, err := imageCatalogReference(cluster.Object)
	if err != nil {
		c.logClusterCatalogUnavailable(err)
		return observe.ImageCatalogFacts{}, observe.ClusterCatalogUnknown
	}
	if kind != clusterImageCatalogKind || name == "" {
		return observe.ImageCatalogFacts{}, observe.ClusterCatalogNotReferenced
	}
	obj, err := c.dyn.Resource(clusterImageCatalogGVR).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return observe.ImageCatalogFacts{Name: name}, observe.ClusterCatalogAbsent
	}
	if err != nil {
		c.logClusterCatalogUnavailable(err)
		return observe.ImageCatalogFacts{Name: name}, observe.ClusterCatalogUnknown
	}
	facts, err := convertImageCatalog(obj.Object)
	if err != nil {
		c.logClusterCatalogUnavailable(err)
		return observe.ImageCatalogFacts{Name: name}, observe.ClusterCatalogUnknown
	}
	return facts, observe.ClusterCatalogPresent
}

// imageCatalogReference reads the Cluster's spec.imageCatalogRef.
func imageCatalogReference(content map[string]any) (name, kind string, err error) {
	var cluster apiv1.Cluster
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &cluster); err != nil {
		return "", "", redact.NewError("cluster catalog reference convert", redact.CategoryInternal, err)
	}
	if cluster.Spec.ImageCatalogRef == nil {
		return "", "", nil
	}
	return cluster.Spec.ImageCatalogRef.Name, cluster.Spec.ImageCatalogRef.Kind, nil
}

// logClusterCatalogUnavailable records the category of a failed
// cluster-scoped lookup, never its text.
func (c *Client) logClusterCatalogUnavailable(err error) {
	c.logger.Info("cluster image catalog unavailable",
		slog.String("category", redact.Safe(categorize("cluster image catalog get", err))))
}

// WatchImageCatalogs follows the namespace's catalogs from the seed
// version.
func (c *Client) WatchImageCatalogs(ctx context.Context, fromResourceVersion string) (observe.ImageCatalogWatch, error) {
	w, err := c.dyn.Resource(imageCatalogGVR).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{
		ResourceVersion: fromResourceVersion,
	})
	if err != nil {
		return nil, categorize("image catalogs watch", err)
	}
	items, stop := fanIn(ctx, []watch.Interface{w}, []pump[observe.ImageCatalogChange]{tap(c, scopeImageCatalogs, pumpImageCatalog)})
	return changeStream[observe.ImageCatalogChange]{stream[observe.ImageCatalogChange]{items: items, stop: stop}}, nil
}

// pumpImageCatalog converts one catalog watch event.
func pumpImageCatalog(event watch.Event) (observe.ImageCatalogChange, bool, bool) {
	var change observe.ImageCatalogChange
	obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
	if !ok {
		return change, false, true
	}
	facts, err := convertImageCatalog(obj.UnstructuredContent())
	if err != nil {
		return change, false, true
	}
	switch event.Type {
	case watch.Added, watch.Modified:
		change.Put = &facts
	case watch.Deleted:
		change.Delete = &observe.ImageCatalogDeletion{Name: facts.Name, UID: facts.UID}
	case watch.Bookmark:
		return change, false, false
	default:
		return change, false, true
	}
	return change, true, false
}

// convertImageCatalog converts a raw catalog into facts. The image list
// is ordered by major version and bounded here, at the boundary.
func convertImageCatalog(content map[string]any) (observe.ImageCatalogFacts, error) {
	var catalog apiv1.ImageCatalog
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &catalog); err != nil {
		return observe.ImageCatalogFacts{}, redact.NewError("image catalog convert", redact.CategoryInternal, err)
	}
	facts := observe.ImageCatalogFacts{
		Name:      catalog.Name,
		UID:       string(catalog.UID),
		CreatedAt: catalog.CreationTimestamp.Time.UTC(),
	}
	images := catalog.Spec.Images
	if len(images) > observe.MaxCatalogImages {
		images = images[:observe.MaxCatalogImages]
		facts.ImagesTruncated = true
	}
	for _, img := range images {
		facts.Images = append(facts.Images, observe.CatalogImageFacts{Major: img.Major, Image: img.Image})
	}
	sort.Slice(facts.Images, func(i, j int) bool { return facts.Images[i].Major < facts.Images[j].Major })
	return facts, nil
}
