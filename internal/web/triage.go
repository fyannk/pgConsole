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

package web

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/fyannk/pgConsole/internal/diagnose"
	"github.com/fyannk/pgConsole/internal/diagnose/catalog"
	"github.com/fyannk/pgConsole/internal/diagnose/triage"
	"github.com/fyannk/pgConsole/internal/redact"
)

// TriageView is the symptom-first face of the same run the diagnostics
// screen renders: one playbook per symptom, every step answered.
type TriageView struct {
	Shell       ShellView
	ClusterName string
	Playbooks   []PlaybookView
}

// PlaybookView is one symptom walked.
type PlaybookView struct {
	ID        string
	Symptom   string
	Describes string
	Upstream  string
	// StartAt is the one-based number of the first found step, zero
	// when no step found anything.
	StartAt int
	// Summary is the walk in one line: where to start, or that nothing
	// was found and how many steps could not be judged.
	Summary string
	// State is the stylesheet token for the summary.
	State string
	Steps []StepView
}

// StepView is one step answered.
type StepView struct {
	Number   int
	Question string
	Outcome  string
	// State is the stylesheet token: degraded for found, current for
	// ruled out, unknown for could-not-be-judged, na otherwise.
	State   string
	Explain string
	Because string
	// Findings link to the matched findings on the diagnostics screen.
	Findings []FindingRefView
	Evidence []EvidenceView
	Yourself string
	// YourselfNote qualifies the command: why the reader may still
	// want to run it.
	YourselfNote string
}

// FindingRefView is a pointer to a finding rendered elsewhere.
type FindingRefView struct {
	ID       string
	Severity string
	Summary  string
	Anchor   string
}

// handleTriage renders the playbooks against the current run. It reads
// the same snapshots and runs the same catalog as the diagnostics
// screen, then walks them in the reader's order rather than the run's.
func (h *Handler) handleTriage(w http.ResponseWriter, r *http.Request) {
	in := h.diagnosticsInput()
	result := diagnose.Run(in, catalog.Rules()...)
	view := TriageView{
		Shell:       h.shellFrom(r, "triage", &result),
		ClusterName: h.cfg.ClusterName,
	}
	for _, playbook := range triage.Playbooks() {
		view.Playbooks = append(view.Playbooks, playbookView(triage.Evaluate(playbook, in, result)))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := h.tpl.ExecuteTemplate(w, "triage.html.tmpl", view); err != nil {
		h.logger.Error("render failed",
			slog.String("route", "triage"),
			slog.String("category", redact.Safe(err)))
	}
}

// playbookView renders one walk.
func playbookView(walk triage.Walk) PlaybookView {
	view := PlaybookView{
		ID: walk.ID, Symptom: walk.Symptom, Describes: walk.Describes, Upstream: walk.Upstream,
		StartAt: walk.StartAt + 1,
	}
	unjudged := 0
	for i, step := range walk.Steps {
		one := StepView{
			Number:   i + 1,
			Question: step.Question,
			Outcome:  string(step.Outcome),
			Explain:  boundMessage(step.Explain),
			Because:  boundMessage(step.Because),
			Yourself: step.Yourself,
		}
		switch step.Outcome {
		case triage.OutcomeFound:
			one.State = "degraded"
			one.Yourself = ""
		case triage.OutcomeRuledOut:
			one.State = "current"
			one.YourselfNote = "the checks above rule out only what they describe"
		case triage.OutcomeUnknown:
			one.State = unknown
			one.YourselfNote = "the console could not judge this step"
			unjudged++
		case triage.OutcomeAsk:
			one.State = "na"
			one.YourselfNote = "the console observes nothing that answers this step"
		default:
			one.State = "na"
			one.YourselfNote = "the checks behind this step did not run here"
		}
		for _, finding := range step.Findings {
			one.Findings = append(one.Findings, FindingRefView{
				ID: finding.ID, Severity: finding.Severity.String(),
				Summary: boundMessage(finding.Summary), Anchor: "/diagnostics#finding-" + finding.ID,
			})
		}
		for _, evidence := range step.Evidence {
			one.Evidence = append(one.Evidence, EvidenceView{
				Origin: evidence.Origin, Object: evidence.Object, Detail: boundEvidence(evidence.Detail),
			})
		}
		view.Steps = append(view.Steps, one)
	}
	switch {
	case walk.StartAt >= 0:
		view.State = "degraded"
		view.Summary = "start at step " + strconv.Itoa(walk.StartAt+1) + ": " + walk.Steps[walk.StartAt].Question
	case unjudged > 0:
		view.State = unknown
		view.Summary = "nothing found; " + strconv.Itoa(unjudged) + " of " + strconv.Itoa(len(walk.Steps)) + " steps could not be judged"
	default:
		view.State = "current"
		view.Summary = "nothing found: every step that could be judged was ruled out"
	}
	return view
}
