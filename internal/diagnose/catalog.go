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

package diagnose

// Catalog is the declarative half of diagnostics: every rule, grouped
// into one file per component — catalog_cnpg.go, catalog_postgres.go,
// catalog_barman.go, catalog_kubernetes.go — so the question "what do we
// claim about Barman, and on which versions" has one place to be
// answered.
//
// Adding a diagnostic means adding a Rule to the component's file:
// declare the version pins the claim is tested against, the observation
// (a log line, an event), and the finding it means. The evaluator turns
// it into a check row with the same honesty contract as the hand-written
// detectors — plus one more answer those never need: "does not apply",
// for a rule whose pins exclude the observed versions.
//
// A rule earns its place the same way a logstream rule always has: by
// catching something no object status reports. Anything visible in a
// status, condition, or event field belongs in an EventMatch rule or a
// hand-written detector, where it does not depend on message text.
func Catalog() []Rule {
	var rules []Rule
	rules = append(rules, cnpgRules()...)
	rules = append(rules, postgresRules()...)
	rules = append(rules, barmanRules()...)
	rules = append(rules, kubernetesRules()...)
	return rules
}
