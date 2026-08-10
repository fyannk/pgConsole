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

// cnpgRules are the claims about the CloudNativePG operator itself.
//
// Empty today, and the reason is a constraint worth recording: the
// operator runs outside this console's namespaced authority, so its
// version is not yet in any snapshot and versionFacts leaves
// ComponentCNPG unknown. A rule pinned to it would evaluate to "could
// not run: the CloudNativePG version is not observed" on every run —
// honest, but a standing row that can never resolve is noise, so the
// first pinned rule should land together with a version source.
// Everything else is already in place: pin with
//
//	Requires: []Requirement{{Component: ComponentCNPG, Constraint: ">=1.24 <1.27"}}
//
// and the evaluator gates the rule, states the pin on its check row,
// and quotes the observed version beneath any finding.
func cnpgRules() []Rule {
	return nil
}
