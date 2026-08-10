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

import "github.com/fyannk/pgConsole/internal/diagnose"

// postgresRules are the claims about PostgreSQL itself.
func postgresRules() []diagnose.Rule {
	return []diagnose.Rule{
		{
			// The substring is the JSON field CloudNativePG's log pipe
			// wraps every PostgreSQL server record in, which is why a
			// PostgreSQL rule carries a CloudNativePG pin: the message is
			// the database's, the envelope is the operator's. Matching
			// the envelope rather than a bare "FATAL:" keeps quoted
			// errors in other components' lines from counting as the
			// server speaking. The log pipe's field is verbatim-identical
			// across the verified releases 1.28.4, 1.29.2 and 1.30.0.
			ID:        "postgres-fatal",
			Component: diagnose.ComponentPostgreSQL,
			Requires: []diagnose.Requirement{
				{Component: diagnose.ComponentCNPG, Constraint: ">=1.28 <1.31"}},
			Severity:  diagnose.SeverityWarning,
			Describes: "a server log record with FATAL severity",
			Summary:   "PostgreSQL logged a FATAL-severity record.",
			Detail: "FATAL ends one backend, not the server, and routine failures — a " +
				"wrong password, a refused connection during startup — log at this " +
				"severity too; the quoted record says which this is. Read from the " +
				"container's log while following it, best effort: the count below is " +
				"a floor and an absence here rules nothing out.",
			When: diagnose.LogContains{Substrings: []string{`"error_severity":"FATAL"`}},
		},
		{
			ID:        "postgres-panic",
			Component: diagnose.ComponentPostgreSQL,
			Requires: []diagnose.Requirement{
				{Component: diagnose.ComponentCNPG, Constraint: ">=1.28 <1.31"}},
			Severity:  diagnose.SeverityCritical,
			Describes: "a server log record with PANIC severity",
			Summary:   "PostgreSQL panicked, which ends the whole server process.",
			Detail: "A panic crashes the postmaster and forces crash recovery. Read from " +
				"the container's log while following it, best effort: the count below " +
				"is a floor and an absence here rules nothing out.",
			When: diagnose.LogContains{Substrings: []string{`"error_severity":"PANIC"`}},
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
			Component: diagnose.ComponentPostgreSQL,
			Requires: []diagnose.Requirement{
				{Component: diagnose.ComponentPostgreSQL, Constraint: "<14"}},
			Severity:  diagnose.SeverityWarning,
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
