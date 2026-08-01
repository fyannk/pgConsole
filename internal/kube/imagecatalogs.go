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
	"sort"

	apiv1 "github.com/cloudnative-pg/api/pkg/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

var imageCatalogGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "imagecatalogs"}

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
func (c *Client) FetchImageCatalogs(ctx context.Context) ([]observe.ImageCatalogFacts, string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	var catalogs []observe.ImageCatalogFacts
	examined := 0
	opts := metav1.ListOptions{Limit: imageCatalogListPageSize}
	rv := ""
	for {
		list, err := c.dyn.Resource(imageCatalogGVR).Namespace(c.opts.Namespace).List(ctx, opts)
		if err != nil {
			return nil, "", false, categorize("image catalogs list", err)
		}
		rv = list.GetResourceVersion()
		for i := range list.Items {
			facts, err := convertImageCatalog(list.Items[i].Object)
			if err != nil {
				return nil, "", false, err
			}
			catalogs = append(catalogs, facts)
		}
		examined += len(list.Items)
		if list.GetContinue() == "" {
			break
		}
		if len(catalogs) > observe.MaxImageCatalogs || examined >= maxImageCatalogCandidates {
			return catalogs, rv, true, nil
		}
		opts.Continue = list.GetContinue()
	}
	return catalogs, rv, len(catalogs) > observe.MaxImageCatalogs, nil
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
	items, stop := fanIn(ctx, []watch.Interface{w}, []pump[observe.ImageCatalogChange]{pumpImageCatalog})
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
