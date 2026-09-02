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
	"sort"
	"strings"
	"time"

	"github.com/fyannk/pgConsole/internal/logstream"
	"github.com/fyannk/pgConsole/internal/metrics"
	"github.com/fyannk/pgConsole/internal/observe"
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
	// NextSteps is the console-pinned guidance carried onto every
	// finding this rule produces. See Finding.NextSteps.
	NextSteps string
	// ConsequenceOf names the checks this rule's findings follow from.
	// See Finding.ConsequenceOf.
	ConsequenceOf []string
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
	// Except withdraws a line containing any of these, for a rule whose
	// marker is broader than its meaning: PostgreSQL stamps routine
	// lifecycle refusals with the same severity word as real faults, and
	// the carve-outs name those known-benign messages exactly.
	Except []string
}

func (c LogContains) describe() string {
	quoted := make([]string, len(c.Substrings))
	for i, s := range c.Substrings {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	described := "a followed log line containing " + strings.Join(quoted, " and ")
	if len(c.Except) > 0 {
		described += fmt.Sprintf(", excluding %d known-benign messages", len(c.Except))
	}
	return described
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

// EventMatch matches events by reason, optionally narrowed to messages
// carrying every substring. It quotes the events rather than reasoning
// about them: the API server — or the operator, through its recorder —
// already stated the refusal in its own words.
//
// Only events on the Cluster object and its member pods are in the
// observed window, so a rule here must be about one of those. An event
// the operator records on a Backup or ScheduledBackup object is not
// observable and belongs to a status-backed condition instead.
type EventMatch struct {
	// Reasons are the event reasons accepted, any of which matches.
	Reasons []string
	// Types are the event types accepted; empty means Warning only.
	// Named explicitly because the operator's type is not a reliable
	// severity signal — some real failures are recorded as Normal.
	Types []string
	// MessageContains narrows to messages carrying every substring.
	// Empty accepts any message under a matching reason.
	MessageContains []string
}

func (c EventMatch) describe() string {
	return "an event with reason " + strings.Join(c.Reasons, " or ")
}

func (c EventMatch) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if reason := eventsUnavailable(in); reason != "" {
		return nil, reason
	}
	types := c.Types
	if len(types) == 0 {
		types = []string{"Warning"}
	}
	// Bounded like warningEvents, so one flapping object cannot fill a
	// finding with the same line.
	const maxQuoted = 3
	var evidence []Evidence
	for _, event := range in.Events.Events {
		if len(evidence) >= maxQuoted {
			break
		}
		typeAccepted := false
		for _, accepted := range types {
			if event.Type == accepted {
				typeAccepted = true
				break
			}
		}
		if !typeAccepted {
			continue
		}
		reasonAccepted := false
		for _, reason := range c.Reasons {
			if event.Reason == reason {
				reasonAccepted = true
				break
			}
		}
		if !reasonAccepted {
			continue
		}
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

// BackupPhase matches Backup objects sitting in one of the given phases
// for at least MinAge, quoting the operator's own phase. The age bound
// keeps ordinary progress out: every backup passes through its early
// phases, and only staying there is a finding.
type BackupPhase struct {
	// AnyOf are the phase strings that match, compared case-insensitively
	// because the operator has written both "failed" and "Failed" across
	// versions.
	AnyOf []string
	// MinAge is how long the backup must have existed before its phase
	// counts. Zero matches immediately.
	MinAge time.Duration
}

func (c BackupPhase) describe() string {
	return "a Backup whose reported phase is " + strings.Join(c.AnyOf, " or ")
}

func (c BackupPhase) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if !in.HasBackups {
		return nil, "the backup catalog has not been observed yet"
	}
	if in.Backups.Stale {
		return nil, "the backup catalog is stale, so current phases are unknown"
	}
	var matches []conditionMatch
	for _, backup := range in.Backups.Backups {
		phaseAccepted := false
		for _, phase := range c.AnyOf {
			if strings.EqualFold(backup.Phase, phase) {
				phaseAccepted = true
				break
			}
		}
		if !phaseAccepted {
			continue
		}
		if c.MinAge > 0 && in.Now.Sub(backup.CreatedAt) < c.MinAge {
			continue
		}
		matches = append(matches, conditionMatch{
			idSuffix: "/" + backup.Name,
			evidence: []Evidence{{
				Origin: "operator-reported",
				Object: "Backup/" + backup.Name,
				Detail: fmt.Sprintf("phase %s, created %s", backup.Phase,
					backup.CreatedAt.UTC().Format("2006-01-02 15:04:05Z")),
			}},
			link:      "/backups",
			linkLabel: "Backups",
		})
	}
	return matches, ""
}

// ClusterCondition matches an operator-reported condition on the
// Cluster at a given status, quoting the operator's own reason and
// message. It is the status-backed condition: nothing here depends on
// log text, so rules built on it survive rewordings that would silence
// a LogContains rule.
type ClusterCondition struct {
	// Type is the condition type, such as "ContinuousArchiving".
	Type string
	// Status is the status that matches, such as "False".
	Status string
	// Reason, when set, narrows to that reason. It matters when one
	// status carries two meanings: LastBackupSucceeded goes False both
	// for a backup that failed and for one that just started, and only
	// the reason tells them apart.
	Reason string
}

func (c ClusterCondition) describe() string {
	described := fmt.Sprintf("the operator reporting condition %s as %s", c.Type, c.Status)
	if c.Reason != "" {
		described += " with reason " + c.Reason
	}
	return described
}

func (c ClusterCondition) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if reason := clusterUnavailable(in); reason != "" {
		return nil, reason
	}
	for _, condition := range in.Cluster.Cluster.Conditions {
		if condition.Type != c.Type || condition.Status != c.Status {
			continue
		}
		if c.Reason != "" && condition.Reason != c.Reason {
			continue
		}
		detail := fmt.Sprintf("status %s, reason %s", condition.Status, condition.Reason)
		if condition.Message != "" {
			detail += ": " + condition.Message
		}
		return []conditionMatch{{evidence: []Evidence{{
			Origin: "operator-reported",
			Object: "Cluster condition " + c.Type,
			Detail: detail,
		}}}}, ""
	}
	// An absent condition is not the sought status; it is also not
	// evidence of the opposite, which is why this reads as clear only
	// under a check row that names the exact condition it waited for.
	return nil, ""
}

// ClusterPhase matches the operator-reported phase, quoting the phase
// and its reason. It is for phases that mean the cluster is waiting on
// something — a phase the operator can sit in indefinitely is the
// closest thing it has to saying "stuck" out loud.
type ClusterPhase struct {
	// AnyOf are the phase strings that match, exactly as the operator
	// writes them.
	AnyOf []string
}

func (c ClusterPhase) describe() string {
	return "the operator reporting phase " + strings.Join(c.AnyOf, " or ")
}

func (c ClusterPhase) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if reason := clusterUnavailable(in); reason != "" {
		return nil, reason
	}
	for _, phase := range c.AnyOf {
		if in.Cluster.Cluster.Phase != phase {
			continue
		}
		detail := "phase " + phase
		if in.Cluster.Cluster.PhaseReason != "" {
			detail += ": " + in.Cluster.Cluster.PhaseReason
		}
		return []conditionMatch{{evidence: []Evidence{{
			Origin: "operator-reported",
			Object: "Cluster",
			Detail: detail,
		}}}}, ""
	}
	return nil, ""
}

// InstantNonZero matches an instance whose latest reading of one
// point-in-time metric is non-zero. The keys are the metric catalog's
// instant keys, such as "fencing-on"; the exporter's own metric name is
// quoted in the evidence.
//
// It reads the scraped window, so it is a claim about what the exporter
// last reported — the read time is quoted so the reader can judge how
// stale that claim is.
type InstantNonZero struct {
	// Key is the instant key in the instance metric catalog.
	Key string
}

func (c InstantNonZero) describe() string {
	return fmt.Sprintf("an instance whose exporter reports %s as non-zero", instantMetricName(c.Key))
}

func (c InstantNonZero) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if in.Metrics == nil {
		return nil, "instance metrics are not scraped"
	}
	readings := in.Metrics.InstantReadings()
	instances := make([]string, 0, len(readings))
	for instance := range readings {
		instances = append(instances, instance)
	}
	sort.Strings(instances)
	var matches []conditionMatch
	for _, instance := range instances {
		reading, reported := readings[instance][c.Key]
		if !reported || reading.Value == 0 {
			continue
		}
		matches = append(matches, conditionMatch{
			idSuffix: "/" + instance,
			evidence: []Evidence{{
				Origin: "console-scraped from the instance exporter",
				Object: "instance " + instance,
				Detail: fmt.Sprintf("%s = %g, read %s", instantMetricName(c.Key), reading.Value,
					time.Unix(reading.At, 0).UTC().Format("15:04:05Z")),
			}},
			link:      "/cluster/metrics",
			linkLabel: "Metrics",
		})
	}
	return matches, ""
}

// instantMetricName resolves an instant key to the exporter's metric
// name, so evidence quotes the exporter's vocabulary rather than the
// console's.
func instantMetricName(key string) string {
	for _, def := range metrics.Instance.Instants {
		if def.Key == key {
			if len(def.Names) > 0 {
				return def.Names[0]
			}
			break
		}
	}
	return key
}

// ContainerState matches containers whose kubelet-reported state
// reason is in the set — CrashLoopBackOff, OOMKilled, and the like. It
// reads the same container facts the pods screen renders: the reason is
// the kubelet's own word, quoted, never inferred from restarts or
// timing.
//
// Instance pods are always scanned; pooler pods join when observed,
// because a crash-looping PgBouncer breaks applications just as surely
// while looking nothing like an instance failure.
type ContainerState struct {
	// Reasons are the kubelet reasons accepted, any of which matches.
	Reasons []string
}

func (c ContainerState) describe() string {
	return "a container whose state reason is " + strings.Join(c.Reasons, " or ")
}

func (c ContainerState) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if reason := podsUnavailable(in); reason != "" {
		return nil, reason
	}
	if reason := poolerPodsUnavailable(in); reason != "" {
		return nil, reason
	}
	pods := in.Pods.Pods
	if in.HasPoolerPods {
		pods = append(append([]observe.PodFacts{}, pods...), in.PoolerPods.Pods...)
	}
	var matches []conditionMatch
	for _, pod := range pods {
		for _, container := range pod.Containers {
			accepted := false
			for _, reason := range c.Reasons {
				if container.Reason == reason {
					accepted = true
					break
				}
			}
			if !accepted {
				continue
			}
			detail := fmt.Sprintf("container %q: %s", container.Name, container.Reason)
			if container.State != "" {
				detail += ", state " + container.State
			}
			if container.Restarts != nil {
				detail += fmt.Sprintf(", %d restarts", *container.Restarts)
			}
			if container.ExitCode != nil {
				detail += fmt.Sprintf(", last exit code %d", *container.ExitCode)
			}
			matches = append(matches, conditionMatch{
				idSuffix: "/" + pod.Name + "/" + container.Name,
				evidence: []Evidence{{
					Origin: "Kubernetes-observed",
					Object: "Pod/" + pod.Name,
					Detail: detail,
				}},
			})
		}
	}
	return matches, ""
}

// PrimaryMismatch matches a primary move still in flight after MinAge:
// the operator's current and target primaries disagree, and the
// operator's own request timestamp is at least that old. Both bounds
// come from the operator — without the timestamp there is no honest way
// to call the move stuck, so an unreported timestamp matches nothing.
type PrimaryMismatch struct {
	// MinAge is how long the move must have been in flight.
	MinAge time.Duration
}

func (c PrimaryMismatch) describe() string {
	return fmt.Sprintf("a primary move still in flight after %s", c.MinAge)
}

func (c PrimaryMismatch) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if reason := clusterUnavailable(in); reason != "" {
		return nil, reason
	}
	cluster := in.Cluster.Cluster
	if cluster.CurrentPrimary == "" || cluster.TargetPrimary == "" ||
		cluster.CurrentPrimary == cluster.TargetPrimary {
		return nil, ""
	}
	if cluster.TargetPrimaryTimestamp == nil {
		return nil, ""
	}
	age := in.Now.Sub(*cluster.TargetPrimaryTimestamp)
	if age < c.MinAge {
		return nil, ""
	}
	detail := fmt.Sprintf("currentPrimary %s, targetPrimary %s, requested %s (%s ago)",
		cluster.CurrentPrimary, cluster.TargetPrimary,
		cluster.TargetPrimaryTimestamp.UTC().Format("15:04:05Z"),
		age.Round(time.Minute))
	if cluster.TargetPrimary == "pending" {
		detail += `; "pending" is the operator's marker for a failover decided with no candidate chosen yet`
	}
	return []conditionMatch{{evidence: []Evidence{{
		Origin: "operator-reported",
		Object: "Cluster primaries",
		Detail: detail,
	}}}}, ""
}

// ScheduledBackupSuspended matches suspended backup schedules.
type ScheduledBackupSuspended struct{}

func (ScheduledBackupSuspended) describe() string {
	return "a ScheduledBackup whose suspend flag is set"
}

func (ScheduledBackupSuspended) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if !in.HasBackups {
		return nil, "the backup catalog has not been observed yet"
	}
	if in.Backups.Stale {
		return nil, "the backup catalog is stale, so current schedules are unknown"
	}
	var matches []conditionMatch
	for _, schedule := range in.Backups.ScheduledBackups {
		if schedule.Suspended == nil || !*schedule.Suspended {
			continue
		}
		matches = append(matches, conditionMatch{
			idSuffix: "/" + schedule.Name,
			evidence: []Evidence{{
				Origin: "operator-reported",
				Object: "ScheduledBackup/" + schedule.Name,
				Detail: "suspend is true",
			}},
			link:      "/backups",
			linkLabel: "Backups",
		})
	}
	return matches, ""
}

// ScheduledBackupOverdue matches schedules whose operator-reported next
// run is at least Grace in the past — the operator advances that field
// every time it schedules, so a stale one means scheduling itself has
// stopped, whatever the cause. Suspended schedules are excluded: those
// are the other rule's finding.
type ScheduledBackupOverdue struct {
	// Grace is how far past the reported next run counts as stopped.
	Grace time.Duration
}

func (c ScheduledBackupOverdue) describe() string {
	return fmt.Sprintf("a ScheduledBackup whose reported next run is over %s past", c.Grace)
}

func (c ScheduledBackupOverdue) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if !in.HasBackups {
		return nil, "the backup catalog has not been observed yet"
	}
	if in.Backups.Stale {
		return nil, "the backup catalog is stale, so current schedules are unknown"
	}
	var matches []conditionMatch
	for _, schedule := range in.Backups.ScheduledBackups {
		if schedule.Suspended != nil && *schedule.Suspended {
			continue
		}
		if schedule.NextScheduleTime == nil {
			continue
		}
		overdue := in.Now.Sub(*schedule.NextScheduleTime)
		if overdue < c.Grace {
			continue
		}
		detail := fmt.Sprintf("next run reported for %s, now %s past",
			schedule.NextScheduleTime.UTC().Format("2006-01-02 15:04:05Z"),
			overdue.Round(time.Minute))
		if schedule.LastScheduleTime != nil {
			detail += ", last scheduled " + schedule.LastScheduleTime.UTC().Format("2006-01-02 15:04:05Z")
		}
		matches = append(matches, conditionMatch{
			idSuffix: "/" + schedule.Name,
			evidence: []Evidence{{
				Origin: "operator-reported",
				Object: "ScheduledBackup/" + schedule.Name,
				Detail: detail,
			}},
			link:      "/backups",
			linkLabel: "Backups",
		})
	}
	return matches, ""
}

// SeriesAbove matches an instance whose latest reading of one retained
// metric series is at or above the threshold. Raw samples are preferred;
// the rollup tier answers when the raw window holds nothing.
type SeriesAbove struct {
	// Key is the series key in the instance metric catalog.
	Key string
	// Threshold is the inclusive lower bound that matches.
	Threshold float64
}

func (c SeriesAbove) describe() string {
	return fmt.Sprintf("an instance whose %s reading is at least %g", seriesMetricName(c.Key), c.Threshold)
}

func (c SeriesAbove) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if in.Metrics == nil {
		return nil, "instance metrics are not scraped"
	}
	latest := map[string]seriesReading{}
	for _, tier := range [...]metrics.Tier{metrics.TierRaw, metrics.TierRollup} {
		times, byInstance := in.Metrics.Range(c.Key, tier)
		for instance, column := range byInstance {
			if _, done := latest[instance]; done {
				continue
			}
			for i := len(column) - 1; i >= 0; i-- {
				if column[i] != nil {
					latest[instance] = seriesReading{value: *column[i], at: times[i]}
					break
				}
			}
		}
	}
	instances := make([]string, 0, len(latest))
	for instance := range latest {
		instances = append(instances, instance)
	}
	sort.Strings(instances)
	var matches []conditionMatch
	for _, instance := range instances {
		reading := latest[instance]
		if reading.value < c.Threshold {
			continue
		}
		matches = append(matches, conditionMatch{
			idSuffix: "/" + instance,
			evidence: []Evidence{{
				Origin: "console-scraped from the instance exporter",
				Object: "instance " + instance,
				Detail: fmt.Sprintf("%s = %.0f, read %s", seriesMetricName(c.Key), reading.value,
					time.Unix(reading.at, 0).UTC().Format("15:04:05Z")),
			}},
			link:      "/cluster/metrics",
			linkLabel: "Metrics",
		})
	}
	return matches, ""
}

// seriesReading is one instance's latest sample of a series.
type seriesReading struct {
	value float64
	at    int64
}

// seriesMetricName resolves a series key to the exporter's metric name,
// so evidence quotes the exporter's vocabulary rather than the
// console's.
func seriesMetricName(key string) string {
	if def, ok := metrics.Instance.SeriesByKey(key); ok && len(def.Names) > 0 {
		return def.Names[0]
	}
	return key
}

// DeclaredObjectFailed matches declarative database objects — Database,
// DatabaseRole, Publication, Subscription — whose operator
// reconciliation report says failed. An object the operator has not
// reported on yet matches nothing: unreported is not failed.
type DeclaredObjectFailed struct{}

func (DeclaredObjectFailed) describe() string {
	return "a declared database object whose reconciliation the operator reports as failed"
}

func (DeclaredObjectFailed) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if !in.HasDatabaseObjects {
		return nil, "the declared database objects have not been observed yet"
	}
	if in.DatabaseObjects.Stale {
		return nil, "the declared database objects are stale, so current reports are unknown"
	}
	var matches []conditionMatch
	failed := func(kind, name string, declared observe.Declared) {
		if declared.Applied == nil || *declared.Applied {
			return
		}
		detail := "applied false"
		if declared.Message != "" {
			detail += ": " + declared.Message
		}
		matches = append(matches, conditionMatch{
			idSuffix: "/" + strings.ToLower(kind) + "/" + name,
			summary:  fmt.Sprintf("The operator cannot apply the declared %s %q.", kind, name),
			evidence: []Evidence{{
				Origin: "operator-reported",
				Object: kind + "/" + name,
				Detail: detail,
			}},
			link:      "/databases",
			linkLabel: "Databases",
		})
	}
	for _, object := range in.DatabaseObjects.Databases {
		failed("Database", object.Name, object.Declared)
	}
	for _, object := range in.DatabaseObjects.Roles {
		failed("DatabaseRole", object.Name, object.Declared)
	}
	for _, object := range in.DatabaseObjects.Publications {
		failed("Publication", object.Name, object.Declared)
	}
	for _, object := range in.DatabaseObjects.Subscriptions {
		failed("Subscription", object.Name, object.Declared)
	}
	return matches, ""
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
			ID:            rule.ID + match.idSuffix,
			Check:         rule.ID,
			Severity:      rule.Severity,
			Summary:       rule.Summary,
			Detail:        rule.Detail,
			Evidence:      append(match.evidence, pins...),
			NextSteps:     rule.NextSteps,
			ConsequenceOf: rule.ConsequenceOf,
			Link:          rule.Link,
			LinkLabel:     rule.LinkLabel,
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

// LogRules derives the continuous matcher's rule set from a catalog:
// every rule watching for a log line is declared once, there, and the
// matcher learns its substrings from the same declaration the evaluator
// reads. The matcher is deliberately version-blind — it retains matches
// whatever the versions are, and the evaluator applies the pins when the
// findings are assembled, so a version fact that arrives late does not
// need lines re-read that are already gone.
func LogRules(rules []Rule) []logstream.Rule {
	var derived []logstream.Rule
	for _, rule := range rules {
		if condition, ok := rule.When.(LogContains); ok {
			derived = append(derived, logstream.Rule{
				ID:       rule.ID,
				Contains: condition.Substrings,
				Except:   condition.Except,
				Summary:  rule.Summary,
			})
		}
	}
	return derived
}
