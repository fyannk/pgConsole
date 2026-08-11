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

import "testing"

// TestConvertQuotaComputesExhaustionAtTheBoundary pins the one line the
// quota-exhausted verdict rests on: used against hard, compared as
// quantities — so "1Gi" against "1024Mi" is equality, not a string
// mismatch — with exhaustion meaning at-or-over the ceiling, and an
// unreported usage never counting as exhausted.
func TestConvertQuotaComputesExhaustionAtTheBoundary(t *testing.T) {
	t.Parallel()
	facts, err := convertQuota(map[string]any{
		"metadata": map[string]any{"name": "tight", "uid": "u1"},
		"status": map[string]any{
			"hard": map[string]any{
				"requests.storage": "1Gi",
				"pods":             "3",
				"count/jobs.batch": "5",
				"limits.memory":    "2Gi",
			},
			"used": map[string]any{
				"requests.storage": "1024Mi", // equal, in another unit
				"pods":             "4",      // over
				"count/jobs.batch": "2",      // under
				// limits.memory unreported
			},
		},
	})
	if err != nil {
		t.Fatalf("convertQuota: %v", err)
	}
	if facts.Name != "tight" || len(facts.Resources) != 4 {
		t.Fatalf("facts = %+v", facts)
	}

	want := map[string]bool{
		"requests.storage": true,  // at the ceiling is exhausted
		"pods":             true,  // over it certainly is
		"count/jobs.batch": false, // headroom
		"limits.memory":    false, // unreported usage claims nothing
	}
	for _, line := range facts.Resources {
		if line.Exhausted != want[line.Resource] {
			t.Errorf("%s: exhausted = %v (hard %q, used %q), want %v",
				line.Resource, line.Exhausted, line.Hard, line.Used, want[line.Resource])
		}
	}

	// Quantities render canonically — "1024Mi" reads back as "1Gi" —
	// which is the same value in the form Kubernetes itself normalizes
	// to. The verdict beside it is what the reader checks.
	for _, line := range facts.Resources {
		if line.Resource == "requests.storage" && line.Used != "1Gi" {
			t.Errorf("used = %q, want the canonical rendering 1Gi", line.Used)
		}
	}
}
