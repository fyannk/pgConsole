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
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/observe"
)

// operatorBackup builds one repository-backed operator Backup fact.
func operatorBackup(name, phase, backupID string) observe.BackupFacts {
	return observe.BackupFacts{
		Name: name, UID: "u-" + name, Phase: phase,
		Method: methodPlugin, PluginName: acceptedBarmanPlugin,
		BackupID: backupID, CreatedAt: testNow.Add(-time.Hour),
	}
}

// repoBackup builds one repository evidence item.
func repoBackup(backupID, state, code string) evidence.RepoBackup {
	end := testNow.Add(-30 * time.Minute)
	return evidence.RepoBackup{
		Server: "orders", BackupID: backupID, Status: "DONE",
		Result: evidence.StateFact{State: state, Code: code}, EndAt: &end,
	}
}

// crossInputs builds current, identity-matched inputs over the given
// catalogs.
func crossInputs(operator []observe.BackupFacts, repo []evidence.RepoBackup) crossCheckInputs {
	report := completeReport()
	facts := healthyFacts()
	facts.UID = report.ClusterUID
	return crossCheckInputs{
		backups:   observe.BackupsSnapshot{Generation: 3, ObservedAt: testNow.Add(-time.Minute), Backups: operator},
		evidence:  evidence.Status{HasReport: true, Snapshot: evidence.Snapshot{Generation: 2, ObservedAt: testNow.Add(-time.Minute), Report: report, Backups: repo}},
		cluster:   observe.Snapshot{Generation: 5, ObservedAt: testNow.Add(-time.Minute), Cluster: facts},
		clusterOK: true,
	}
}

func outcomeOf(t *testing.T, view *CrossCheckView, name string) CrossCheckRowView {
	t.Helper()
	for _, row := range view.Rows {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("no cross-check row for %q in %+v", name, view.Rows)
	return CrossCheckRowView{}
}

func TestCrossCheckOutcomeMatrix(t *testing.T) {
	t.Parallel()
	volumeSnapshot := observe.BackupFacts{Name: "b-vs", UID: "u-vs", Phase: "completed", Method: "volumeSnapshot"}
	foreignPlugin := observe.BackupFacts{Name: "b-fp", UID: "u-fp", Phase: "completed", Method: methodPlugin, PluginName: "other.plugin.io", BackupID: "id-foreign"}
	operator := []observe.BackupFacts{
		operatorBackup("b-agree", "completed", "id-agree"),
		operatorBackup("b-warn", "completed", "id-warn"),
		operatorBackup("b-missing", "completed", "id-missing"),
		operatorBackup("b-contradicted", "completed", "id-bad"),
		operatorBackup("b-unknown-state", "completed", "id-unknown"),
		operatorBackup("b-running", "running", ""),
		operatorBackup("b-no-id", "completed", ""),
		volumeSnapshot,
		foreignPlugin,
	}
	repo := []evidence.RepoBackup{
		repoBackup("id-agree", "healthy", "structural-evidence"),
		repoBackup("id-warn", "warning", "wal-gap-candidate"),
		repoBackup("id-bad", "unhealthy", "artifact-missing"),
		repoBackup("id-unknown", "unknown", "not-yet-validated"),
		repoBackup("id-orphan", "healthy", "structural-evidence"),
	}
	view := buildCrossCheckView(crossInputs(operator, repo))

	if view.Degraded != "" {
		t.Fatalf("current inputs degraded: %q", view.Degraded)
	}
	wantOutcomes := map[string]struct {
		outcome Outcome
		finding bool
	}{
		"b-agree":         {OutcomeAgreement, false},
		"b-warn":          {OutcomeAgreement, false},
		"b-missing":       {OutcomeDiscrepancy, true},
		"b-contradicted":  {OutcomeDiscrepancy, true},
		"b-unknown-state": {OutcomeUnknown, false},
		"b-running":       {OutcomeOperatorOnly, false},
		"b-no-id":         {OutcomeOperatorOnly, false},
		"b-vs":            {OutcomeOperatorOnly, false},
		"b-fp":            {OutcomeOperatorOnly, false},
	}
	for name, want := range wantOutcomes {
		row := outcomeOf(t, view, name)
		if row.Outcome != want.outcome || row.Finding != want.finding {
			t.Errorf("%s = %s finding=%v, want %s finding=%v (%s)", name, row.Outcome, row.Finding, want.outcome, want.finding, row.Detail)
		}
	}
	if len(view.Orphans) != 1 || view.Orphans[0].BackupID != "id-orphan" {
		t.Errorf("orphans = %+v", view.Orphans)
	}
	if view.FindingCount != 3 {
		t.Errorf("finding count = %d, want 2 discrepancies + 1 orphan", view.FindingCount)
	}
}

func TestCrossCheckAmbiguousIdentities(t *testing.T) {
	t.Parallel()
	operator := []observe.BackupFacts{
		operatorBackup("b-dup-1", "completed", "id-dup"),
		operatorBackup("b-dup-2", "completed", "id-dup"),
		operatorBackup("b-repo-dup", "completed", "id-repo-dup"),
	}
	repo := []evidence.RepoBackup{
		repoBackup("id-dup", "healthy", "structural-evidence"),
		repoBackup("id-repo-dup", "healthy", "structural-evidence"),
		repoBackup("id-repo-dup", "warning", "duplicate-entry"),
	}
	view := buildCrossCheckView(crossInputs(operator, repo))

	for _, name := range []string{"b-dup-1", "b-dup-2", "b-repo-dup"} {
		if row := outcomeOf(t, view, name); row.Outcome != OutcomeAmbiguous {
			t.Errorf("%s = %s (%s), want ambiguous", name, row.Outcome, row.Detail)
		}
	}
}

func TestCrossCheckStaleSidesDegradeToUnknown(t *testing.T) {
	t.Parallel()
	operator := []observe.BackupFacts{operatorBackup("b1", "completed", "id-1")}
	repo := []evidence.RepoBackup{repoBackup("id-1", "healthy", "structural-evidence")}

	cases := []struct {
		name   string
		mutate func(*crossCheckInputs)
		want   string
	}{
		{
			name:   "operator catalog stale",
			mutate: func(in *crossCheckInputs) { in.backups.Stale = true },
			want:   "operator catalog stale",
		},
		{
			name:   "console contact stale",
			mutate: func(in *crossCheckInputs) { in.evidence.Snapshot.Stale = true },
			want:   "repository evidence stale (console contact lost)",
		},
		{
			name:   "sidecar-reported stale",
			mutate: func(in *crossCheckInputs) { in.evidence.Snapshot.Report.SourceStale = true },
			want:   "repository evidence stale (sidecar-reported)",
		},
		{
			name: "both sources stale",
			mutate: func(in *crossCheckInputs) {
				in.backups.Stale = true
				in.evidence.Snapshot.Stale = true
			},
			want: "both sources stale",
		},
		{
			name:   "cluster observation stale",
			mutate: func(in *crossCheckInputs) { in.cluster.Stale = true },
			want:   "cluster observation stale",
		},
		{
			name:   "identity mismatch",
			mutate: func(in *crossCheckInputs) { in.cluster.Cluster.UID = "uid-other" },
			want:   "cluster identity mismatch",
		},
		{
			name:   "identity unknown",
			mutate: func(in *crossCheckInputs) { in.clusterOK = false },
			want:   "cluster identity unknown",
		},
		{
			name:   "no evidence report",
			mutate: func(in *crossCheckInputs) { in.evidence = evidence.Status{Failure: evidence.FailureUnavailable} },
			want:   "repository evidence unavailable",
		},
		{
			name:   "no completed scan",
			mutate: func(in *crossCheckInputs) { in.evidence.Snapshot.Report.EvidenceGeneration = 0 },
			want:   "no completed repository scan",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := crossInputs(operator, repo)
			tc.mutate(&in)
			view := buildCrossCheckView(in)
			if !strings.Contains(view.Degraded, tc.want) {
				t.Fatalf("degraded = %q, want it to name %q", view.Degraded, tc.want)
			}
			row := outcomeOf(t, view, "b1")
			if row.Outcome != OutcomeUnknown {
				t.Errorf("outcome = %s, want unknown: a degraded correlation never concludes", row.Outcome)
			}
			if view.FindingCount != 0 {
				t.Errorf("degraded correlation produced findings: %d", view.FindingCount)
			}
			if view.OrphansUnknown == "" {
				t.Error("degraded correlation left orphan detection authoritative")
			}
		})
	}
}

func TestCrossCheckTruncationNeverProvesAbsence(t *testing.T) {
	t.Parallel()
	operator := []observe.BackupFacts{operatorBackup("b-missing", "completed", "id-missing")}
	repo := []evidence.RepoBackup{repoBackup("id-other", "healthy", "structural-evidence")}

	in := crossInputs(operator, repo)
	in.evidence.Snapshot.BackupsTruncated = true
	view := buildCrossCheckView(in)
	if row := outcomeOf(t, view, "b-missing"); row.Outcome != OutcomeUnknown || row.Finding {
		t.Errorf("truncated repository produced %s finding=%v, want unknown", row.Outcome, row.Finding)
	}
	if view.OrphansUnknown == "" {
		t.Error("truncated repository left orphan detection authoritative")
	}

	in = crossInputs(operator, repo)
	in.backups.BackupsTruncated = true
	view = buildCrossCheckView(in)
	if view.OrphansUnknown == "" {
		t.Error("truncated operator catalog left orphan detection authoritative")
	}
	if row := outcomeOf(t, view, "b-missing"); row.Outcome != OutcomeDiscrepancy {
		t.Errorf("operator truncation must not weaken per-row conclusions: %s", row.Outcome)
	}
}

func TestCrossCheckOrphanDisplayBound(t *testing.T) {
	t.Parallel()
	var repo []evidence.RepoBackup
	for index := range MaxOrphanRows + 5 {
		repo = append(repo, repoBackup(fmt.Sprintf("id-%04d", index), "healthy", "structural-evidence"))
	}
	view := buildCrossCheckView(crossInputs(nil, repo))
	if len(view.Orphans) != MaxOrphanRows || !view.OrphansTruncated {
		t.Errorf("orphan display = %d rows truncated=%v", len(view.Orphans), view.OrphansTruncated)
	}
	if view.FindingCount != MaxOrphanRows+5 {
		t.Errorf("finding count = %d, want every orphan counted", view.FindingCount)
	}
	if view.Orphans[0].BackupID != fmt.Sprintf("id-%04d", MaxOrphanRows+4) {
		t.Errorf("orphans not newest-first: %q", view.Orphans[0].BackupID)
	}
}

func TestHandlerBackupEvidenceCrossCheckFindingsAndProhibitedLanguage(t *testing.T) {
	t.Parallel()
	report := completeReport()
	facts := healthyFacts()
	facts.UID = report.ClusterUID
	sources := staticSnapshots{
		snap: observe.Snapshot{Generation: 5, ObservedAt: testNow.Add(-time.Minute), Cluster: facts}, ok: true,
		backups: observe.BackupsSnapshot{
			Generation: 3, ObservedAt: testNow.Add(-time.Minute),
			Backups: []observe.BackupFacts{
				operatorBackup("nightly-1", "completed", "id-agree"),
				operatorBackup("nightly-2", "completed", "id-missing"),
			},
		}, backupsOK: true,
	}
	status := evidence.Status{HasReport: true, Snapshot: evidence.Snapshot{
		Generation: 2, ObservedAt: testNow.Add(-time.Minute), Report: report,
		Backups: []evidence.RepoBackup{
			repoBackup("id-agree", "healthy", "structural-evidence"),
			repoBackup("id-orphan", "healthy", "structural-evidence"),
		},
	}}
	h := newEvidenceHandler(t, sources, status)
	body := get(t, h, http.MethodGet, "/backups/evidence").Body.String()

	for _, want := range []string{
		"Backup cross-check",
		"agreement — operator claim plus structural repository evidence",
		"discrepancy — completed claim without repository artifacts",
		"Repository backups no resource accounts for",
		"id-orphan",
		"2 cross-check finding(s)",
		"correlated by exact backup ID",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("cross-check page misses %q", want)
		}
	}
	for _, prohibited := range []string{"verified", "restorable", "restore guaranteed", "validated backup", "safe to restore"} {
		if strings.Contains(strings.ToLower(body), prohibited) {
			t.Errorf("cross-check page contains prohibited claim %q", prohibited)
		}
	}
}

func TestHandlerIndexCrossCheckAbsentWithoutEvidenceConsumer(t *testing.T) {
	t.Parallel()
	sources := staticSnapshots{
		backups:   observe.BackupsSnapshot{Generation: 3, ObservedAt: testNow, Backups: []observe.BackupFacts{operatorBackup("b1", "completed", "id-1")}},
		backupsOK: true,
	}
	h, _ := newTestHandler(t, sources, nil, Links{})
	if body := get(t, h, http.MethodGet, "/").Body.String(); strings.Contains(body, "Backup cross-check") {
		t.Error("disabled consumer renders a cross-check section")
	}
}

// The evidence section tells a reader to open ObjectStoreViewer from the
// sidebar. That is only true when the viewer link-out in particular is
// configured — a deployment with pgAdmin and monitoring wired but no
// viewer has no such sidebar entry, and pointing at one is worse than
// staying quiet.
func TestEvidenceIntroPointsAtTheSidebarOnlyWhenTheViewerIsThere(t *testing.T) {
	t.Parallel()
	page := buildPage(context.Background(), "orders", "payments", snapshots{window: time.Hour},
		testNow, Links{PgAdmin: "https://pgadmin.example.com", Monitoring: "https://grafana.example.com"})
	if page.ViewerLinked {
		t.Error("other siblings being linked marks the viewer as linked")
	}
	if len(page.Links) == 0 {
		t.Fatal("the fixture configured no link-out at all, so the case is not exercised")
	}

	page = buildPage(context.Background(), "orders", "payments", snapshots{window: time.Hour},
		testNow, Links{ObjectStoreViewer: "https://viewer.example.com/orders"})
	if !page.ViewerLinked {
		t.Error("the configured viewer link is not reported")
	}
}
