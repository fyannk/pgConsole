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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/observe"
)

// The pgToolBox operator CRDs the review panel consumes, in the family
// API group. The console reads requests and role names, and writes only
// the request status subresource; it never touches users or proxy
// configuration.
var (
	accessRequestGVR = schema.GroupVersionResource{Group: "pgtoolbox.fyannk.dev", Version: "v1alpha1", Resource: "pgtoolboxaccessrequests"}
	accessRoleGVR    = schema.GroupVersionResource{Group: "pgtoolbox.fyannk.dev", Version: "v1alpha1", Resource: "pgtoolboxroles"}
)

const (
	accessRequestListPageSize  = 200
	maxAccessRequestCandidates = 2000
	maxRoleCandidates          = 1000
)

// FetchAccessReview lists the namespace's access requests and the role
// names for the approval picker. Roles are seeded here and refresh on
// each reseed; the requests are what the watch keeps live.
func (c *Client) FetchAccessReview(ctx context.Context) (observe.AccessReviewState, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	requests, rv, truncated, err := c.listAccessRequests(ctx)
	if err != nil {
		return observe.AccessReviewState{}, err
	}
	roles, err := c.listAccessRoles(ctx)
	if err != nil {
		return observe.AccessReviewState{}, err
	}
	return observe.AccessReviewState{
		Requests:                requests,
		Roles:                   roles,
		RequestsTruncated:       truncated,
		RequestsResourceVersion: rv,
	}, nil
}

func (c *Client) listAccessRequests(ctx context.Context) ([]observe.AccessRequestFacts, string, bool, error) {
	var requests []observe.AccessRequestFacts
	examined := 0
	opts := metav1.ListOptions{Limit: accessRequestListPageSize}
	rv := ""
	seed := c.seedRecord(scopeAccessRequests)
	for {
		list, err := c.dyn.Resource(accessRequestGVR).Namespace(c.opts.Namespace).List(ctx, opts)
		if err != nil {
			return nil, "", false, categorize("access requests list", err)
		}
		rv = list.GetResourceVersion()
		for i := range list.Items {
			seed.add(list.Items[i].Object)
			requests = append(requests, convertAccessRequest(&list.Items[i]))
		}
		examined += len(list.Items)
		if list.GetContinue() == "" {
			break
		}
		if len(requests) > observe.MaxAccessRequests || examined >= maxAccessRequestCandidates {
			seed.commit(false)
			return requests, rv, true, nil
		}
		opts.Continue = list.GetContinue()
	}
	seed.commit(true)
	return requests, rv, len(requests) > observe.MaxAccessRequests, nil
}

func (c *Client) listAccessRoles(ctx context.Context) ([]string, error) {
	var roles []string
	examined := 0
	opts := metav1.ListOptions{Limit: accessRequestListPageSize}
	seed := c.seedRecord(scopeAccessRoles)
	complete := false
	for {
		list, err := c.dyn.Resource(accessRoleGVR).Namespace(c.opts.Namespace).List(ctx, opts)
		if err != nil {
			return nil, categorize("access roles list", err)
		}
		for i := range list.Items {
			seed.add(list.Items[i].Object)
			if name := list.Items[i].GetName(); name != "" {
				roles = append(roles, name)
			}
		}
		examined += len(list.Items)
		if list.GetContinue() == "" {
			complete = true
			break
		}
		if len(roles) >= observe.MaxAccessRoles || examined >= maxRoleCandidates {
			break
		}
		opts.Continue = list.GetContinue()
	}
	seed.commit(complete)
	return roles, nil
}

// convertAccessRequest maps one unstructured request onto bounded,
// source-neutral facts. Only review-relevant fields cross the boundary.
func convertAccessRequest(u *unstructured.Unstructured) observe.AccessRequestFacts {
	content := u.Object
	subject, _, _ := unstructured.NestedString(content, "spec", "subject")
	message, _, _ := unstructured.NestedString(content, "spec", "message")
	rawState, _, _ := unstructured.NestedString(content, "status", "state")
	role, _, _ := unstructured.NestedString(content, "status", "requestedRoleRef", "name")
	decidedBy, _, _ := unstructured.NestedString(content, "status", "decidedBy")
	decidedAtRaw, _, _ := unstructured.NestedString(content, "status", "decidedAt")

	return observe.AccessRequestFacts{
		Name:          u.GetName(),
		UID:           string(u.GetUID()),
		Subject:       subject,
		Message:       message,
		State:         accessRequestState(rawState),
		CreatedAt:     u.GetCreationTimestamp().Time.UTC(),
		RequestedRole: role,
		DecidedBy:     decidedBy,
		DecidedAt:     parseDecidedAt(decidedAtRaw),
	}
}

// accessRequestState maps the reported state onto the closed set. An
// empty state is a freshly created, not-yet-decided request; anything
// outside the set is an explicit unknown, never silently a decision.
func accessRequestState(raw string) observe.AccessRequestState {
	switch raw {
	case "", string(observe.AccessRequestPending):
		return observe.AccessRequestPending
	case string(observe.AccessRequestApproved):
		return observe.AccessRequestApproved
	case string(observe.AccessRequestDenied):
		return observe.AccessRequestDenied
	default:
		return observe.AccessRequestUnknown
	}
}

func parseDecidedAt(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// WatchAccessReview follows the access requests from the seed version.
// Roles are not watched: they are seeded and refresh when the collector
// reseeds.
func (c *Client) WatchAccessReview(ctx context.Context, state observe.AccessReviewState) (observe.AccessReviewWatch, error) {
	w, err := c.dyn.Resource(accessRequestGVR).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{
		ResourceVersion: state.RequestsResourceVersion,
	})
	if err != nil {
		return nil, categorize("access requests watch", err)
	}
	items, stop := fanIn(ctx,
		[]watch.Interface{w},
		[]pump[observe.AccessRequestChange]{tap(c, scopeAccessRequests, pumpAccessRequest)})
	return changeStream[observe.AccessRequestChange]{stream[observe.AccessRequestChange]{items: items, stop: stop}}, nil
}

// pumpAccessRequest converts one access-request watch event. A
// malformed or error event ends the watch; the collector then re-lists
// and republishes only a complete generation.
func pumpAccessRequest(event watch.Event) (observe.AccessRequestChange, bool, bool) {
	var change observe.AccessRequestChange
	u, ok := event.Object.(*unstructured.Unstructured)
	if !ok {
		return change, false, true
	}
	facts := convertAccessRequest(u)
	switch event.Type {
	case watch.Added, watch.Modified:
		change.Put = &facts
	case watch.Deleted:
		change.Delete = &observe.AccessRequestDeletion{Name: facts.Name, UID: facts.UID}
	case watch.Bookmark:
		return change, false, false
	default:
		return change, false, true
	}
	return change, true, false
}
