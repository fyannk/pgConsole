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

// Package cnpg is the CloudNativePG rule catalog: the states in which
// the operator is stuck, waiting, or refusing, and why. One file per
// evidence kind — phases, conditions, events, metrics, backups, logs.
//
// Every string here was read out of the operator's own source, and
// every rule is pinned to the span of releases that reading was
// actually performed against — currently 1.28.4, 1.29.2 and 1.30.0.
// The pin is the claim's scope: a rule pinned since128 was verified
// verbatim in all three trees; only130 marks machinery (the primary
// lease, the invalid-definition phase) that does not exist before 1.30.
// Verifying another release means checking the strings in its tree and
// widening the spans, not trusting that nothing moved.
//
// The operator version itself is observed, not assumed — parsed from
// the bootstrap-controller init container the operator injects into
// every instance pod. On an unverified release these checks answer
// "does not apply", never a false clear and never a wrong finding.
package cnpg

import "github.com/fyannk/pgConsole/internal/diagnose"

// Rules is the full CloudNativePG catalog.
func Rules() []diagnose.Rule {
	var rules []diagnose.Rule
	rules = append(rules, phaseRules()...)
	rules = append(rules, conditionRules()...)
	rules = append(rules, eventRules()...)
	rules = append(rules, metricRules()...)
	rules = append(rules, backupRules()...)
	rules = append(rules, databaseRules()...)
	rules = append(rules, logRules()...)
	rules = append(rules, resourceRules()...)
	return rules
}

// VerifiedReleases are the operator releases whose source trees the
// catalog's strings were read from, verbatim. The pin verification
// (make verify-pins) fetches each tree and greps every pinned rule's
// strings in it; widening a span means adding the release here and
// letting that check pass, not trusting that nothing moved.
var VerifiedReleases = []string{"1.28.4", "1.29.2", "1.30.0"}

// The verified spans. A span is widened only by verifying the strings
// in another release's tree.
const (
	// since128: verified verbatim in 1.28.4, 1.29.2 and 1.30.0.
	since128 = ">=1.28 <1.31"
	// since129: absent from 1.28.4, verified in 1.29.2 and 1.30.0.
	since129 = ">=1.29 <1.31"
	// only130: machinery that first appears in 1.30.0.
	only130 = ">=1.30 <1.31"
)

// pin builds the requirement list for one verified span.
func pin(constraint string) []diagnose.Requirement {
	return []diagnose.Requirement{{Component: diagnose.ComponentCNPG, Constraint: constraint}}
}

// streamCaveat is the standing qualifier on every log-backed finding.
const streamCaveat = "Read from the container's log while following it. Following is " +
	"best effort, so the count below is a floor and an absence here " +
	"rules nothing out."
