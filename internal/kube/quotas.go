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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

var quotaGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "resourcequotas"}

// FetchQuotas lists the namespace's ResourceQuota objects. Like the
// image catalogs, quotas are namespace infrastructure: nothing owns
// them, and any of them can refuse this cluster's objects, so the whole
// namespaced set is the honest scope.
func (c *Client) FetchQuotas(ctx context.Context) (observe.QuotasState, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	list, err := c.dyn.Resource(quotaGVR).Namespace(c.opts.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return observe.QuotasState{}, categorize("resource quotas list", err)
	}
	quotas := make([]observe.QuotaFacts, 0, len(list.Items))
	for i := range list.Items {
		facts, err := convertQuota(list.Items[i].Object)
		if err != nil {
			return observe.QuotasState{}, err
		}
		quotas = append(quotas, facts)
	}
	return observe.QuotasState{Quotas: quotas, ResourceVersion: list.GetResourceVersion()}, nil
}

// WatchQuotas streams quota changes from the given version.
func (c *Client) WatchQuotas(ctx context.Context, fromResourceVersion string) (observe.QuotaWatch, error) {
	w, err := c.dyn.Resource(quotaGVR).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{
		ResourceVersion: fromResourceVersion,
	})
	if err != nil {
		return nil, categorize("resource quotas watch", err)
	}
	items, stop := fanIn(ctx,
		[]watch.Interface{w},
		[]pump[observe.QuotaChange]{c.pumpQuota})
	return changeStream[observe.QuotaChange]{stream[observe.QuotaChange]{items: items, stop: stop}}, nil
}

// pumpQuota converts one quota watch event; a malformed object is
// dropped rather than ending the stream.
func (c *Client) pumpQuota(event watch.Event) (observe.QuotaChange, bool, bool) {
	obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
	if !ok {
		return observe.QuotaChange{}, false, event.Type == watch.Error
	}
	facts, err := convertQuota(obj.UnstructuredContent())
	if err != nil {
		return observe.QuotaChange{}, false, false
	}
	switch event.Type {
	case watch.Added, watch.Modified:
		return observe.QuotaChange{Put: &facts}, true, false
	case watch.Deleted:
		return observe.QuotaChange{Delete: &observe.QuotaDeletion{Name: facts.Name, UID: facts.UID}}, true, false
	default:
		return observe.QuotaChange{}, false, event.Type == watch.Error
	}
}

// convertQuota reduces one ResourceQuota to facts. Exhaustion is
// computed here, at the boundary, where hard and used are still
// quantities that can be compared as quantities; downstream layers see
// the verdict beside the two numbers it came from.
func convertQuota(content map[string]any) (observe.QuotaFacts, error) {
	var quota corev1.ResourceQuota
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &quota); err != nil {
		return observe.QuotaFacts{}, redact.NewError("resource quota convert", redact.CategoryInternal, err)
	}
	facts := observe.QuotaFacts{Name: quota.Name, UID: string(quota.UID)}
	for resource, hard := range quota.Status.Hard {
		line := observe.QuotaResourceFacts{Resource: string(resource), Hard: hard.String()}
		if used, ok := quota.Status.Used[resource]; ok {
			line.Used = used.String()
			line.Exhausted = used.Cmp(hard) >= 0
		}
		facts.Resources = append(facts.Resources, line)
	}
	sort.Slice(facts.Resources, func(i, j int) bool {
		return facts.Resources[i].Resource < facts.Resources[j].Resource
	})
	return facts, nil
}
