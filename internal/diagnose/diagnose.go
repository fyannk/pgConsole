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
)

// String names the outcome for display.
func (o CheckOutcome) String() string {
	switch o {
	case CheckMatched:
		return "matched"
	case CheckUnavailable:
		return "could not run"
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
	// Infrastructure carries the cluster's volumes.
	Infrastructure    observe.InfrastructureSnapshot
	HasInfrastructure bool
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

// Detectors is the registered set, in the order their checks are listed.
func Detectors() []Detector {
	return []Detector{
		quotaDetector{},
		schedulingDetector{},
		imagePullDetector{},
		volumeDetector{},
		backupCadenceDetector{},
	}
}

// Run executes every detector and assembles the result. A detector
// reporting an unavailable reason contributes no findings, however many
// it returned: a detector that could not read its input has nothing
// trustworthy to say.
func Run(in Input) Result {
	detectors := Detectors()
	result := Result{Checks: make([]Check, 0, len(detectors))}
	for _, detector := range detectors {
		check := Check{Name: detector.Name(), Describes: detector.Describes()}
		findings, unavailable := detector.Detect(in)
		switch {
		case unavailable != "":
			check.Outcome, check.Because = CheckUnavailable, unavailable
		case len(findings) > 0:
			check.Outcome = CheckMatched
			result.Findings = append(result.Findings, findings...)
		default:
			check.Outcome = CheckClear
		}
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
