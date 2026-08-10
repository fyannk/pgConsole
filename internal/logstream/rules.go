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

package logstream

// DefaultRules is the closed set of log lines the console looks for.
//
// It is small on purpose. A rule here is a claim that a fixed string
// means a specific thing, and that claim is pinned to the tested
// CloudNativePG tuple: upstream can reword a message in any release, at
// which point the rule silently stops matching. A quiet rule is a
// finding nobody gets rather than a wrong one, which is the right way
// round — but it is still a cost, so each rule has to earn its place by
// catching something no object status reports.
//
// Everything here is a failure the API server and the operator's own
// resources do not describe. Anything visible in a status, a condition,
// or an event belongs in a snapshot-backed detector instead, where it
// does not depend on message text at all.
func DefaultRules() []Rule {
	return []Rule{
		{
			ID:       "wal-archive-not-empty",
			Contains: []string{"WAL archive check failed"},
			Summary: "The configured WAL archive is not empty, so the operator " +
				"refused to start archiving into it.",
		},
		{
			ID:       "backup-destination-conflict",
			Contains: []string{"backup", "already exists"},
			Summary: "The backup destination already holds data for this server " +
				"name, so the operator refused to write into it.",
		},
		{
			ID:       "object-store-denied",
			Contains: []string{"AccessDenied"},
			Summary: "The object store refused the operator's credentials for the " +
				"configured destination.",
		},
		{
			ID:       "object-store-unreachable",
			Contains: []string{"could not connect to", "endpoint"},
			Summary: "The operator could not reach the configured object store " +
				"endpoint.",
		},
		{
			ID:       "postgres-fatal",
			Contains: []string{"FATAL:"},
			Summary:  "PostgreSQL reported a fatal error.",
		},
		{
			ID:       "postgres-panic",
			Contains: []string{"PANIC:"},
			Summary:  "PostgreSQL reported a panic, which ends the process.",
		},
	}
}
