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

import (
	"fmt"

	"github.com/fyannk/pgConsole/internal/evidence"
)

// RepositoryAspect names one typed state the repository-evidence sidecar
// reports about the barman-cloud repository.
type RepositoryAspect string

// The aspects the sidecar reports as four-state facts.
const (
	// RepositoryWAL is the archived WAL sequence's continuity.
	RepositoryWAL RepositoryAspect = "WAL continuity"
	// RepositoryCoverage is the observed recovery coverage.
	RepositoryCoverage RepositoryAspect = "recovery coverage"
	// RepositoryRetention is the retention comparison.
	RepositoryRetention RepositoryAspect = "retention"
)

// RepositoryState matches when the repository-evidence sidecar reports
// one aspect of the repository in one of the given states. It restates
// the sidecar's typed state and stable reason code and nothing else:
// the sidecar's free-text diagnostics never reach this package, and the
// finding never merges the observation into an operator claim or says
// what the repository would or would not restore.
type RepositoryState struct {
	Aspect RepositoryAspect
	// States are the sidecar's states that match; "unhealthy" alone when
	// empty.
	States []string
}

func (c RepositoryState) describe() string {
	states := c.States
	if len(states) == 0 {
		states = []string{"unhealthy"}
	}
	described := "the repository-evidence sidecar reporting " + string(c.Aspect) + " as " + states[0]
	for _, state := range states[1:] {
		described += " or " + state
	}
	return described
}

// fact picks the aspect's state from a recognised report.
func (c RepositoryAspect) fact(barman *evidence.BarmanFacts) (evidence.StateFact, bool) {
	switch c {
	case RepositoryWAL:
		return barman.WAL, true
	case RepositoryCoverage:
		return barman.Coverage, true
	case RepositoryRetention:
		return barman.Retention.Result, true
	}
	return evidence.StateFact{}, false
}

func (c RepositoryState) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if reason := evidenceUnavailable(in); reason != "" {
		return nil, reason
	}
	report := in.Evidence.Snapshot.Report
	fact, known := c.Aspect.fact(report.Barman)
	if !known {
		return nil, "the catalog names a repository aspect this console does not read"
	}
	states := c.States
	if len(states) == 0 {
		states = []string{"unhealthy"}
	}
	matched := false
	for _, state := range states {
		if fact.State == state {
			matched = true
			break
		}
	}
	if !matched {
		return nil, ""
	}
	detail := fmt.Sprintf("state %s, reason code %s; evidence generation %d", fact.State, fact.Code, report.EvidenceGeneration)
	if report.CompletedAt != nil {
		detail += ", scan completed " + report.CompletedAt.UTC().Format("2006-01-02 15:04:05Z")
	}
	if report.Completeness == "incomplete" {
		detail += " (the scan is incomplete, so the state rests on what was examined)"
	}
	match := conditionMatch{
		subject: EntityRef{Kind: "Repository", Name: report.ScopeName},
		evidence: []Evidence{{
			Origin: "repository-evidence",
			Object: "repository " + string(c.Aspect),
			Detail: detail,
		}},
		link:      "/backups/evidence",
		linkLabel: "Repository evidence",
	}
	if report.CompletedAt != nil {
		match.at = *report.CompletedAt
	}
	return []conditionMatch{match}, ""
}
