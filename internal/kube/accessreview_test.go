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
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/fyannk/pgConsole/internal/observe"
)

func TestConvertAccessRequestReadsReviewFields(t *testing.T) {
	t.Parallel()
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "pgtoolbox.fyannk.dev/v1alpha1",
		"kind":       "PgToolBoxAccessRequest",
		"metadata":   map[string]any{"name": "req-1", "uid": "u-1", "creationTimestamp": "2026-07-29T08:00:00Z"},
		"spec":       map[string]any{"subject": "alice@corp", "message": "need access"},
		"status": map[string]any{
			"state":          "approved",
			"requestedLevel": "poweruser",
			"decidedBy":      "dba@corp",
			"decidedAt":      "2026-07-29T09:30:00Z",
		},
	}}
	got := convertAccessRequest(u)
	if got.Name != "req-1" || got.UID != "u-1" || got.Subject != "alice@corp" || got.Message != "need access" {
		t.Errorf("metadata/spec = %+v", got)
	}
	if got.State != observe.AccessRequestApproved || got.RequestedLevel != "poweruser" || got.DecidedBy != "dba@corp" {
		t.Errorf("status = %+v", got)
	}
	if got.DecidedAt == nil || got.DecidedAt.Hour() != 9 {
		t.Errorf("decidedAt = %v", got.DecidedAt)
	}
}

// TestAccessRequestStateClosedSet proves the state maps onto the closed
// set: an empty state is a pending request, an off-set value is unknown,
// never silently a decision.
func TestAccessRequestStateClosedSet(t *testing.T) {
	t.Parallel()
	cases := map[string]observe.AccessRequestState{
		"":         observe.AccessRequestPending,
		"pending":  observe.AccessRequestPending,
		"approved": observe.AccessRequestApproved,
		"denied":   observe.AccessRequestDenied,
		"Approved": observe.AccessRequestUnknown,
		"granted":  observe.AccessRequestUnknown,
	}
	for raw, want := range cases {
		if got := accessRequestState(raw); got != want {
			t.Errorf("accessRequestState(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestConvertAccessRequestPendingWhenNoStatus proves a freshly created
// request with no status is pending with no decision fields.
func TestConvertAccessRequestPendingWhenNoStatus(t *testing.T) {
	t.Parallel()
	u := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "req-2"},
		"spec":     map[string]any{"subject": "bob@corp"},
	}}
	got := convertAccessRequest(u)
	if !got.Pending() {
		t.Errorf("state = %q, want pending", got.State)
	}
	if got.DecidedAt != nil || got.DecidedBy != "" || got.RequestedLevel != "" {
		t.Errorf("undecided request carries decision fields: %+v", got)
	}
}
