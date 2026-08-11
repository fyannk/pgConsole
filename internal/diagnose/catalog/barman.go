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

// barmanRules are the claims about backup and archiving through the
// Barman Cloud tooling — messages the barman-cloud commands themselves
// write, whichever process invokes them. They are unpinned
// deliberately: each message has held stable across the releases the
// console has been tested with, and the sidecar's observed version
// (parsed from its image tag) is there to pin against the day one of
// them is reworded.
func barmanRules() []diagnose.Rule {
	const bestEffort = "Read from the container's log while following it. Following is " +
		"best effort, so the count below is a floor and an absence here " +
		"rules nothing out."
	return []diagnose.Rule{
		{
			ID:        "wal-archive-not-empty",
			Component: diagnose.ComponentBarman,
			Severity:  diagnose.SeverityCritical,
			Describes: "the archiver refusing a WAL archive that is not empty",
			Summary: "The configured WAL archive is not empty, so the operator " +
				"refused to start archiving into it.",
			Detail: bestEffort,
			When:   diagnose.LogContains{Substrings: []string{"WAL archive check failed"}},
		},
		{
			ID:        "backup-destination-conflict",
			Component: diagnose.ComponentBarman,
			Severity:  diagnose.SeverityCritical,
			Describes: "a backup destination already holding another cluster's data",
			Summary: "The backup destination already holds data for this server " +
				"name, so the operator refused to write into it.",
			Detail: bestEffort,
			When:   diagnose.LogContains{Substrings: []string{"backup", "already exists"}},
		},
		{
			ID:        "object-store-denied",
			Component: diagnose.ComponentBarman,
			Severity:  diagnose.SeverityCritical,
			Describes: "the object store refusing the configured credentials",
			Summary: "The object store refused the operator's credentials for the " +
				"configured destination.",
			Detail: bestEffort,
			When:   diagnose.LogContains{Substrings: []string{"AccessDenied"}},
		},
		{
			// The same refusal in the other dialect. AWS answers bad
			// credentials with an AccessDenied code; MinIO answers the
			// same request with a bare 403 — observed live as "An error
			// occurred (403) when calling the HeadBucket operation:
			// Forbidden" — and one substring cannot cover both.
			ID:        "object-store-forbidden",
			Component: diagnose.ComponentBarman,
			Severity:  diagnose.SeverityCritical,
			Describes: "the object store answering 403 Forbidden",
			Summary: "The object store answered 403 Forbidden, so the configured " +
				"credentials do not grant this destination.",
			Detail: bestEffort,
			When:   diagnose.LogContains{Substrings: []string{"An error occurred (403)"}},
		},
		{
			ID:        "object-store-unreachable",
			Component: diagnose.ComponentBarman,
			Severity:  diagnose.SeverityCritical,
			Describes: "an unreachable object store endpoint",
			Summary: "The operator could not reach the configured object store " +
				"endpoint.",
			Detail: bestEffort,
			When:   diagnose.LogContains{Substrings: []string{"could not connect to", "endpoint"}},
		},
	}
}
