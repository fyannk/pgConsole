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
	"time"

	"github.com/fyannk/pgConsole/internal/diagnose"
)

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
			Pinned:    []string{"error_severity"},
			Severity:  diagnose.SeverityWarning,
			Describes: "a server log record with FATAL severity",
			Summary:   "PostgreSQL logged a FATAL-severity record.",
			Detail: "FATAL ends one backend, not the server, and routine failures — a " +
				"wrong password, a refused connection during startup — log at this " +
				"severity too; the quoted record says which this is. Read from the " +
				"container's log while following it, best effort: the count below is " +
				"a floor and an absence here rules nothing out.",
			When: diagnose.LogContains{
				Substrings: []string{`"error_severity":"FATAL"`},
				// PostgreSQL stamps routine lifecycle refusals FATAL too,
				// and the operator's own probes provoke them on every
				// start: a connection during startup, shutdown, or crash
				// recovery is refused at this severity, and an orderly
				// switchover terminates backends with it. Reporting those
				// would fire on every restart of every cluster forever,
				// which is how a screen teaches people to ignore it. The
				// messages are verbatim server strings, stable across the
				// pinned majors.
				Except: []string{
					"the database system is starting up",
					"the database system is not yet accepting connections",
					"the database system is shutting down",
					"the database system is in recovery mode",
					"terminating connection due to administrator command",
				},
			},
		},
		{
			ID:        "postgres-panic",
			Component: diagnose.ComponentPostgreSQL,
			Requires: []diagnose.Requirement{
				{Component: diagnose.ComponentCNPG, Constraint: ">=1.28 <1.31"}},
			Pinned:    []string{"error_severity"},
			Severity:  diagnose.SeverityCritical,
			Describes: "a server log record with PANIC severity",
			Summary:   "PostgreSQL panicked, which ends the whole server process.",
			Detail: "A panic crashes the postmaster and forces crash recovery. Read from " +
				"the container's log while following it, best effort: the count below " +
				"is a floor and an absence here rules nothing out.",
			When: diagnose.LogContains{Substrings: []string{`"error_severity":"PANIC"`}},
			// The panic is the server on one instance failing to write WAL, so
			// the relation is pinned to the pod and to the hour: both lines
			// carry a time, and a disk-full line hours old on another
			// instance explains nothing here.
			ConsequenceOf: []diagnose.Relation{
				{Cause: "cnpg-wal-disk-full", Scope: diagnose.ScopePod, Within: time.Hour}},
		},
		{
			// The threshold is pinned knowledge, like the EOL boundary:
			// transaction ids wrap at two billion usable values, and
			// PostgreSQL stops accepting writes with three million left.
			// At 1.6 billion the cluster still works, which is exactly
			// why the number deserves a finding — nothing else looks
			// wrong while the headroom runs out. The metric name is the
			// CloudNativePG exporter's, hence the operator pin.
			ID:        "postgres-xid-wraparound",
			Component: diagnose.ComponentPostgreSQL,
			Requires: []diagnose.Requirement{
				{Component: diagnose.ComponentCNPG, Constraint: ">=1.28 <1.31"}},
			Pinned:    []string{"xid_age"},
			Severity:  diagnose.SeverityCritical,
			Describes: "a database's transaction-id age near wraparound",
			Summary:   "A database's transaction-id age is approaching wraparound, which ends in PostgreSQL refusing writes.",
			Detail: "Something is holding the oldest transaction horizon — a forgotten " +
				"open transaction, a stale replication slot, a failing autovacuum — " +
				"and the age below leaves under a quarter of the headroom. If it " +
				"reaches the hard limit, PostgreSQL stops accepting writes " +
				"cluster-wide until a vacuum completes. The boundary is " +
				"console-pinned knowledge; the reading is the exporter's.",
			When: diagnose.SeriesAbove{Key: "xid-age", Threshold: 1_600_000_000},
		},
		{
			ID:        "postgres-mxid-wraparound",
			Component: diagnose.ComponentPostgreSQL,
			Requires: []diagnose.Requirement{
				{Component: diagnose.ComponentCNPG, Constraint: ">=1.28 <1.31"}},
			Pinned:    []string{"mxid_age"},
			Severity:  diagnose.SeverityCritical,
			Describes: "a database's multixact-id age near wraparound",
			Summary:   "A database's multixact-id age is approaching wraparound, which ends in PostgreSQL refusing writes.",
			Detail: "The multixact counter wraps exactly like the transaction counter " +
				"and is exhausted by the same causes, plus heavy row-level sharing. " +
				"The same consequence applies: past the hard limit, writes stop " +
				"until vacuum catches up.",
			When: diagnose.SeriesAbove{Key: "mxid-age", Threshold: 1_600_000_000},
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
