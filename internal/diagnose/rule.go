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
	"strings"

	"github.com/fyannk/pgConsole/internal/logstream"
)

// Rule is one catalog diagnostic, written as data: on the pinned
// versions, this observation means this finding. The prose a hand-written
// detector carries in code — what it looks for, what a match means,
// where the reader goes next — a rule carries in fields, which is what
// makes adding one a declaration rather than a program.
//
// Versioning is first-class because the claim is version-scoped whether
// the author says so or not: upstream can reword a log line or change a
// behaviour in any release. A pin makes that scope explicit, and the
// evaluator turns it into one of three honest answers per rule — it
// matched, it does not apply to the observed versions, or it could not
// be evaluated because a pinned version is not observed.
type Rule struct {
	// ID is the stable identifier findings are reported under. Unique
	// across the catalog and the hand-written detectors.
	ID string
	// Component is the part of the stack the rule is about. It groups
	// the catalog files and prefixes nothing: applicability comes only
	// from Requires.
	Component Component
	// Requires are the version pins, all of which must hold. Empty means
	// the rule applies to every version the console encounters — a claim
	// the author makes by leaving it empty, not a default.
	Requires []Requirement
	// Severity of the finding when the rule matches.
	Severity Severity
	// Describes states what the rule looks for, for the check row.
	Describes string
	// Summary is the finding's one-sentence statement of what is wrong.
	Summary string
	// Detail expands on the consequence. Empty when the summary says
	// everything.
	Detail string
	// When is the observation that must hold. Nil means the version pins
	// alone are the finding — the rule fires whenever it applies, which
	// is how "this version is itself the problem" is written.
	When Condition
	// Link and LinkLabel are where the reader goes next. A condition may
	// override them per match with something more specific.
	Link      string
	LinkLabel string
}

// Requirement is one version pin: a component and the constraint its
// observed version must satisfy, such as ">=1.24 <1.27".
type Requirement struct {
	Component  Component
	Constraint string
}

// String renders the pin for the check row, so the screen states the
// scope of a clear result: "clear" from a rule pinned to CNPG 1.24 rules
// nothing out on 1.27.
func (r Requirement) String() string {
	return string(r.Component) + " " + r.Constraint
}

// Condition is one observation a rule can wait for. Implementations are
// declarative structs, so a rule stays data; each knows which input it
// reads and answers "could not run" when that input is unobserved,
// keeping the per-rule accounting as honest as the hand-written
// detectors'.
type Condition interface {
	// describe states what is looked for, folded into the check row.
	describe() string
	// evaluate returns one entry per matched finding, or the reason the
	// condition could not be evaluated. The rule's ID is passed through
	// so stream-backed conditions can find their own observations.
	evaluate(ruleID string, in Input) (matches []conditionMatch, unavailable string)
}

// conditionMatch is one matched finding as a condition reports it: the
// evidence, plus the parts only the condition can know.
type conditionMatch struct {
	// idSuffix distinguishes findings when one rule matches in several
	// places; empty when the rule's ID alone identifies the finding.
	idSuffix string
	// summary overrides the rule's summary when the match can state it
	// more precisely; empty keeps the rule's.
	summary string
	// evidence is the quoted claims, in reading order.
	evidence []Evidence
	// link and linkLabel override the rule's when set.
	link, linkLabel string
}

// LogContains matches a line in a followed container log carrying every
// substring. Substrings rather than patterns, deliberately: a rule is a
// claim that a fixed message means a fixed thing, and the fixed strings
// are the claim's own words.
//
// The matching itself happens continuously in the logstream matcher —
// LogRules derives the matcher's rule set from the catalog — and this
// condition reads back what that matcher retained. A stream is best
// effort: it breaks on every container restart and Kubernetes cannot say
// what was missed, so a match reports what was seen, counts are floors,
// and no absence here is evidence of anything.
type LogContains struct {
	// Substrings must all appear in one line.
	Substrings []string
}

func (c LogContains) describe() string {
	quoted := make([]string, len(c.Substrings))
	for i, s := range c.Substrings {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return "a followed log line containing " + strings.Join(quoted, " and ")
}

func (c LogContains) evaluate(ruleID string, in Input) ([]conditionMatch, string) {
	if in.Logs == nil {
		return nil, "log following is off, so nothing in the logs has been read"
	}
	var matches []conditionMatch
	for _, observation := range in.Logs.Observations() {
		if observation.RuleID != ruleID {
			continue
		}
		matches = append(matches, conditionMatch{
			idSuffix: "/" + observation.Pod + "/" + observation.Container,
			evidence: []Evidence{
				{
					Origin: "container log (best effort)",
					Object: fmt.Sprintf("Pod/%s container %s", observation.Pod, observation.Container),
					Detail: observation.Line,
				},
				{
					Origin: "console-observed",
					Object: "matching window",
					Detail: fmt.Sprintf("first seen %s, most recently %s, at least %d matching lines",
						observation.FirstSeen.UTC().Format("15:04:05Z"),
						observation.LastSeen.UTC().Format("15:04:05Z"),
						observation.Count),
				},
			},
			link:      "/logs/" + observation.Pod + "/" + observation.Container,
			linkLabel: "Log tail",
		})
	}
	return matches, ""
}

// EventMatch matches Warning events by reason, optionally narrowed to
// messages carrying every substring. It quotes the events rather than
// reasoning about them: the API server already stated the refusal in its
// own words.
type EventMatch struct {
	// Reasons are the event reasons accepted, any of which matches.
	Reasons []string
	// MessageContains narrows to messages carrying every substring.
	// Empty accepts any message under a matching reason.
	MessageContains []string
}

func (c EventMatch) describe() string {
	return "a Warning event with reason " + strings.Join(c.Reasons, " or ")
}

func (c EventMatch) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if !in.HasEvents {
		return nil, "events have not been observed yet"
	}
	var evidence []Evidence
	for _, event := range warningEvents(in.Events.Events, c.Reasons...) {
		carriesAll := true
		for _, needle := range c.MessageContains {
			if !strings.Contains(event.Message, needle) {
				carriesAll = false
				break
			}
		}
		if carriesAll {
			evidence = append(evidence, eventEvidence(event))
		}
	}
	if len(evidence) == 0 {
		return nil, ""
	}
	// One finding quoting every matched event: the events are the same
	// refusal repeating, not separate incidents.
	return []conditionMatch{{evidence: evidence}}, ""
}

// evaluateRule turns one rule into its check row and findings. The
// outcomes, in the order they are decided:
//
//   - a pinned component's version is unobserved → could not run,
//   - the observed versions fall outside the pins → does not apply,
//   - the condition's input is unobserved → could not run,
//   - the condition matched → findings, each quoting its evidence and,
//     when the rule is pinned, the version facts that made it apply,
//   - otherwise → clear, scoped by the pins the check row states.
func evaluateRule(rule Rule, in Input) (Check, []Finding) {
	check := Check{Name: rule.ID, Describes: ruleDescribes(rule)}
	facts := versionFacts(in)

	var pins []Evidence
	for _, requirement := range rule.Requires {
		observed, known := facts[requirement.Component]
		if !known {
			check.Outcome = CheckUnavailable
			check.Because = fmt.Sprintf("the %s version is not observed, and this check applies only to %s",
				requirement.Component, requirement)
			return check, nil
		}
		if !satisfies(observed.Version, requirement.Constraint) {
			check.Outcome = CheckNotApplicable
			check.Because = fmt.Sprintf("applies to %s; observed %s", requirement, observed.Version)
			return check, nil
		}
		pins = append(pins, observed.evidence())
	}

	matches := []conditionMatch{{}}
	if rule.When != nil {
		var unavailable string
		matches, unavailable = rule.When.evaluate(rule.ID, in)
		if unavailable != "" {
			check.Outcome, check.Because = CheckUnavailable, unavailable
			return check, nil
		}
		if len(matches) == 0 {
			check.Outcome = CheckClear
			return check, nil
		}
	}

	check.Outcome = CheckMatched
	findings := make([]Finding, 0, len(matches))
	for _, match := range matches {
		finding := Finding{
			ID:        rule.ID + match.idSuffix,
			Severity:  rule.Severity,
			Summary:   rule.Summary,
			Detail:    rule.Detail,
			Evidence:  append(match.evidence, pins...),
			Link:      rule.Link,
			LinkLabel: rule.LinkLabel,
		}
		if match.summary != "" {
			finding.Summary = match.summary
		}
		if match.link != "" {
			finding.Link, finding.LinkLabel = match.link, match.linkLabel
		}
		findings = append(findings, finding)
	}
	return check, findings
}

// ruleDescribes folds the version pins into the check row, so a clear
// result states its own scope.
func ruleDescribes(rule Rule) string {
	describes := rule.Describes
	if describes == "" && rule.When != nil {
		describes = rule.When.describe()
	}
	if len(rule.Requires) == 0 {
		return describes
	}
	pins := make([]string, len(rule.Requires))
	for i, requirement := range rule.Requires {
		pins[i] = requirement.String()
	}
	return describes + " (applies to " + strings.Join(pins, ", ") + ")"
}

// LogRules derives the continuous matcher's rule set from the catalog:
// every rule watching for a log line is declared once, there, and the
// matcher learns its substrings from the same declaration the evaluator
// reads. The matcher is deliberately version-blind — it retains matches
// whatever the versions are, and the evaluator applies the pins when the
// findings are assembled, so a version fact that arrives late does not
// need lines re-read that are already gone.
func LogRules() []logstream.Rule {
	var rules []logstream.Rule
	for _, rule := range Catalog() {
		if condition, ok := rule.When.(LogContains); ok {
			rules = append(rules, logstream.Rule{
				ID:       rule.ID,
				Contains: condition.Substrings,
				Summary:  rule.Summary,
			})
		}
	}
	return rules
}
