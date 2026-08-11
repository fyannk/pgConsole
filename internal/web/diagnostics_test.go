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
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/diagnose"
	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/history"
	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/observe"
)

// stubHistory and stubEvidence stand in for the two sources that are not
// snapshot-shaped, so the wiring guard below covers them too.
type stubHistory struct{ has bool }

func (s stubHistory) Snapshot() (history.Snapshot, bool) { return history.Snapshot{}, s.has }

func (stubHistory) Revision(uint64) (history.Revision, bool) { return history.Revision{}, false }

func (stubHistory) Diff(uint64) (history.Diff, bool) { return history.Diff{}, false }

type stubEvidence struct{}

func (stubEvidence) CurrentEvidence() evidence.Status { return evidence.Status{} }

// newDiagnosticsHandler builds a handler with diagnostics on or off and
// the given backup catalog, which is the only input the first detector
// reads.
func newDiagnosticsHandler(t *testing.T, allow bool, snapshots staticSnapshots) *Handler {
	return newDiagnosticsHandlerFull(t, allow, snapshots, nil, nil)
}

// newDiagnosticsHandlerFull also wires the two non-snapshot sources.
func newDiagnosticsHandlerFull(t *testing.T, allow bool, snapshots staticSnapshots,
	history HistorySource, ev EvidenceSource) *Handler {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h, err := New(Config{
		ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour,
		AllowLogs: true, LevelHeader: "X-PgToolBox-Level", AllowDiagnostics: allow,
	},
		Sources{Cluster: snapshots, Pods: snapshots, Events: snapshots, Backups: snapshots,
			Poolers: snapshots, PoolerPods: snapshots, FailoverQuorum: snapshots,
			ImageCatalogs: snapshots, DatabaseObjects: snapshots, Infrastructure: snapshots,
			KubeVersion: snapshots, Quotas: snapshots, History: history, Evidence: ev},
		kube.UnavailableProber{}, nil, Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
		nil, nil, func() time.Time { return testNow }, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// TestDiagnosticsDisabledRegistersNoRoute proves the flag removes the
// screen rather than hiding it: disabled means 404, the same shape as
// the other opt-in panels.
func TestDiagnosticsDisabledRegistersNoRoute(t *testing.T) {
	t.Parallel()
	h := newDiagnosticsHandler(t, false, staticSnapshots{})
	if rec := getWithHeaders(t, h, "/diagnostics", dba); rec.Code != http.StatusNotFound {
		t.Errorf("disabled diagnostics = %d, want 404", rec.Code)
	}
}

// TestDiagnosticsRequiresPowerUser proves the gate. Findings quote
// evidence the ladder already gates at poweruser, so the screen sits at
// the same level rather than below it.
func TestDiagnosticsRequiresPowerUser(t *testing.T) {
	t.Parallel()
	h := newDiagnosticsHandler(t, true, staticSnapshots{})
	view := map[string]string{"X-Forwarded-User": "v@corp", "X-PgToolBox-Level": "view"}
	if rec := getWithHeaders(t, h, "/diagnostics", view); rec.Code != http.StatusForbidden {
		t.Errorf("view-level diagnostics = %d, want 403", rec.Code)
	}
	power := map[string]string{"X-Forwarded-User": "p@corp", "X-PgToolBox-Level": "poweruser"}
	if rec := getWithHeaders(t, h, "/diagnostics", power); rec.Code != http.StatusOK {
		t.Errorf("poweruser diagnostics = %d, want 200", rec.Code)
	}
	if rec := getWithHeaders(t, h, "/diagnostics", nil); rec.Code != http.StatusForbidden {
		t.Errorf("ungated diagnostics = %d, want 403", rec.Code)
	}
}

// TestDiagnosticsEmptyResultDoesNotClaimHealth is the honesty property
// of the screen. With nothing observed there are no findings, and the
// page must say what it looked at and that a check could not run —
// never that the cluster is fine.
func TestDiagnosticsEmptyResultDoesNotClaimHealth(t *testing.T) {
	t.Parallel()
	h := newDiagnosticsHandler(t, true, staticSnapshots{})
	body := getWithHeaders(t, h, "/diagnostics", dba).Body.String()
	for _, want := range []string{
		"No check matched",
		"not a statement that the cluster is healthy",
		"What was checked",
		"could not run",
		"backup-cadence",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("empty diagnostics page misses %q", want)
		}
	}
}

// TestDiagnosticsRendersAFindingWithItsEvidence proves the screen states
// the finding and quotes what it rests on, with the origin named.
func TestDiagnosticsRendersAFindingWithItsEvidence(t *testing.T) {
	t.Parallel()
	snapshots := staticSnapshots{backups: observe.BackupsSnapshot{
		Generation: 1,
		ObservedAt: testNow,
		ScheduledBackups: []observe.ScheduledBackupFacts{
			{Name: "orders-nightly", Schedule: "0 2 * * * *", Method: "barmanObjectStore"},
		},
	}, backupsOK: true}

	h := newDiagnosticsHandler(t, true, snapshots)
	body := getWithHeaders(t, h, "/diagnostics", dba).Body.String()
	for _, want := range []string{
		"orders-nightly",
		"24 times a day",
		"0 2 * * * *",       // the schedule, quoted verbatim
		"operator-reported", // its origin, named
		"matched",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("finding page misses %q", want)
		}
	}
}

// TestDiagnosticsRendersAnEventBackedFinding proves the screen carries a
// refusal the API server already explained, quoted with its numbers
// intact — which is why the quota finding needs no ResourceQuota read.
func TestDiagnosticsRendersAnEventBackedFinding(t *testing.T) {
	t.Parallel()
	const message = `pods "orders-3" is forbidden: exceeded quota: compute, used: pods=8, limited: pods=8`
	snapshots := staticSnapshots{
		events: observe.EventsSnapshot{Generation: 1, ObservedAt: testNow,
			Events: []observe.EventFacts{{
				Kind: "Cluster", Object: "orders", Type: "Warning",
				Reason: "FailedCreate", Message: message, Count: 1, LastSeen: testNow,
			}}},
		eventsOK: true,
	}
	body := getWithHeaders(t, newDiagnosticsHandler(t, true, snapshots), "/diagnostics", dba).Body.String()
	for _, want := range []string{
		"namespace quota is refusing",
		"used: pods=8", // the headroom, straight from the refusal
		"limited: pods=8",
		"resource-quota",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("event-backed finding page misses %q", want)
		}
	}
}

// TestDiagnosticsInputReachesEveryPublishedSource is the guard on the
// foundation. Detectors can only correlate what they are handed, so a
// source added to Sources and forgotten in diagnosticsInput would be
// invisible — no error, no failing test, just a detector that can never
// see it.
//
// The assertion is reflective rather than a written-out list: every
// Has* flag on diagnose.Input must be true when every source is wired
// and reporting. A new snapshot added to Input therefore fails here
// until it is actually plumbed through.
func TestDiagnosticsInputReachesEveryPublishedSource(t *testing.T) {
	t.Parallel()
	all := staticSnapshots{
		ok: true, podsOK: true, eventsOK: true, backupsOK: true,
		poolersOK: true, poolerPodsOK: true, quorumOK: true,
		catalogsOK: true, declaredOK: true, infraOK: true, kubeVersionOK: true,
		quotasOK: true,
	}
	h := newDiagnosticsHandlerFull(t, true, all, stubHistory{has: true}, stubEvidence{})
	in := h.diagnosticsInput()

	value := reflect.ValueOf(in)
	for i := range value.NumField() {
		name := value.Type().Field(i).Name
		if !strings.HasPrefix(name, "Has") {
			continue
		}
		if !value.Field(i).Bool() {
			t.Errorf("%s is false: the source is published but never reaches the detectors", name)
		}
	}
	if in.Now.IsZero() {
		t.Error("the run instant was not set, so nothing time-relative can be judged")
	}
}

// TestDiagnosticsInputDistinguishesUnobservedFromEmpty proves the flags
// carry their meaning: with no source wired every Has* is false, so a
// detector reports that it could not run rather than that it found
// nothing.
func TestDiagnosticsInputDistinguishesUnobservedFromEmpty(t *testing.T) {
	t.Parallel()
	in := newDiagnosticsHandler(t, true, staticSnapshots{}).diagnosticsInput()
	value := reflect.ValueOf(in)
	for i := range value.NumField() {
		name := value.Type().Field(i).Name
		if strings.HasPrefix(name, "Has") && value.Field(i).Bool() {
			t.Errorf("%s is true with nothing observed", name)
		}
	}
}

// TestEvidenceQuotesSurviveTheEnvelope is the truncation regression: the
// operator's log envelope alone runs past five hundred characters, so
// bounding evidence like an ordinary message cut every quoted record
// exactly before its message field — the part the reader came for.
func TestEvidenceQuotesSurviveTheEnvelope(t *testing.T) {
	t.Parallel()
	envelope := `{"level":"info","logger":"postgres","record":{` +
		strings.Repeat(`"padding":"the operator envelope is long",`, 14) +
		`"error_severity":"FATAL","message":"database \"absent_db\" does not exist"}}`
	if len(envelope) <= 512 {
		t.Fatalf("fixture too short to prove anything: %d", len(envelope))
	}

	h := newDiagnosticsHandler(t, true, staticSnapshots{})
	view := h.buildDiagnosticsView(
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/diagnostics", nil),
		diagnose.Input{},
		diagnose.Result{Findings: []diagnose.Finding{{
			ID: "postgres-fatal", Severity: diagnose.SeverityWarning,
			Summary:  "PostgreSQL logged a FATAL-severity record.",
			Evidence: []diagnose.Evidence{{Origin: "container log (best effort)", Object: "Pod/orders-1", Detail: envelope}},
		}}})

	got := view.Findings[0].Evidence[0].Detail
	if !strings.Contains(got, `database \"absent_db\" does not exist`) {
		t.Errorf("the message field did not survive the display bound; quote ends %q", got[max(0, len(got)-60):])
	}
}

// TestGroupIncidentsNestsConsequencesUnderTheirCause proves the
// incident view: a chain of matched findings renders as one root card
// with its consequences inside, ordered nearest cause first, and an
// unrelated finding stays its own card. A malformed cyclic relation
// degrades to flat cards rather than losing a finding.
func TestGroupIncidentsNestsConsequencesUnderTheirCause(t *testing.T) {
	t.Parallel()
	h := newDiagnosticsHandler(t, true, staticSnapshots{})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/diagnostics", nil)

	view := h.buildDiagnosticsView(req, diagnose.Input{}, diagnose.Result{Findings: []diagnose.Finding{
		{ID: "root", Check: "root", Summary: "Root."},
		{ID: "mid", Check: "mid", Summary: "Mid.", ConsequenceOf: []string{"root"}},
		{ID: "leaf", Check: "leaf", Summary: "Leaf.", ConsequenceOf: []string{"mid"}},
		{ID: "other", Check: "other", Summary: "Other."},
	}})
	if len(view.Findings) != 2 {
		t.Fatalf("cards = %d, want the root and the unrelated finding", len(view.Findings))
	}
	root := view.Findings[0]
	if root.ID != "root" || len(root.Consequences) != 2 {
		t.Fatalf("root card = %+v", root)
	}
	if root.Consequences[0].ID != "mid" || root.Consequences[1].ID != "leaf" {
		t.Errorf("chain not ordered nearest cause first: %+v", root.Consequences)
	}

	cyclic := h.buildDiagnosticsView(req, diagnose.Input{}, diagnose.Result{Findings: []diagnose.Finding{
		{ID: "a", Check: "a", Summary: "A.", ConsequenceOf: []string{"b"}},
		{ID: "b", Check: "b", Summary: "B.", ConsequenceOf: []string{"a"}},
	}})
	if len(cyclic.Findings) != 2 {
		t.Errorf("a cyclic relation lost a finding: %+v", cyclic.Findings)
	}
}

// TestClusterStateStripStatesTheOperator proves the header answers
// "what state is the cluster in" from the operator's own words, and
// answers "unknown" — never an empty healthy strip — when nothing was
// observed.
func TestClusterStateStripStatesTheOperator(t *testing.T) {
	t.Parallel()
	if state := clusterStateView(diagnose.Input{}); state.Observed || state.Phase != "unknown" {
		t.Errorf("unobserved cluster rendered as %+v", state)
	}

	desired, ready := 3, 1
	in := diagnose.Input{HasCluster: true, Cluster: observe.Snapshot{Cluster: observe.ClusterFacts{
		Present: true, Phase: "Creating a new replica", PhaseReason: "Creating replica quota-2",
		CurrentPrimary: "quota-1", DesiredInstances: &desired, ReadyInstances: &ready,
	}}}
	state := clusterStateView(in)
	if state.Phase != "Creating a new replica" || state.Instances != "1 of 3 ready" ||
		state.Primary != "quota-1" || state.State != "degraded" {
		t.Errorf("state strip = %+v", state)
	}
}
