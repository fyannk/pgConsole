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
	"github.com/fyannk/pgConsole/internal/metrics"
	"github.com/fyannk/pgConsole/internal/observe"
)

type staticObservations []logstream.Observation

func (s staticObservations) Observations() []logstream.Observation { return s }

// clusterInput is an input whose operator status reports the given
// PostgreSQL major version.
func clusterInput(major int) Input {
	return Input{
		Now:        now,
		HasCluster: true,
		Cluster: observe.Snapshot{Cluster: observe.ClusterFacts{
			Present:              true,
			PostgresMajorVersion: &major,
		}},
	}
}

// TestLogRuleReportsWhatTheMatcherSaw proves a log-backed catalog rule
// quotes the matched line, marks the stream best effort, states the
// count as a floor, and links to the tail of the exact container.
func TestLogRuleReportsWhatTheMatcherSaw(t *testing.T) {
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

	finding := findingByID(t, Run(in), "wal-archive-not-empty/orders-1/plugin-barman-cloud")
	if !strings.Contains(finding.Summary, "WAL archive is not empty") {
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

// TestLogRuleWithFollowingOffCannotBeClear is the honesty property. A
// console that is not reading logs has ruled nothing out in them, so
// every log-backed rule reports that it could not run.
func TestLogRuleWithFollowingOffCannotBeClear(t *testing.T) {
	t.Parallel()
	result := Run(Input{Now: now})
	if got := outcomeOf(t, result, "object-store-denied"); got != CheckUnavailable {
		t.Errorf("outcome = %v with following off, want could-not-run", got)
	}
}

// TestLogRuleFollowingOnWithNoMatchIsClear proves the other side: with
// following on and nothing matched, the check is genuinely clear. The
// rule under test is an unpinned one, so the outcome reflects only the
// log stream.
func TestLogRuleFollowingOnWithNoMatchIsClear(t *testing.T) {
	t.Parallel()
	result := Run(Input{Now: now, Logs: staticObservations{}})
	if got := outcomeOf(t, result, "object-store-denied"); got != CheckClear {
		t.Errorf("outcome = %v with following on and no match, want clear", got)
	}
}

// TestPinnedRuleWithUnobservedVersionCannotRun proves the framework's
// own honesty rule: a rule pinned to a version nobody observed answers
// "could not run" with the pin in the reason — never clear, never
// matched.
func TestPinnedRuleWithUnobservedVersionCannotRun(t *testing.T) {
	t.Parallel()
	result := Run(Input{Now: now})
	if got := outcomeOf(t, result, "postgres-eol"); got != CheckUnavailable {
		t.Errorf("outcome = %v with no observed version, want could-not-run", got)
	}
	for _, check := range result.Checks {
		if check.Name == "postgres-eol" && !strings.Contains(check.Because, "PostgreSQL <14") {
			t.Errorf("reason does not state the pin: %q", check.Because)
		}
	}
}

// TestPinnedRuleOutsideItsVersionsDoesNotApply proves the third answer:
// on versions the pin excludes, the rule rules itself out and says so,
// stating both the pin and the observed version.
func TestPinnedRuleOutsideItsVersionsDoesNotApply(t *testing.T) {
	t.Parallel()
	result := Run(clusterInput(17))
	if got := outcomeOf(t, result, "postgres-eol"); got != CheckNotApplicable {
		t.Errorf("outcome = %v on PostgreSQL 17, want does-not-apply", got)
	}
	for _, check := range result.Checks {
		if check.Name == "postgres-eol" {
			if !strings.Contains(check.Because, "17") || !strings.Contains(check.Because, "<14") {
				t.Errorf("reason states neither observed version nor pin: %q", check.Because)
			}
		}
	}
	if len(Run(clusterInput(17)).Findings) != 0 {
		t.Error("a rule that does not apply reported findings")
	}
}

// TestVersionOnlyRuleFiresOnThePinAlone proves a rule with no condition
// matches whenever it applies, and its finding quotes the version fact
// it rests on.
func TestVersionOnlyRuleFiresOnThePinAlone(t *testing.T) {
	t.Parallel()
	finding := findingByID(t, Run(clusterInput(13)), "postgres-eol")
	if finding.Severity != SeverityWarning {
		t.Errorf("severity = %v", finding.Severity)
	}
	if len(finding.Evidence) == 0 {
		t.Fatal("finding carries no evidence")
	}
	if !strings.Contains(finding.Evidence[0].Detail, "13") {
		t.Errorf("evidence does not quote the observed version: %+v", finding.Evidence[0])
	}
	if finding.Evidence[0].Origin != "operator-reported" {
		t.Errorf("evidence origin = %q", finding.Evidence[0].Origin)
	}
}

// TestPinnedLogRuleQuotesTheVersionBeneathTheFinding proves the two
// evidence kinds compose: a pinned, condition-bearing rule quotes the
// observation first and the version fact that made the rule apply after.
func TestPinnedLogRuleQuotesTheVersionBeneathTheFinding(t *testing.T) {
	t.Parallel()
	rule := Rule{
		ID:        "example-pinned",
		Component: ComponentPostgreSQL,
		Requires:  []Requirement{{Component: ComponentPostgreSQL, Constraint: ">=13"}},
		Severity:  SeverityCritical,
		Summary:   "Example.",
		When:      LogContains{Substrings: []string{"boom"}},
	}
	in := clusterInput(17)
	in.Logs = staticObservations{{RuleID: "example-pinned", Pod: "p", Container: "c", Line: "boom", Count: 1}}

	check, findings := evaluateRule(rule, in)
	if check.Outcome != CheckMatched {
		t.Fatalf("outcome = %v, want matched", check.Outcome)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	last := findings[0].Evidence[len(findings[0].Evidence)-1]
	if !strings.Contains(last.Detail, "17") {
		t.Errorf("version fact not quoted beneath the finding: %+v", findings[0].Evidence)
	}
}

// TestEventConditionQuotesTheEvent proves the declarative event
// condition behaves like the hand-written event-backed detectors: the
// finding quotes the API server's words, and an unobserved event window
// is could-not-run.
func TestEventConditionQuotesTheEvent(t *testing.T) {
	t.Parallel()
	rule := Rule{
		ID:       "example-event",
		Severity: SeverityWarning,
		Summary:  "Example.",
		When:     EventMatch{Reasons: []string{"FailedMount"}, MessageContains: []string{"secret"}},
	}

	if check, _ := evaluateRule(rule, Input{Now: now}); check.Outcome != CheckUnavailable {
		t.Errorf("outcome = %v with events unobserved, want could-not-run", check.Outcome)
	}

	in := Input{Now: now, HasEvents: true, Events: observe.EventsSnapshot{Events: []observe.EventFacts{
		{Type: "Warning", Reason: "FailedMount", Kind: "Pod", Object: "orders-1",
			Message: `MountVolume.SetUp failed for volume "certs" : secret "tls" not found`},
		{Type: "Warning", Reason: "FailedMount", Kind: "Pod", Object: "orders-1",
			Message: "unrelated"},
	}}}
	check, findings := evaluateRule(rule, in)
	if check.Outcome != CheckMatched {
		t.Fatalf("outcome = %v, want matched", check.Outcome)
	}
	if len(findings) != 1 || len(findings[0].Evidence) != 1 {
		t.Fatalf("want one finding quoting the one narrowed event, got %+v", findings)
	}
	if !strings.Contains(findings[0].Evidence[0].Detail, `secret "tls" not found`) {
		t.Errorf("event not quoted verbatim: %q", findings[0].Evidence[0].Detail)
	}
}

// TestClusterConditionQuotesTheOperator proves the status-backed
// condition: it matches only the exact type and status, quotes reason
// and message, treats an absent condition as clear, and an unobserved
// or absent Cluster as could-not-run.
func TestClusterConditionQuotesTheOperator(t *testing.T) {
	t.Parallel()
	rule := Rule{
		ID:       "example-condition",
		Severity: SeverityCritical,
		Summary:  "Example.",
		When:     ClusterCondition{Type: "ContinuousArchiving", Status: "False"},
	}

	if check, _ := evaluateRule(rule, Input{Now: now}); check.Outcome != CheckUnavailable {
		t.Errorf("outcome = %v with no Cluster observed, want could-not-run", check.Outcome)
	}
	if check, _ := evaluateRule(rule, Input{Now: now, HasCluster: true}); check.Outcome != CheckUnavailable {
		t.Errorf("outcome = %v with the Cluster absent, want could-not-run", check.Outcome)
	}

	in := clusterInput(17)
	in.Cluster.Cluster.Conditions = []observe.Condition{
		{Type: "Ready", Status: "True"},
		{Type: "ContinuousArchiving", Status: "False", Reason: "Failing",
			Message: "unexpected failure invoking barman-cloud-wal-archive"},
	}
	check, findings := evaluateRule(rule, in)
	if check.Outcome != CheckMatched {
		t.Fatalf("outcome = %v, want matched", check.Outcome)
	}
	detail := findings[0].Evidence[0].Detail
	if !strings.Contains(detail, "Failing") || !strings.Contains(detail, "barman-cloud-wal-archive") {
		t.Errorf("reason and message not quoted: %q", detail)
	}

	// The same condition at True is not the sought status.
	in.Cluster.Cluster.Conditions[1].Status = "True"
	if check, _ := evaluateRule(rule, in); check.Outcome != CheckClear {
		t.Errorf("outcome = %v with the condition True, want clear", check.Outcome)
	}
}

// TestClusterPhaseQuotesPhaseAndReason proves the phase condition
// matches exactly and carries the operator's reason.
func TestClusterPhaseQuotesPhaseAndReason(t *testing.T) {
	t.Parallel()
	rule := Rule{
		ID:       "example-phase",
		Severity: SeverityWarning,
		Summary:  "Example.",
		When:     ClusterPhase{AnyOf: []string{"Cluster in unrecoverable state", "Setting up primary"}},
	}
	in := clusterInput(17)
	in.Cluster.Cluster.Phase = "Cluster in unrecoverable state"
	in.Cluster.Cluster.PhaseReason = "the instances are unrecoverable"

	check, findings := evaluateRule(rule, in)
	if check.Outcome != CheckMatched {
		t.Fatalf("outcome = %v, want matched", check.Outcome)
	}
	detail := findings[0].Evidence[0].Detail
	if !strings.Contains(detail, "unrecoverable state") || !strings.Contains(detail, "the instances are unrecoverable") {
		t.Errorf("phase and reason not quoted: %q", detail)
	}

	in.Cluster.Cluster.Phase = "Cluster in healthy state"
	if check, _ := evaluateRule(rule, in); check.Outcome != CheckClear {
		t.Errorf("outcome = %v on another phase, want clear", check.Outcome)
	}
}

// staticWindow is a MetricsWindow serving fixed instant readings.
type staticWindow map[string]map[string]metrics.Instant

func (w staticWindow) Instances() []string { return nil }
func (w staticWindow) Range(string, metrics.Tier) ([]int64, map[string][]*float64) {
	return nil, nil
}
func (w staticWindow) InstantReadings() map[string]map[string]metrics.Instant { return w }

// TestInstantNonZeroReadsTheScrapedFlag proves the metric-backed
// condition: per-instance findings for non-zero readings, the exporter's
// own metric name and the read time in the evidence, zero and unreported
// readings clear, and no window at all could-not-run.
func TestInstantNonZeroReadsTheScrapedFlag(t *testing.T) {
	t.Parallel()
	rule := Rule{
		ID:       "example-instant",
		Severity: SeverityWarning,
		Summary:  "Example.",
		When:     InstantNonZero{Key: "fencing-on"},
	}

	if check, _ := evaluateRule(rule, Input{Now: now}); check.Outcome != CheckUnavailable {
		t.Errorf("outcome = %v with no metrics window, want could-not-run", check.Outcome)
	}

	in := Input{Now: now, Metrics: staticWindow{
		"orders-1": {"fencing-on": {At: now.Unix(), Value: 1}},
		"orders-2": {"fencing-on": {At: now.Unix(), Value: 0}},
		"orders-3": {},
	}}
	check, findings := evaluateRule(rule, in)
	if check.Outcome != CheckMatched {
		t.Fatalf("outcome = %v, want matched", check.Outcome)
	}
	if len(findings) != 1 || findings[0].ID != "example-instant/orders-1" {
		t.Fatalf("want one finding for the fenced instance, got %+v", findings)
	}
	detail := findings[0].Evidence[0].Detail
	if !strings.Contains(detail, "cnpg_collector_fencing_on") {
		t.Errorf("evidence does not name the exporter's metric: %q", detail)
	}
	if !strings.Contains(detail, "read ") {
		t.Errorf("evidence does not date the reading: %q", detail)
	}

	in.Metrics = staticWindow{"orders-2": {"fencing-on": {At: now.Unix(), Value: 0}}}
	if check, _ := evaluateRule(rule, in); check.Outcome != CheckClear {
		t.Errorf("outcome = %v with the flag at zero, want clear", check.Outcome)
	}
}

// TestCatalogDeclarationsAreComplete is the sanity gate on the data:
// IDs unique across the catalog and the hand-written detectors, every
// rule able to state what it looks for, and every log rule carrying the
// substrings the matcher will be given.
func TestCatalogDeclarationsAreComplete(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, detector := range Detectors() {
		seen[detector.Name()] = true
	}
	for _, rule := range Catalog() {
		if rule.ID == "" || rule.Summary == "" || rule.Component == "" {
			t.Errorf("rule is missing its identity: %+v", rule)
		}
		if seen[rule.ID] {
			t.Errorf("duplicate check name %q", rule.ID)
		}
		seen[rule.ID] = true
		if ruleDescribes(rule) == "" {
			t.Errorf("rule %q cannot state what it looks for", rule.ID)
		}
		if rule.When == nil && len(rule.Requires) == 0 {
			t.Errorf("rule %q has neither a condition nor a pin, so it would always fire", rule.ID)
		}
		if condition, ok := rule.When.(LogContains); ok && len(condition.Substrings) == 0 {
			t.Errorf("log rule %q has no substrings, so it could never match", rule.ID)
		}
	}
}

// TestLogRulesMirrorTheCatalog proves the matcher is fed from the same
// declarations the evaluator reads: every log-backed rule appears, under
// its own ID, with its own substrings.
func TestLogRulesMirrorTheCatalog(t *testing.T) {
	t.Parallel()
	derived := map[string]logstream.Rule{}
	for _, rule := range LogRules() {
		derived[rule.ID] = rule
	}
	for _, rule := range Catalog() {
		condition, ok := rule.When.(LogContains)
		if !ok {
			continue
		}
		matcher, present := derived[rule.ID]
		if !present {
			t.Errorf("log rule %q not derived for the matcher", rule.ID)
			continue
		}
		if len(matcher.Contains) != len(condition.Substrings) {
			t.Errorf("rule %q substrings diverge: %v vs %v", rule.ID, matcher.Contains, condition.Substrings)
		}
		delete(derived, rule.ID)
	}
	for id := range derived {
		t.Errorf("matcher rule %q has no catalog declaration", id)
	}
}
