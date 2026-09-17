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

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

var leaseGVR = schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"}

// FetchPrimaryLease reads the Lease named after the cluster through a
// pinned get. Absence is an observation: an operator before 1.30 keeps
// no such lease.
//
// The Lease is deliberately not recorded into the object timeline. Its
// holder renews it every few seconds, and each renewal is a spec change
// to the recorder, so a recorded lease would fill the timeline with
// nothing but its own heartbeat and evict the revisions worth keeping.
func (c *Client) FetchPrimaryLease(ctx context.Context) (observe.PrimaryLeaseState, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	obj, err := c.dyn.Resource(leaseGVR).Namespace(c.opts.Namespace).
		Get(ctx, c.opts.ClusterName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return observe.PrimaryLeaseState{Facts: observe.PrimaryLeaseFacts{Present: false}}, nil
	}
	if err != nil {
		return observe.PrimaryLeaseState{}, categorize("primary lease get", err)
	}
	facts, err := convertPrimaryLease(obj.Object)
	if err != nil {
		return observe.PrimaryLeaseState{}, err
	}
	return observe.PrimaryLeaseState{Facts: facts}, nil
}

// WatchPrimaryLease follows the one Lease by name from the server's
// current state.
func (c *Client) WatchPrimaryLease(ctx context.Context) (observe.PrimaryLeaseWatch, error) {
	w, err := c.dyn.Resource(leaseGVR).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector:       "metadata.name=" + c.opts.ClusterName,
		AllowWatchBookmarks: false,
	})
	if err != nil {
		return nil, categorize("primary lease watch", err)
	}
	items, stop := fanIn(ctx, []watch.Interface{w}, []pump[observe.PrimaryLeaseState]{pumpPrimaryLease})
	return resultStream[observe.PrimaryLeaseState]{stream[observe.PrimaryLeaseState]{items: items, stop: stop}}, nil
}

func pumpPrimaryLease(event watch.Event) (observe.PrimaryLeaseState, bool, bool) {
	switch event.Type {
	case watch.Added, watch.Modified:
		obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
		if !ok {
			return observe.PrimaryLeaseState{}, false, true
		}
		facts, err := convertPrimaryLease(obj.UnstructuredContent())
		if err != nil {
			return observe.PrimaryLeaseState{}, false, true
		}
		return observe.PrimaryLeaseState{Facts: facts}, true, false
	case watch.Deleted:
		return observe.PrimaryLeaseState{Facts: observe.PrimaryLeaseFacts{Present: false}}, true, false
	case watch.Bookmark:
		return observe.PrimaryLeaseState{}, false, false
	default:
		return observe.PrimaryLeaseState{}, false, true
	}
}

// convertPrimaryLease reads the Lease spec verbatim. Every field is
// optional in the API and stays nil when absent.
func convertPrimaryLease(content map[string]any) (observe.PrimaryLeaseFacts, error) {
	var lease coordinationv1.Lease
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &lease); err != nil {
		return observe.PrimaryLeaseFacts{}, redact.NewError("primary lease convert", redact.CategoryInternal, err)
	}
	facts := observe.PrimaryLeaseFacts{
		Present:         true,
		DurationSeconds: lease.Spec.LeaseDurationSeconds,
		Transitions:     lease.Spec.LeaseTransitions,
	}
	if lease.Spec.HolderIdentity != nil {
		facts.Holder = boundOperatorMessage(*lease.Spec.HolderIdentity)
	}
	if lease.Spec.AcquireTime != nil {
		at := lease.Spec.AcquireTime.Time.UTC()
		facts.AcquiredAt = &at
	}
	if lease.Spec.RenewTime != nil {
		at := lease.Spec.RenewTime.Time.UTC()
		facts.RenewedAt = &at
	}
	return facts, nil
}
