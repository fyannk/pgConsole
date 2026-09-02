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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

var failoverQuorumGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "failoverquorums"}

// FetchFailoverQuorum reads the cluster's quorum object through a pinned
// get.
//
// CloudNativePG names the object after the Cluster, so the name is known
// from configuration and the get can be pinned by resourceNames in RBAC
// — the same access shape the Cluster itself uses. A not-found is a
// successful observation of absence: most clusters do not run a failover
// quorum, and reporting that as an error would make an ordinary
// configuration look like a fault.
func (c *Client) FetchFailoverQuorum(ctx context.Context) (observe.FailoverQuorumState, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	obj, err := c.dyn.Resource(failoverQuorumGVR).Namespace(c.opts.Namespace).
		Get(ctx, c.opts.ClusterName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		c.seedRecord(scopeFailoverQuorum).commit(true)
		return observe.FailoverQuorumState{Facts: observe.FailoverQuorumFacts{Present: false}}, nil
	}
	if err != nil {
		return observe.FailoverQuorumState{}, categorize("failover quorum get", err)
	}
	facts, err := convertFailoverQuorum(obj.Object)
	if err != nil {
		return observe.FailoverQuorumState{}, err
	}
	seed := c.seedRecord(scopeFailoverQuorum)
	seed.add(obj.Object)
	seed.commit(true)
	return observe.FailoverQuorumState{Facts: facts}, nil
}

// WatchFailoverQuorum follows the one quorum object by name. Like the
// Cluster watch it sends no resource version — a singleton get yields no
// watch-safe cursor, see Watch — so it starts from current state and
// re-delivers the object as one synthetic Added.
func (c *Client) WatchFailoverQuorum(ctx context.Context) (observe.FailoverQuorumWatch, error) {
	w, err := c.dyn.Resource(failoverQuorumGVR).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector:       "metadata.name=" + c.opts.ClusterName,
		AllowWatchBookmarks: false,
	})
	if err != nil {
		return nil, categorize("failover quorum watch", err)
	}
	items, stop := fanIn(ctx, []watch.Interface{w}, []pump[observe.FailoverQuorumState]{tap(c, scopeFailoverQuorum, pumpFailoverQuorum)})
	return resultStream[observe.FailoverQuorumState]{stream[observe.FailoverQuorumState]{items: items, stop: stop}}, nil
}

// pumpFailoverQuorum converts one quorum watch event. A deletion is a
// complete observation in its own right: quorum being switched off is a
// fact the console renders, not a reason to re-seed.
func pumpFailoverQuorum(event watch.Event) (observe.FailoverQuorumState, bool, bool) {
	switch event.Type {
	case watch.Added, watch.Modified:
		obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
		if !ok {
			return observe.FailoverQuorumState{}, false, true
		}
		facts, err := convertFailoverQuorum(obj.UnstructuredContent())
		if err != nil {
			return observe.FailoverQuorumState{}, false, true
		}
		return observe.FailoverQuorumState{Facts: facts}, true, false
	case watch.Deleted:
		return observe.FailoverQuorumState{Facts: observe.FailoverQuorumFacts{Present: false}}, true, false
	case watch.Bookmark:
		return observe.FailoverQuorumState{}, false, false
	default:
		return observe.FailoverQuorumState{}, false, true
	}
}

// convertFailoverQuorum converts a raw quorum object into facts. The
// standby list is bounded here, at the boundary, so no later layer can
// render or retain an unbounded one.
func convertFailoverQuorum(content map[string]any) (observe.FailoverQuorumFacts, error) {
	var quorum apiv1.FailoverQuorum
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &quorum); err != nil {
		return observe.FailoverQuorumFacts{}, redact.NewError("failover quorum convert", redact.CategoryInternal, err)
	}
	facts := observe.FailoverQuorumFacts{
		Present:       true,
		Method:        quorum.Status.Method,
		Primary:       quorum.Status.Primary,
		StandbyNumber: quorum.Status.StandbyNumber,
	}
	standbys := quorum.Status.StandbyNames
	if len(standbys) > observe.MaxQuorumStandbys {
		standbys = standbys[:observe.MaxQuorumStandbys]
		facts.StandbysTruncated = true
	}
	facts.Standbys = append([]string(nil), standbys...)
	return facts, nil
}
