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

	apiv1 "github.com/cloudnative-pg/api/pkg/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

var poolerGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "poolers"}

const (
	poolerListPageSize  = 200
	maxPoolerCandidates = 1000
)

// FetchPoolers lists the namespace's Pooler resources, selecting the
// configured Cluster by spec.cluster.name.
//
// The watch is namespace-scoped because RBAC cannot pin a list or watch
// by name, so another cluster's poolers are expected traffic and the
// selection is made here, application-side.
func (c *Client) FetchPoolers(ctx context.Context) ([]observe.PoolerFacts, string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	var poolers []observe.PoolerFacts
	examined := 0
	opts := metav1.ListOptions{Limit: poolerListPageSize}
	rv := ""
	seed := c.seedRecord(scopePoolers)
	for {
		list, err := c.dyn.Resource(poolerGVR).Namespace(c.opts.Namespace).List(ctx, opts)
		if err != nil {
			return nil, "", false, categorize("poolers list", err)
		}
		rv = list.GetResourceVersion()
		for i := range list.Items {
			facts, member, err := c.convertPooler(list.Items[i].Object)
			if err != nil {
				return nil, "", false, err
			}
			if member {
				seed.add(list.Items[i].Object)
				poolers = append(poolers, facts)
			}
		}
		examined += len(list.Items)
		if list.GetContinue() == "" {
			break
		}
		// Two independent ceilings: the kept set, and the items scanned,
		// which is what protects against a namespace holding thousands
		// of other clusters' poolers.
		if len(poolers) > observe.MaxPoolers || examined >= maxPoolerCandidates {
			seed.commit(false)
			return poolers, rv, true, nil
		}
		opts.Continue = list.GetContinue()
	}
	seed.commit(true)
	return poolers, rv, len(poolers) > observe.MaxPoolers, nil
}

// WatchPoolers follows the namespace's Pooler resources from the seed
// version.
func (c *Client) WatchPoolers(ctx context.Context, fromResourceVersion string) (observe.PoolerWatch, error) {
	w, err := c.dyn.Resource(poolerGVR).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{
		ResourceVersion: fromResourceVersion,
	})
	if err != nil {
		return nil, categorize("poolers watch", err)
	}
	items, stop := fanIn(ctx, []watch.Interface{w}, []pump[observe.PoolerChange]{tap(c, scopePoolers, c.pumpPooler)})
	return changeStream[observe.PoolerChange]{stream[observe.PoolerChange]{items: items, stop: stop}}, nil
}

// pumpPooler converts one Pooler watch event. A pooler belonging to
// another cluster is skipped, not fatal.
func (c *Client) pumpPooler(event watch.Event) (observe.PoolerChange, bool, bool) {
	var change observe.PoolerChange
	obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
	if !ok {
		return change, false, true
	}
	facts, member, err := c.convertPooler(obj.UnstructuredContent())
	if err != nil {
		return change, false, true
	}
	if !member {
		return change, false, false
	}
	switch event.Type {
	case watch.Added, watch.Modified:
		change.Put = &facts
	case watch.Deleted:
		change.Delete = &observe.PoolerDeletion{Name: facts.Name, UID: facts.UID}
	case watch.Bookmark:
		return change, false, false
	default:
		return change, false, true
	}
	return change, true, false
}

// convertPooler converts a raw Pooler into facts plus its membership
// verdict. Membership is the exact spec.cluster.name reference in the
// configured namespace.
//
// status.secrets is deliberately not read: it names credential Secrets,
// the console holds no Secret permission, and a pooler's credential
// wiring is not something this screen answers.
func (c *Client) convertPooler(content map[string]any) (observe.PoolerFacts, bool, error) {
	var pooler apiv1.Pooler
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &pooler); err != nil {
		return observe.PoolerFacts{}, false, redact.NewError("pooler convert", redact.CategoryInternal, err)
	}
	facts := observe.PoolerFacts{
		Name:             pooler.Name,
		UID:              string(pooler.UID),
		Type:             string(pooler.Spec.Type),
		DesiredInstances: pooler.Spec.Instances,
		ReadyInstances:   pooler.Status.Instances,
		Phase:            string(pooler.Status.Phase),
		PhaseReason:      boundOperatorMessage(pooler.Status.PhaseReason),
		Image:            pooler.Status.Image,
		CreatedAt:        pooler.CreationTimestamp.Time.UTC(),
	}
	if pooler.Spec.PgBouncer != nil {
		facts.PoolMode = string(pooler.Spec.PgBouncer.PoolMode)
	}
	return facts, pooler.Namespace == c.opts.Namespace && pooler.Spec.Cluster.Name == c.opts.ClusterName, nil
}
