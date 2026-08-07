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
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/observe"
)

// eventGVR is the core events resource. The core v1 API is the recorded
// choice: core and events.k8s.io expose the same underlying objects.
var eventGVR = schema.GroupVersionResource{Version: "v1", Resource: "events"}

// eventListPageSize bounds a single list page.
const eventListPageSize = 200

// maxEventMessage bounds an event message at the boundary.
const maxEventMessage = 1024

// FetchEvents lists the namespace's events and keeps the cluster's
// candidates. RBAC cannot scope an event list below the namespace, so
// every event transits this filter — the documented honest scope; only
// candidates are retained. Pod-kind candidates are selected by the
// cluster name prefix here and decided by membership at rendering.
func (c *Client) FetchEvents(ctx context.Context) ([]observe.EventFacts, string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	var events []observe.EventFacts
	opts := metav1.ListOptions{Limit: eventListPageSize}
	rv := ""
	for {
		list, err := c.dyn.Resource(eventGVR).Namespace(c.opts.Namespace).List(ctx, opts)
		if err != nil {
			return nil, "", categorize("events list", err)
		}
		rv = list.GetResourceVersion()
		for i := range list.Items {
			facts, candidate := c.convertEvent(list.Items[i].Object)
			if candidate {
				events = append(events, facts)
			}
		}
		if list.GetContinue() == "" || len(events) > observe.MaxEventsRetained {
			break
		}
		opts.Continue = list.GetContinue()
	}
	return events, rv, nil
}

// WatchEvents starts a namespace event watch delivering candidates.
func (c *Client) WatchEvents(ctx context.Context, fromResourceVersion string) (observe.EventWatch, error) {
	w, err := c.dyn.Resource(eventGVR).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{
		ResourceVersion: fromResourceVersion,
	})
	if err != nil {
		return nil, categorize("events watch", err)
	}
	items, stop := fanIn(ctx,
		[]watch.Interface{w},
		[]pump[observe.EventChange]{c.pumpEvent})
	return changeStream[observe.EventChange]{stream[observe.EventChange]{items: items, stop: stop}}, nil
}

// pumpEvent converts one event watch event. Unlike the single-object
// cluster watch, one malformed event object is dropped rather than
// ending the stream: the surrounding events remain honest on their own.
func (c *Client) pumpEvent(event watch.Event) (observe.EventChange, bool, bool) {
	obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
	if !ok {
		return observe.EventChange{}, false, event.Type == watch.Error
	}
	facts, candidate := c.convertEvent(obj.UnstructuredContent())
	if !candidate {
		return observe.EventChange{}, false, false
	}
	switch event.Type {
	case watch.Added, watch.Modified:
		return observe.EventChange{Put: &facts}, true, false
	case watch.Deleted:
		return observe.EventChange{Delete: &observe.EventDeletion{Name: facts.Name, UID: facts.UID}}, true, false
	case watch.Bookmark:
		return observe.EventChange{}, false, false
	default:
		return observe.EventChange{}, false, true
	}
}

// convertEvent converts a raw event and reports whether it is a
// candidate of the cluster. A malformed event is not a candidate.
func (c *Client) convertEvent(content map[string]any) (observe.EventFacts, bool) {
	var event corev1.Event
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &event); err != nil {
		return observe.EventFacts{}, false
	}
	if !c.eventIsCandidate(&event) {
		return observe.EventFacts{}, false
	}

	message := event.Message
	if len(message) > maxEventMessage {
		message = message[:maxEventMessage]
	}
	count := int(event.Count)
	if event.Series != nil && int(event.Series.Count) > count {
		count = int(event.Series.Count)
	}
	if count < 1 {
		count = 1
	}

	return observe.EventFacts{
		Name:     event.Name,
		UID:      string(event.UID),
		Kind:     event.InvolvedObject.Kind,
		Object:   event.InvolvedObject.Name,
		Type:     event.Type,
		Reason:   event.Reason,
		Message:  message,
		Count:    count,
		LastSeen: eventTime(&event),
	}, true
}

// eventIsCandidate keeps events involving the cluster object itself, or
// Pod-kind objects carrying the cluster name prefix. The prefix is
// selection only; rendering admits pod events solely for verified
// members.
func (c *Client) eventIsCandidate(event *corev1.Event) bool {
	involved := event.InvolvedObject
	if involved.Namespace != "" && involved.Namespace != c.opts.Namespace {
		return false
	}
	group, _, ok := splitAPIVersion(involved.APIVersion)
	if involved.Kind == "Cluster" && ok && group == clusterGVR.Group {
		return involved.Name == c.opts.ClusterName
	}
	if involved.Kind == "Pod" {
		return involved.Name == c.opts.ClusterName ||
			strings.HasPrefix(involved.Name, c.opts.ClusterName+"-")
	}
	return false
}

// eventTime returns the most recent occurrence time the API server
// reports, falling back through the event's timestamp fields.
func eventTime(event *corev1.Event) time.Time {
	switch {
	case !event.LastTimestamp.IsZero():
		return event.LastTimestamp.Time
	case event.Series != nil && !event.Series.LastObservedTime.IsZero():
		return event.Series.LastObservedTime.Time
	case !event.EventTime.IsZero():
		return event.EventTime.Time
	case !event.FirstTimestamp.IsZero():
		return event.FirstTimestamp.Time
	default:
		return event.CreationTimestamp.Time
	}
}
