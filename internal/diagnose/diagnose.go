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

// Package diagnose correlates facts the other screens already carry into
// findings: statements about what is wrong and where, each anchored to
// the claim it rests on.
//
// It observes nothing of its own. Every detector is a pure function from
// already-published snapshots to findings, which is what keeps the whole
// package hermetically testable and keeps the screen inside the
// snapshot-first rule — no detector may reach the API server.
//
// The honesty rules the rest of the console follows apply here with more
// force, because a finding is the one thing on the site that is
// application-derived rather than reported:
//
//   - A finding restates and correlates. It never concludes something no
//     source reports.
//   - Every finding carries its evidence verbatim, with the origin of
//     that evidence named.
//   - A detector that cannot read its inputs says so. It never reports
//     "nothing wrong" from missing data, which is why Run returns what it
//     checked alongside what it found: an empty finding list means no
//     detector matched, never that the cluster is healthy.
package diagnose

import (
	"sort"
	"time"

	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/history"
	"github.com/fyannk/pgConsole/internal/metrics"
	"github.com/fyannk/pgConsole/internal/observe"
)

// Severity orders findings on the screen. It is a property of the
// finding, not of the detector: the same detector may report a note or a
// critical depending on what it found.
type Severity int

const (
	// SeverityNote is worth knowing and costs nothing.
	SeverityNote Severity = iota
	// SeverityWarning is something an operator should look at.
	SeverityWarning
	// SeverityCritical is doing damage now — including damage that looks
	// like health, such as a backup schedule running far more often than
	// its author can have intended.
	SeverityCritical
)

// String names the severity for display and for the state token the
// stylesheet keys off.
func (s Severity) String() string {
	switch s {
	case SeverityCritical:
		return "critical"
	case SeverityWarning:
		return "warning"
	default:
		return "note"
	}
}

// Evidence is one verbatim claim a finding rests on, with the origin
// that made it. The text is quoted, never paraphrased: the finding above
// may summarise, but the reader must be able to see the underlying words
// and judge for themselves.
type Evidence struct {
	// Origin names who made the claim: the operator, Kubernetes, or the
	// console's own observation of them.
	Origin string
	// Object is the resource the claim is about, such as
	// "ScheduledBackup/orders-nightly".
	Object string
	// Detail is the claim, quoted as the source stated it.
	Detail string
}

// Finding is one correlated statement about what is wrong.
type Finding struct {
	// ID is the stable detector-scoped identifier, used for ordering
	// ties and for referring to a finding without quoting its prose.
	ID string
	// Check names the detector or rule that produced this finding, so
	// findings can be related to each other by their producers.
	Check string
	// Severity orders the finding on the screen.
	Severity Severity
	// Summary is one sentence stating what is wrong, in plain language.
	Summary string
	// Detail expands on the consequence when the summary alone would
	// understate it. Empty when the summary says everything.
	Detail string
	// Evidence is what the finding rests on, in the order a reader
	// should encounter it. Never empty: a finding without evidence is an
	// opinion.
	Evidence []Evidence
	// NextSteps is what an operator does about it: console-pinned
	// guidance, stated as guidance and rendered apart from the quoted
	// evidence — advice is the one thing on this screen no source
	// reported. Empty when the summary and evidence say everything.
	NextSteps string
	// ConsequenceOf names the checks whose findings this one follows
	// from, in preference order. When one of them also matched in the
	// same run, the screen presents this finding beneath it as part of
	// one incident rather than as a second alarm. The relation is
	// catalog-pinned knowledge, stated as such; each nested finding
	// keeps its own evidence.
	ConsequenceOf []string
	// Link is where the reader goes next — always a screen, never an
	// action. Diagnostics proposes; it does not remediate.
	Link string
	// LinkLabel names the link.
	LinkLabel string
}

// CheckOutcome reports what became of one detector in a run.
type CheckOutcome int

const (
	// CheckMatched means the detector reported at least one finding.
	CheckMatched CheckOutcome = iota
	// CheckClear means the detector ran against readable inputs and
	// found nothing.
	CheckClear
	// CheckUnavailable means the detector could not run: its input was
	// absent, forbidden, or not yet observed. This is the outcome that
	// stops an empty screen from reading as a healthy one.
	CheckUnavailable
	// CheckNotApplicable means a catalog rule's version pins exclude the
	// observed versions. Distinct from clear on purpose: the rule looked
	// at the versions and ruled itself out, not the failure.
	CheckNotApplicable
)

// String names the outcome for display.
func (o CheckOutcome) String() string {
	switch o {
	case CheckMatched:
		return "matched"
	case CheckUnavailable:
		return "could not run"
	case CheckNotApplicable:
		return "does not apply"
	default:
		return "clear"
	}
}

// Check is one detector's account of itself in a run.
type Check struct {
	// Name is the detector's stable name.
	Name string
	// Describes states what the detector looks for, so a reader can tell
	// what a clear result actually rules out.
	Describes string
	// Outcome is what became of it.
	Outcome CheckOutcome
	// Because states why an unavailable check could not run. Empty
	// otherwise.
	Because string
}

// Input is everything the detectors may read: the published snapshots,
// and the clock, so that anything time-relative is testable.
//
// Each snapshot is paired with a flag saying whether it exists at all. A
// detector must consult the flag rather than the zero value, because an
// unobserved snapshot and an empty one are different claims and only the
// second one licenses "clear".
type Input struct {
	// Now is the run instant.
	Now time.Time
	// Backups is the backup catalog, and HasBackups whether it has been
	// observed at all.
	Backups    observe.BackupsSnapshot
	HasBackups bool
	// Events is the namespace's event window, filtered to this cluster's
	// objects. It is where Kubernetes has already written down why it
	// refused something, which is why several detectors quote it rather
	// than reasoning their own way to a cause.
	Events    observe.EventsSnapshot
	HasEvents bool
	// Pods is the instance roster, including per-container state.
	Pods    observe.PodsSnapshot
	HasPods bool
	// Cluster is the operator's report, read only for the declared
	// instance count.
	Cluster    observe.Snapshot
	HasCluster bool
	// Infrastructure carries the cluster's services, volumes, volume
	// snapshots, and owned child objects including Jobs.
	Infrastructure    observe.InfrastructureSnapshot
	HasInfrastructure bool
	// Poolers and their member pods. A pooler pod is not an instance
	// pod: it runs PgBouncer, is owned through a Deployment, and fails
	// in its own ways.
	Poolers       observe.PoolersSnapshot
	HasPoolers    bool
	PoolerPods    observe.PodsSnapshot
	HasPoolerPods bool
	// FailoverQuorum is the operator's account of whether a failover
	// could proceed.
	FailoverQuorum    observe.FailoverQuorumSnapshot
	HasFailoverQuorum bool
	// ImageCatalogs are the catalogs the Cluster draws its image from.
	ImageCatalogs    observe.ImageCatalogsSnapshot
	HasImageCatalogs bool
	// KubeVersion is the API server's own /version report, the one
	// observed fact that arrives by poll rather than by watch.
	KubeVersion    observe.KubeVersionSnapshot
	HasKubeVersion bool
	// Quotas are the namespace's ResourceQuota objects, with usage and
	// exhaustion as the API server reports them. They are what lets a
	// refusal name the quota that made it.
	Quotas    observe.QuotasSnapshot
	HasQuotas bool
	// DatabaseObjects are the declared Database, DatabaseRole,
	// Publication, and Subscription resources with the operator's
	// reconciliation report on each.
	DatabaseObjects    observe.DatabaseObjectsSnapshot
	HasDatabaseObjects bool
	// History is the bounded object-definition timeline. It is what lets
	// a detector say when something changed rather than only that it is
	// wrong now — the difference between a finding and a cause.
	History    history.Snapshot
	HasHistory bool
	// Evidence is the repository-evidence sidecar's status, when one is
	// wired. The console never reads object storage itself; this is the
	// viewer's word, carried through.
	Evidence    evidence.Status
	HasEvidence bool
	// Metrics and PoolerMetrics are the scraped windows. They are query
	// interfaces rather than plain snapshots because the window is a
	// rollup ring, but reading them is still an in-memory operation: no
	// detector reaches the API server or the exporters.
	Metrics       MetricsWindow
	PoolerMetrics MetricsWindow
	// Logs is the continuous matcher's read side, nil when log following
	// is off. It is the one input that is not a snapshot: a stream is
	// best effort, so a detector reading it may report what was seen but
	// never how much there was.
	Logs LogObservations
}

// MetricsWindow is the read side of a scraped metrics window, narrowed
// to what a detector needs. It is an interface so a test can supply one
// without a scraper, and so diagnose depends on the shape rather than on
// the store.
//
// A nil window means metrics are disabled or unobserved, which a
// detector must report as "could not run" rather than as no data.
type MetricsWindow interface {
	// Instances names the instances the window holds series for.
	Instances() []string
	// Range returns the retained series for one key at one tier.
	Range(key string, tier metrics.Tier) (times []int64, byInstance map[string][]*float64)
	// InstantReadings returns every instance's latest point-in-time
	// claims, keyed by instance then by instant key. A key an instance
	// never reported is absent rather than zero.
	InstantReadings() map[string]map[string]metrics.Instant
}

// Result is one complete run: what was found, and what was checked.
type Result struct {
	// Findings are ordered most severe first, then by detector ID, so
	// the screen is stable between refreshes that change nothing.
	Findings []Finding
	// Checks are every detector that ran, in registration order. This is
	// the half that keeps the screen honest — it is what turns an empty
	// result from "nothing is wrong" into "these fourteen things were
	// looked at, and two could not be".
	Checks []Check
}

// Detector examines the input and reports findings, plus its own account
// of whether it could run at all.
type Detector interface {
	// Name is the detector's stable name.
	Name() string
	// Describes states what it looks for.
	Describes() string
	// Detect returns any findings, and — when it could not run — the
	// reason, which the screen states instead of a clear result.
	Detect(Input) (findings []Finding, unavailable string)
}

// Detectors is the registered set of hand-written detectors, in the
// order their checks are listed. These are the diagnostics that
// correlate across snapshots; the single-observation, version-scoped
// ones are declared in the catalog packages and passed to Run by the
// caller.
func Detectors() []Detector {
	return []Detector{
		quotaDetector{},
		quotaExhaustedDetector{},
		schedulingDetector{},
		imagePullDetector{},
		volumeDetector{},
		backupCadenceDetector{},
	}
}

// Run executes every hand-written detector and every given catalog
// rule, and assembles the result. The rules arrive as an argument
// rather than a registry so the catalog can live in its own packages —
// one per component, importing this one — without a dependency cycle.
// A detector reporting an unavailable reason contributes no findings,
// however many it returned: a detector that could not read its input
// has nothing trustworthy to say.
func Run(in Input, rules ...Rule) Result {
	detectors := Detectors()
	result := Result{Checks: make([]Check, 0, len(detectors)+len(rules))}
	for _, detector := range detectors {
		check := Check{Name: detector.Name(), Describes: detector.Describes()}
		findings, unavailable := detector.Detect(in)
		switch {
		case unavailable != "":
			check.Outcome, check.Because = CheckUnavailable, unavailable
		case len(findings) > 0:
			check.Outcome = CheckMatched
			for i := range findings {
				if findings[i].Check == "" {
					findings[i].Check = detector.Name()
				}
			}
			result.Findings = append(result.Findings, findings...)
		default:
			check.Outcome = CheckClear
		}
		result.Checks = append(result.Checks, check)
	}
	for _, rule := range rules {
		check, findings := evaluateRule(rule, in)
		result.Findings = append(result.Findings, findings...)
		result.Checks = append(result.Checks, check)
	}
	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].Severity != result.Findings[j].Severity {
			return result.Findings[i].Severity > result.Findings[j].Severity
		}
		return result.Findings[i].ID < result.Findings[j].ID
	})
	return result
}
