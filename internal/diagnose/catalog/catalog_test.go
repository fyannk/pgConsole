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

package catalog

import (
	"testing"

	"github.com/fyannk/pgConsole/internal/diagnose"
)

// TestCatalogDeclarationsAreComplete is the sanity gate on the data:
// IDs unique across the catalog and the hand-written detectors, every
// rule able to state what it looks for, and every log rule carrying the
// substrings the matcher will be given.
func TestCatalogDeclarationsAreComplete(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, detector := range diagnose.Detectors() {
		seen[detector.Name()] = true
	}
	for _, rule := range Rules() {
		if rule.ID == "" || rule.Summary == "" || rule.Component == "" {
			t.Errorf("rule is missing its identity: %+v", rule)
		}
		if seen[rule.ID] {
			t.Errorf("duplicate check name %q", rule.ID)
		}
		seen[rule.ID] = true
		if rule.Describes == "" && rule.When == nil {
			t.Errorf("rule %q cannot state what it looks for", rule.ID)
		}
		if rule.When == nil && len(rule.Requires) == 0 {
			t.Errorf("rule %q has neither a condition nor a pin, so it would always fire", rule.ID)
		}
		if condition, ok := rule.When.(diagnose.LogContains); ok && len(condition.Substrings) == 0 {
			t.Errorf("log rule %q has no substrings, so it could never match", rule.ID)
		}
	}
}

// TestLogRulesMirrorTheCatalog proves the matcher is fed from the same
// declarations the evaluator reads: every log-backed rule appears, under
// its own ID, with its own substrings.
func TestLogRulesMirrorTheCatalog(t *testing.T) {
	t.Parallel()
	derived := map[string]int{}
	for _, rule := range LogRules() {
		derived[rule.ID] = len(rule.Contains)
	}
	for _, rule := range Rules() {
		condition, ok := rule.When.(diagnose.LogContains)
		if !ok {
			continue
		}
		substrings, present := derived[rule.ID]
		if !present {
			t.Errorf("log rule %q not derived for the matcher", rule.ID)
			continue
		}
		if substrings != len(condition.Substrings) {
			t.Errorf("rule %q substring count diverges", rule.ID)
		}
		delete(derived, rule.ID)
	}
	for id := range derived {
		t.Errorf("matcher rule %q has no catalog declaration", id)
	}
}
