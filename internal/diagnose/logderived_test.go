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
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/logstream"
)

type staticObservations []logstream.Observation

func (s staticObservations) Observations() []logstream.Observation { return s }

// TestLogDetectorReportsWhatTheMatcherSaw proves the finding quotes the
// log line and links to the tail of the exact container it came from.
func TestLogDetectorReportsWhatTheMatcherSaw(t *testing.T) {
	t.Parallel()
	in := Input{Now: now, Logs: staticObservations{{
		RuleID:    "wal-archive-not-empty",
		Summary:   "The configured WAL archive is not empty.",
		Pod:       "orders-1",
		Container: "plugin-barman-cloud",
		Line:      "ERROR: WAL archive check failed for server orders",
		FirstSeen: now.Add(-time.Hour),
		LastSeen:  now,
		Count:     7,
	}}}

	finding := findingByID(t, Run(in), "log/wal-archive-not-empty/orders-1/plugin-barman-cloud")
	if finding.Summary != "The configured WAL archive is not empty." {
		t.Errorf("summary = %q", finding.Summary)
	}
	if finding.Evidence[0].Detail != "ERROR: WAL archive check failed for server orders" {
		t.Errorf("line not quoted verbatim: %q", finding.Evidence[0].Detail)
	}
	// The origin must say the evidence is best effort: a stream cannot
	// promise completeness the way a snapshot can.
	if !strings.Contains(finding.Evidence[0].Origin, "best effort") {
		t.Errorf("origin does not mark the stream best effort: %q", finding.Evidence[0].Origin)
	}
	if !strings.Contains(finding.Evidence[1].Detail, "at least 7") {
		t.Errorf("count is not stated as a floor: %q", finding.Evidence[1].Detail)
	}
	if finding.Link != "/logs/orders-1/plugin-barman-cloud" {
		t.Errorf("link = %q, want the tail of the container it came from", finding.Link)
	}
}

// TestLogDetectorWithFollowingOffCannotBeClear is the honesty property.
// A console that is not reading logs has ruled nothing out in them, so
// the check reports that it could not run rather than coming back clear.
func TestLogDetectorWithFollowingOffCannotBeClear(t *testing.T) {
	t.Parallel()
	result := Run(Input{Now: now})
	if got := outcomeOf(t, result, "log-messages"); got != CheckUnavailable {
		t.Errorf("outcome = %v with following off, want could-not-run", got)
	}
}

// TestLogDetectorFollowingOnWithNoMatchIsClear proves the other side:
// with following on and nothing matched, the check is genuinely clear.
func TestLogDetectorFollowingOnWithNoMatchIsClear(t *testing.T) {
	t.Parallel()
	result := Run(Input{Now: now, Logs: staticObservations{}})
	if got := outcomeOf(t, result, "log-messages"); got != CheckClear {
		t.Errorf("outcome = %v with following on and no match, want clear", got)
	}
}
