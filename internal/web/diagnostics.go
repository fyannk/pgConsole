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
	"fmt"
	"log/slog"
	"net/http"
	"sort"

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
	// State is the operator's own account of the cluster, stated before
	// any finding: a reader asking "what is wrong" needs "what state is
	// it in" answered first.
	State ClusterStateView
	// Findings are most severe first. A finding whose declared cause
	// also matched, on a finding that satisfies the relation's scope and
	// window, is nested inside that cause's card rather than listed
	// here, so one incident reads as one incident.
	Findings []FindingView
	// Groups bucket every check by outcome, in reading order: matched
	// first, then could-not-run, then does-not-apply, then clear. The
	// grouping is presentation — every check is still on the page — but
	// it is what lets sixty clear rows collapse under one honest line
	// instead of burying the two rows that matter.
	Groups []CheckGroupView
	// Total counts every check, for the summary line.
	Total int
}

// CheckGroupView is one outcome's checks, rendered as a collapsible
// group. Open groups are the ones a reader must not miss: matches and
// checks that could not run. The details element is ordinary markup, so
// a reader without script opens it the same way.
type CheckGroupView struct {
	// Label is the outcome with its count, in the outcome's own words.
	Label string
	// Explain glosses what belonging to this group means — and, for
	// clear, what it does not: clear rules out exactly what each row
	// describes, nothing more.
	Explain string
	// State is the stylesheet token for the group's chip and rows.
	State string
	// Open renders the group expanded.
	Open   bool
	Checks []CheckView
}

// ClusterStateView is the header strip: the operator-reported state of
// the cluster, or an explicit unknown.
type ClusterStateView struct {
	// Observed is false when no Cluster snapshot exists; the strip then
	// says so instead of rendering empty facts.
	Observed bool
	// Phase and PhaseReason are the operator's words, "unknown" when
	// unreported.
	Phase       string
	PhaseReason string
	// Instances states ready against declared, e.g. "1 of 3 ready".
	Instances string
	// Primary is the current primary instance, "unknown" when
	// unreported.
	Primary string
	// State is the stylesheet token: current only for the operator's
	// healthy phase, unknown when unobserved, degraded otherwise.
	State string
}

// FindingView is one finding as rendered.
type FindingView struct {
	ID       string
	Severity string
	Summary  string
	Detail   string
	Evidence []EvidenceView
	// NextSteps is the console's guidance, rendered apart from the
	// quoted evidence and labeled as guidance: it is the one thing on
	// the screen no source reported.
	NextSteps string
	// Consequences are findings whose declared cause is this finding
	// (directly or through a chain), presented inside this card as one
	// incident. The relation is catalog-pinned knowledge; each nested
	// finding keeps its own evidence.
	Consequences []FindingView
	// Via states the terms of the relation that placed a nested finding
	// under its cause — strength, scope, window — so the reader sees
	// what the nesting rests on. Empty on a root card.
	Via string
	// Related are findings the catalog names as causes of this one that
	// also matched, but on an object or at a time the relation does not
	// cover. They stay their own cards; this list says why, so the
	// reader is told about the near miss instead of left to wonder.
	Related   []RelatedView
	Link      string
	LinkLabel string
}

// RelatedView is one matched cause the relation did not admit.
type RelatedView struct {
	ID      string
	Summary string
	// Object is the other finding's subject, when it names one.
	Object string
	// Because is the relation's own account of why it did not hold.
	Because string
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
	in := h.diagnosticsInput()
	result := diagnose.Run(in, catalog.Rules()...)
	h.renderDiagnostics(w, h.buildDiagnosticsView(r, in, result))
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
	if h.sources.Quotas != nil {
		in.Quotas, in.HasQuotas = h.sources.Quotas.CurrentQuotas()
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
func (h *Handler) buildDiagnosticsView(r *http.Request, in diagnose.Input, result diagnose.Result) DiagnosticsView {
	view := DiagnosticsView{
		Shell:       h.shell(r, "diagnostics"),
		ClusterName: h.cfg.ClusterName,
		State:       clusterStateView(in),
	}
	rendered := make([]FindingView, 0, len(result.Findings))
	for _, finding := range result.Findings {
		one := FindingView{
			ID:        finding.ID,
			Severity:  finding.Severity.String(),
			Summary:   boundMessage(finding.Summary),
			Detail:    boundMessage(finding.Detail),
			NextSteps: boundMessage(finding.NextSteps),
			Link:      finding.Link,
			LinkLabel: finding.LinkLabel,
		}
		for _, evidence := range finding.Evidence {
			one.Evidence = append(one.Evidence, EvidenceView{
				Origin: evidence.Origin,
				Object: evidence.Object,
				Detail: boundEvidence(evidence.Detail),
			})
		}
		rendered = append(rendered, one)
	}
	view.Findings = groupIncidents(result.Findings, rendered)
	// Bucket the checks by outcome, keeping catalog order inside each
	// group. The states are the console's shared vocabulary: a match is
	// degraded, an unrunnable check is unknown, an inapplicable one is
	// na, and only a check that ran and found nothing is current.
	//
	// The one outcome that is split is "could not run", because it
	// covers two things a reader must not confuse: an input this
	// deployment never asked for, and one it did ask for that the
	// console cannot read. The first is a decision, identical on every
	// refresh, with nothing to react to. The second is the console
	// failing to see something it was meant to see — sometimes only just
	// now, sometimes for as long as a Role has been missing a grant, but
	// always a gap between what was asked for and what arrived.
	//
	// In one list the second reads as more of the first, and with log
	// following off, which is the default, that list opens with
	// twenty-seven rows of settled choice on every screen of every
	// healthy cluster.
	buckets := map[checkBucket][]CheckView{}
	for _, check := range result.Checks {
		view.Total++
		bucket := checkBucket{outcome: check.Outcome, sourceOff: check.SourceOff}
		buckets[bucket] = append(buckets[bucket], CheckView{
			Name:      check.Name,
			Describes: check.Describes,
			Outcome:   check.Outcome.String(),
			Because:   boundMessage(check.Because),
		})
	}
	for _, group := range []struct {
		bucket  checkBucket
		label   string
		explain string
		state   string
		open    bool
	}{
		{checkBucket{outcome: diagnose.CheckMatched}, "matched",
			"these found what they look for — each match is a finding above", "degraded", true},
		{checkBucket{outcome: diagnose.CheckUnavailable}, "could not run",
			"this deployment asked for their inputs and the console cannot read them — never observed, " +
				"not permitted, or contact lost; they rule nothing out", "unknown", true},
		{checkBucket{outcome: diagnose.CheckUnavailable, sourceOff: true}, "need a source that is switched off",
			"nothing is wrong with these: each names an input this deployment has not turned on. " +
				"They rule nothing out either, and turning one on is a decision rather than a repair",
			"na", false},
		{checkBucket{outcome: diagnose.CheckNotApplicable}, "do not apply",
			"their version pins exclude the observed versions, so they make no claim here", "na", false},
		{checkBucket{outcome: diagnose.CheckClear}, "clear",
			"ran against readable input and found nothing — which rules out exactly what each row describes, no more", "current", false},
	} {
		checks := buckets[group.bucket]
		if len(checks) == 0 {
			continue
		}
		for i := range checks {
			checks[i].State = group.state
		}
		view.Groups = append(view.Groups, CheckGroupView{
			Label:   fmt.Sprintf("%d %s", len(checks), group.label),
			Explain: group.explain,
			State:   group.state,
			Open:    group.open,
			Checks:  checks,
		})
	}
	return view
}

// checkBucket is how the screen groups one check: by outcome, and — for
// a check that could not run — by whether its input is switched off or
// on and silent.
type checkBucket struct {
	outcome   diagnose.CheckOutcome
	sourceOff bool
}

// clusterStateView reduces the operator's snapshot to the header strip.
func clusterStateView(in diagnose.Input) ClusterStateView {
	if !in.HasCluster || !in.Cluster.Cluster.Present {
		return ClusterStateView{State: unknown, Phase: unknown, Instances: unknown, Primary: unknown}
	}
	cluster := in.Cluster.Cluster
	state := ClusterStateView{
		Observed:    true,
		Phase:       orUnknown(cluster.Phase),
		PhaseReason: boundMessage(cluster.PhaseReason),
		Primary:     orUnknown(cluster.CurrentPrimary),
		Instances:   unknown,
		State:       "degraded",
	}
	if cluster.Phase == "Cluster in healthy state" {
		state.State = "current"
	} else if cluster.Phase == "" {
		state.State = unknown
	}
	if cluster.DesiredInstances != nil {
		ready := 0
		if cluster.ReadyInstances != nil {
			ready = *cluster.ReadyInstances
		}
		state.Instances = fmt.Sprintf("%d of %d ready", ready, *cluster.DesiredInstances)
	}
	return state
}

// groupIncidents nests findings under their declared causes, so one
// incident renders as one card. The relation lives on the finding: a
// finding names the checks it is a consequence of, each with a scope
// and a window, and when one of those checks also matched in the same
// run on a finding the relation admits — same pod where it says so,
// within the window where both carry a time — this finding belongs
// inside it.
//
// A cause that matched but is not admitted is not dropped and not
// nested: the finding stays a root and the near miss is listed beside
// it with the relation's own reason, because "the catalog relates
// these, but not on that pod" is something the reader should be told.
//
// The grouping walks each finding's chain to its topmost cause and
// attaches the finding there, ordered by chain depth so the immediate
// cause reads before the knock-on effects. A relation that would loop
// is ignored — the finding stays a root — because a cycle means the
// catalog's claim is malformed and flat honesty beats clever nesting.
func groupIncidents(findings []diagnose.Finding, rendered []FindingView) []FindingView {
	byCheck := map[string][]int{}
	for i, finding := range findings {
		byCheck[finding.Check] = append(byCheck[finding.Check], i)
	}

	// parentOf is the admitted cause of each finding, in the catalog's
	// preference order, or -1 for a root.
	parentOf := make([]int, len(findings))
	via := make([]diagnose.Relation, len(findings))
	for i, finding := range findings {
		parentOf[i] = -1
		var misses []RelatedView
		for _, relation := range finding.ConsequenceOf {
			if relation.Cause == finding.Check {
				continue
			}
			for _, j := range byCheck[relation.Cause] {
				if j == i {
					continue
				}
				if ok, because := relation.Holds(finding, findings[j]); ok {
					parentOf[i], via[i] = j, relation
					break
				} else {
					misses = append(misses, RelatedView{
						ID:      findings[j].ID,
						Summary: boundMessage(findings[j].Summary),
						Object:  findings[j].Subject.String(),
						Because: boundMessage(because),
					})
				}
			}
			if parentOf[i] >= 0 {
				misses = nil
				break
			}
		}
		rendered[i].Related = misses
	}

	// rootOf climbs the chain, refusing a cycle by leaving the finding
	// a root of its own.
	rootOf := func(i int) (int, int) {
		seen := map[int]bool{i: true}
		current, depth := i, 0
		for parentOf[current] >= 0 {
			next := parentOf[current]
			if seen[next] {
				return i, 0
			}
			seen[next] = true
			current, depth = next, depth+1
		}
		return current, depth
	}

	type nested struct {
		index, depth int
	}
	children := map[int][]nested{}
	var roots []int
	for i := range findings {
		root, depth := rootOf(i)
		if root == i {
			roots = append(roots, i)
			continue
		}
		children[root] = append(children[root], nested{index: i, depth: depth})
	}

	type incident struct {
		card  FindingView
		worst diagnose.Severity
	}
	incidents := make([]incident, 0, len(roots))
	for _, root := range roots {
		one := incident{card: rendered[root], worst: findings[root].Severity}
		chain := children[root]
		sort.SliceStable(chain, func(a, b int) bool { return chain[a].depth < chain[b].depth })
		for _, consequence := range chain {
			card := rendered[consequence.index]
			card.Via = via[consequence.index].Terms(
				findings[consequence.index], findings[parentOf[consequence.index]])
			card.Related = nil
			one.card.Consequences = append(one.card.Consequences, card)
			if s := findings[consequence.index].Severity; s > one.worst {
				one.worst = s
			}
		}
		incidents = append(incidents, one)
	}
	// An incident sorts by the worst it contains: a warning-severity
	// cause holding a critical consequence is a critical story, and the
	// screen reads worst first.
	sort.SliceStable(incidents, func(a, b int) bool { return incidents[a].worst > incidents[b].worst })
	out := make([]FindingView, 0, len(incidents))
	for _, one := range incidents {
		out = append(out, one.card)
	}
	return out
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
