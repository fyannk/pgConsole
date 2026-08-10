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

// postgresRules are the claims about PostgreSQL itself.
func postgresRules() []Rule {
	return []Rule{
		{
			ID:        "postgres-fatal",
			Component: ComponentPostgreSQL,
			Severity:  SeverityCritical,
			Describes: "a FATAL message in a followed container log",
			Summary:   "PostgreSQL reported a fatal error.",
			Detail: "Read from the container's log while following it. Following is " +
				"best effort: a stream breaks on every container restart and " +
				"Kubernetes cannot report what was emitted in between, so the " +
				"count below is a floor and an absence here rules nothing out.",
			When: LogContains{Substrings: []string{"FATAL:"}},
		},
		{
			ID:        "postgres-panic",
			Component: ComponentPostgreSQL,
			Severity:  SeverityCritical,
			Describes: "a PANIC message in a followed container log",
			Summary:   "PostgreSQL reported a panic, which ends the process.",
			Detail: "Read from the container's log while following it. Following is " +
				"best effort, so the count below is a floor and an absence here " +
				"rules nothing out.",
			When: LogContains{Substrings: []string{"PANIC:"}},
		},
		{
			// The one rule where the version pin is the whole diagnostic:
			// there is no observation to wait for, because running the
			// version is the finding. The end-of-life boundary is pinned
			// knowledge — the PostgreSQL project retires each major five
			// years after release, and 13, the last major before 14,
			// left support in November 2025 — and pinned knowledge is
			// exactly what a catalog rule is for, stated as the console's
			// own claim rather than smuggled in as an observation.
			ID:        "postgres-eol",
			Component: ComponentPostgreSQL,
			Requires:  []Requirement{{Component: ComponentPostgreSQL, Constraint: "<14"}},
			Severity:  SeverityWarning,
			Describes: "a PostgreSQL major version past upstream end of life",
			Summary:   "The PostgreSQL major version no longer receives upstream releases.",
			Detail: "Majors before 14 left the PostgreSQL project's five-year support " +
				"window by November 2025, so bug and security fixes are no longer " +
				"published for them. The boundary is console-pinned knowledge, " +
				"current as of this console release; the version it is applied to " +
				"is quoted below.",
			Link:      "/",
			LinkLabel: "Overview",
		},
	}
}
