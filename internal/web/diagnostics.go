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

	"github.com/fyannk/pgConsole/internal/diagnose"
	"github.com/fyannk/pgConsole/internal/diagnose/catalog"
	"github.com/fyannk/pgConsole/internal/redact"
)

// DiagnosticsView is the diagnostics screen: what was found, and — just
// as load-bearing — what was looked at.
//
// The second half is not decoration. Findings alone would make an empty
// screen read as "nothing is wrong", which is a claim the console cannot
// support: it would be asserting health from the absence of a match. The
// checks list turns that into "these were looked at, and these could not
// be", which is what the console actually knows.
type DiagnosticsView struct {
	Shell       ShellView
	ClusterName string
	// Findings are most severe first.
	Findings []FindingView
	// Checks account for every detector that ran.
	Checks []CheckView
	// Unavailable counts the checks that could not run, so the summary
	// line can say so without the reader counting rows.
	Unavailable int
}

// FindingView is one finding as rendered.
type FindingView struct {
	ID        string
	Severity  string
	Summary   string
	Detail    string
	Evidence  []EvidenceView
	Link      string
	LinkLabel string
}

// EvidenceView is one quoted claim beneath a finding.
type EvidenceView struct {
	Origin string
	Object string
	Detail string
}

// CheckView is one detector's account of itself.
type CheckView struct {
	Name      string
	Describes string
	Outcome   string
	Because   string
	// State is the token the stylesheet keys off, so a check that could
	// not run does not read the same as one that came back clear.
	State string
}

// handleDiagnostics renders one diagnostics run. The run is a pure
// function of the snapshots already published, so this handler makes no
// API call: it is not a request-time exception, it is ordinary rendering.
func (h *Handler) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	result := diagnose.Run(h.diagnosticsInput(), catalog.Rules()...)
	h.renderDiagnostics(w, h.buildDiagnosticsView(r, result))
}

// diagnosticsInput gathers every published snapshot for one run.
//
// Every source is optional and each is paired with its own flag: a
// detector must be able to tell "not observed" from "observed and
// empty", because only the second licenses a clear result. It is a
// method of its own so a test can assert that every source the console
// publishes actually reaches the detectors — a new source that is added
// to Sources and forgotten here would otherwise be invisible.
func (h *Handler) diagnosticsInput() diagnose.Input {
	in := diagnose.Input{Now: h.now()}
	if h.sources.Backups != nil {
		in.Backups, in.HasBackups = h.sources.Backups.CurrentBackups()
	}
	if h.sources.Events != nil {
		in.Events, in.HasEvents = h.sources.Events.CurrentEvents()
	}
	if h.sources.Pods != nil {
		in.Pods, in.HasPods = h.sources.Pods.CurrentPods()
	}
	if h.sources.Cluster != nil {
		in.Cluster, in.HasCluster = h.sources.Cluster.Current()
	}
	if h.sources.Infrastructure != nil {
		in.Infrastructure, in.HasInfrastructure = h.sources.Infrastructure.CurrentInfrastructure()
	}
	if h.sources.KubeVersion != nil {
		in.KubeVersion, in.HasKubeVersion = h.sources.KubeVersion.CurrentKubeVersion()
	}
	if h.sources.Poolers != nil {
		in.Poolers, in.HasPoolers = h.sources.Poolers.CurrentPoolers()
	}
	if h.sources.PoolerPods != nil {
		in.PoolerPods, in.HasPoolerPods = h.sources.PoolerPods.CurrentPoolerPods()
	}
	if h.sources.FailoverQuorum != nil {
		in.FailoverQuorum, in.HasFailoverQuorum = h.sources.FailoverQuorum.CurrentFailoverQuorum()
	}
	if h.sources.ImageCatalogs != nil {
		in.ImageCatalogs, in.HasImageCatalogs = h.sources.ImageCatalogs.CurrentImageCatalogs()
	}
	if h.sources.DatabaseObjects != nil {
		in.DatabaseObjects, in.HasDatabaseObjects = h.sources.DatabaseObjects.CurrentDatabaseObjects()
	}
	if h.sources.History != nil {
		in.History, in.HasHistory = h.sources.History.Snapshot()
	}
	if h.sources.Evidence != nil {
		in.Evidence, in.HasEvidence = h.sources.Evidence.CurrentEvidence(), true
	}
	// A nil interface value here is the honest "no window": the detector
	// reports that it could not run rather than that nothing was scraped.
	if h.sources.Metrics != nil {
		in.Metrics = h.sources.Metrics
	}
	if h.sources.PoolerMetrics != nil {
		in.PoolerMetrics = h.sources.PoolerMetrics
	}
	if h.sources.LogObservations != nil {
		in.Logs = h.sources.LogObservations
	}

	return in
}

// buildDiagnosticsView renders one run into the screen's view model.
func (h *Handler) buildDiagnosticsView(r *http.Request, result diagnose.Result) DiagnosticsView {
	view := DiagnosticsView{
		Shell:       h.shell(r, "diagnostics"),
		ClusterName: h.cfg.ClusterName,
	}
	for _, finding := range result.Findings {
		rendered := FindingView{
			ID:        finding.ID,
			Severity:  finding.Severity.String(),
			Summary:   boundMessage(finding.Summary),
			Detail:    boundMessage(finding.Detail),
			Link:      finding.Link,
			LinkLabel: finding.LinkLabel,
		}
		for _, evidence := range finding.Evidence {
			rendered.Evidence = append(rendered.Evidence, EvidenceView{
				Origin: evidence.Origin,
				Object: evidence.Object,
				Detail: boundMessage(evidence.Detail),
			})
		}
		view.Findings = append(view.Findings, rendered)
	}
	for _, check := range result.Checks {
		state := "ok"
		switch check.Outcome {
		case diagnose.CheckUnavailable:
			state = "unknown"
			view.Unavailable++
		case diagnose.CheckMatched:
			state = "bad"
		case diagnose.CheckNotApplicable:
			// Muted, not clear: the rule ruled itself out on the observed
			// versions, and the row's text says so.
			state = "na"
		}
		view.Checks = append(view.Checks, CheckView{
			Name:      check.Name,
			Describes: check.Describes,
			Outcome:   check.Outcome.String(),
			Because:   boundMessage(check.Because),
			State:     state,
		})
	}
	return view
}

// renderDiagnostics writes the screen, matching the other flag-gated
// panels' rendering path.
func (h *Handler) renderDiagnostics(w http.ResponseWriter, view DiagnosticsView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := h.tpl.ExecuteTemplate(w, "diagnostics.html.tmpl", view); err != nil {
		h.logger.Error("render failed",
			slog.String("route", "diagnostics"),
			slog.String("category", redact.Safe(err)))
	}
}
