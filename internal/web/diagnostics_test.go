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
	"github.com/fyannk/pgConsole/internal/diagnose/catalog"
	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/history"
	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/logstream"
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
// intact. The event finding stands on the refusal alone; the observed
// ResourceQuotas are the other half of the story, carried by the
// quota-exhausted check.
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
		{ID: "mid", Check: "mid", Summary: "Mid.", ConsequenceOf: []diagnose.Relation{{Cause: "root"}}},
		{ID: "leaf", Check: "leaf", Summary: "Leaf.", ConsequenceOf: []diagnose.Relation{{Cause: "mid"}}},
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
		{ID: "a", Check: "a", Summary: "A.", ConsequenceOf: []diagnose.Relation{{Cause: "b"}}},
		{ID: "b", Check: "b", Summary: "B.", ConsequenceOf: []diagnose.Relation{{Cause: "a"}}},
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

// TestIncidentsSortByTheWorstTheyContain proves a warning-severity
// cause holding a critical consequence reads as a critical story: the
// incident outranks a standalone warning that would otherwise tie it.
func TestIncidentsSortByTheWorstTheyContain(t *testing.T) {
	t.Parallel()
	h := newDiagnosticsHandler(t, true, staticSnapshots{})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/diagnostics", nil)

	view := h.buildDiagnosticsView(req, diagnose.Input{}, diagnose.Result{Findings: []diagnose.Finding{
		{ID: "lone", Check: "lone", Severity: diagnose.SeverityWarning, Summary: "Lone warning."},
		{ID: "cause", Check: "cause", Severity: diagnose.SeverityWarning, Summary: "Warning cause."},
		{ID: "effect", Check: "effect", Severity: diagnose.SeverityCritical, Summary: "Critical effect.",
			ConsequenceOf: []diagnose.Relation{{Cause: "cause"}}},
	}})
	if len(view.Findings) != 2 {
		t.Fatalf("cards = %d, want two", len(view.Findings))
	}
	if view.Findings[0].ID != "cause" {
		t.Errorf("the incident holding a critical did not sort first: %+v", view.Findings)
	}
}

// TestGroupIncidentsHonoursScopeAndWindow proves the nesting is
// evidential: a pod-scoped relation joins findings on the same pod and
// leaves one on another pod as its own card, listing the near miss with
// the relation's reason; a window keeps a distant pair apart the same
// way; and the nested card states the relation's terms.
func TestGroupIncidentsHonoursScopeAndWindow(t *testing.T) {
	t.Parallel()
	h := newDiagnosticsHandler(t, true, staticSnapshots{})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/diagnostics", nil)
	pod := func(name string) diagnose.EntityRef { return diagnose.EntityRef{Kind: "Pod", Name: name} }
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	view := h.buildDiagnosticsView(req, diagnose.Input{}, diagnose.Result{Findings: []diagnose.Finding{
		{ID: "disk/orders-1", Check: "disk", Summary: "Disk full on 1.", Subject: pod("orders-1"), At: at},
		{ID: "disk/orders-2", Check: "disk", Summary: "Disk full on 2.", Subject: pod("orders-2"), At: at},
		{ID: "crash/orders-2", Check: "crash", Summary: "Crash on 2.", Subject: pod("orders-2"),
			ConsequenceOf: []diagnose.Relation{{Cause: "disk", Scope: diagnose.ScopePod}}},
		{ID: "crash/orders-3", Check: "crash", Summary: "Crash on 3.", Subject: pod("orders-3"),
			ConsequenceOf: []diagnose.Relation{{Cause: "disk", Scope: diagnose.ScopePod}}},
		{ID: "panic/orders-1", Check: "panic", Summary: "Panic on 1.", Subject: pod("orders-1"), At: at.Add(-3 * time.Hour),
			ConsequenceOf: []diagnose.Relation{{Cause: "disk", Scope: diagnose.ScopePod, Within: time.Hour}}},
		{ID: "exit/orders-2", Check: "exit", Summary: "Exit on 2.", Subject: pod("orders-2"),
			ConsequenceOf: []diagnose.Relation{{Cause: "disk", Scope: diagnose.ScopePod, Within: time.Hour}}},
	}})
	cards := map[string]FindingView{}
	for _, card := range view.Findings {
		cards[card.ID] = card
	}
	if len(cards) != 4 {
		t.Fatalf("cards = %v, want disk×2, the crash on 3, and the distant panic as roots", cardIDs(view.Findings))
	}
	if two := cards["disk/orders-2"]; len(two.Consequences) != 2 || two.Consequences[0].ID != "crash/orders-2" ||
		two.Consequences[1].ID != "exit/orders-2" {
		t.Errorf("the crash and the exit on 2 did not nest under the disk on 2: %+v", two.Consequences)
	} else {
		if two.Consequences[0].Via != "established mechanism · same pod" {
			t.Errorf("nested card states %q as its terms", two.Consequences[0].Via)
		}
		// The exit carries no time, so its window was not enforced and
		// the label must not claim it was.
		if via := two.Consequences[1].Via; !strings.Contains(via, "window not enforced") || strings.Contains(via, "within") {
			t.Errorf("nested card claims a window that was not applied: %q", via)
		}
	}
	if one := cards["disk/orders-1"]; len(one.Consequences) != 0 {
		t.Errorf("the disk on 1 gained a consequence it does not explain: %+v", one.Consequences)
	}
	three := cards["crash/orders-3"]
	if len(three.Related) != 2 {
		t.Fatalf("the crash on 3 should list both disk findings as near misses: %+v", three.Related)
	}
	if !strings.Contains(three.Related[0].Because, "same pod") || three.Related[0].Object != "Pod/orders-1" {
		t.Errorf("near miss does not say why: %+v", three.Related[0])
	}
	panicked := cards["panic/orders-1"]
	if len(panicked.Related) != 2 || !strings.Contains(panicked.Related[0].Because, "apart") {
		t.Errorf("the distant panic should stay a root with the window as the reason: %+v", panicked.Related)
	}
}

// cardIDs lists the root cards' IDs.
func cardIDs(cards []FindingView) []string {
	ids := make([]string, len(cards))
	for i, card := range cards {
		ids[i] = card.ID
	}
	return ids
}

type staticLogs []logstream.Observation

func (l staticLogs) Observations() []logstream.Observation { return l }

func (l staticLogs) Unread() []logstream.Unread { return nil }

// TestDiagnosticsRendersTheWALChainAsOneIncident is the golden test of
// the incident view over the real catalog: the flagship chain — the
// object store refusing credentials, archiving failing, WAL filling the
// volume, PostgreSQL panicking and exiting, the container crash-looping
// — all on one instance renders as one card rooted at the refusal, with
// every link stating its terms, while an unrelated crash loop on another
// instance stays its own card and names the near misses.
func TestDiagnosticsRendersTheWALChainAsOneIncident(t *testing.T) {
	t.Parallel()
	h := newDiagnosticsHandler(t, true, staticSnapshots{})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/diagnostics", nil)
	at := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	major := 17
	observed := func(rule, pod, container, line string, minutesAgo int) logstream.Observation {
		seen := at.Add(-time.Duration(minutesAgo) * time.Minute)
		return logstream.Observation{RuleID: rule, Pod: pod, Container: container, Line: line,
			FirstSeen: seen, LastSeen: seen, Count: 1}
	}
	member := func(name string) observe.PodFacts {
		return observe.PodFacts{Name: name, Containers: []observe.ContainerFacts{
			{Name: "bootstrap-controller", Init: true, Image: "ghcr.io/cloudnative-pg/cloudnative-pg:1.30.0"},
			{Name: "postgres", Image: "ghcr.io/cloudnative-pg/postgresql:17.5", State: "waiting", Reason: "CrashLoopBackOff"},
			{Name: "plugin-barman-cloud", Image: "ghcr.io/cloudnative-pg/plugin-barman-cloud-sidecar:v0.5.0"},
		}}
	}
	in := diagnose.Input{
		Now:        at,
		HasCluster: true,
		Cluster: observe.Snapshot{Cluster: observe.ClusterFacts{
			Present: true, PostgresMajorVersion: &major, CurrentPrimary: "orders-1", TargetPrimary: "orders-1",
			Conditions: []observe.Condition{{Type: "ContinuousArchiving", Status: "False", Reason: "Failing",
				Message: "unexpected failure invoking barman-cloud-wal-archive"}},
		}},
		HasPods: true,
		Pods:    observe.PodsSnapshot{Pods: []observe.PodFacts{member("orders-1"), member("orders-2")}},
		Logs: staticLogs{
			observed("object-store-denied", "orders-1", "plugin-barman-cloud",
				"An error occurred (AccessDenied) when calling the PutObject operation", 30),
			observed("cnpg-wal-disk-full", "orders-1", "postgres", "no free disk space for WALs", 20),
			observed("postgres-panic", "orders-1", "postgres", `{"error_severity":"PANIC","message":"could not write to file"}`, 10),
			observed("cnpg-postgres-exited", "orders-1", "postgres", "PostgreSQL process exited with errors", 9),
		},
	}
	view := h.buildDiagnosticsView(req, in, diagnose.Run(in, catalog.Rules()...))
	cards := map[string]FindingView{}
	for _, card := range view.Findings {
		cards[card.ID] = card
	}
	if len(cards) != 2 {
		t.Fatalf("cards = %v, want the incident and the unrelated crash loop", cardIDs(view.Findings))
	}

	root, ok := cards["object-store-denied/orders-1/plugin-barman-cloud"]
	if !ok {
		t.Fatalf("the incident is not rooted at the object store's refusal: %v", cardIDs(view.Findings))
	}
	nested := map[string]FindingView{}
	for _, consequence := range root.Consequences {
		nested[consequence.ID] = consequence
	}
	for _, id := range []string{
		"cnpg-wal-archiving-failing",
		"cnpg-wal-disk-full/orders-1/postgres",
		"postgres-panic/orders-1/postgres",
		"cnpg-postgres-exited/orders-1/postgres",
		"k8s-container-crashloop/orders-1/postgres",
	} {
		if _, ok := nested[id]; !ok {
			t.Errorf("%s is not inside the incident: %v", id, cardIDs(root.Consequences))
		}
	}
	if len(root.Consequences) != 5 {
		t.Errorf("incident holds %d consequences, want the five links of the chain: %v", len(root.Consequences), cardIDs(root.Consequences))
	}
	if root.Consequences[0].ID != "cnpg-wal-archiving-failing" || root.Consequences[1].ID != "cnpg-wal-disk-full/orders-1/postgres" {
		t.Errorf("chain is not ordered nearest cause first: %v", cardIDs(root.Consequences))
	}
	if via := nested["postgres-panic/orders-1/postgres"].Via; !strings.Contains(via, "same pod") || !strings.Contains(via, "within 1h") {
		t.Errorf("the panic's terms should state the pod scope and the enforced window: %q", via)
	}
	if via := nested["cnpg-wal-archiving-failing"].Via; !strings.Contains(via, "same cluster") {
		t.Errorf("the condition's terms should state the cluster scope: %q", via)
	}

	decoy, ok := cards["k8s-container-crashloop/orders-2/postgres"]
	if !ok {
		t.Fatalf("the crash loop on the other instance was swallowed: %v", cardIDs(view.Findings))
	}
	if len(decoy.Related) == 0 {
		t.Fatal("the unrelated crash loop lists no near miss")
	}
	var namesDisk bool
	for _, miss := range decoy.Related {
		if !strings.Contains(miss.Because, "same pod") {
			t.Errorf("near miss does not give the pod scope as its reason: %+v", miss)
		}
		namesDisk = namesDisk || miss.ID == "cnpg-wal-disk-full/orders-1/postgres"
	}
	if !namesDisk {
		t.Errorf("near misses do not name the disk-full finding on the other pod: %+v", decoy.Related)
	}
}

// TestSwitchedOffChecksGroupApartFromFailingOnes is the reason this
// split exists. With log following off — the default — twenty-seven
// checks report that they could not run, permanently and identically on
// every refresh. Listed beside a scraper that has just stopped
// answering, they teach a reader that the group is settled and worth
// skipping, which is where the one row that changed goes to die.
//
// So the settled ones collapse under their own line, and the group that
// stays open holds only what a reader can act on.
func TestSwitchedOffChecksGroupApartFromFailingOnes(t *testing.T) {
	t.Parallel()
	h := newDiagnosticsHandler(t, true, staticSnapshots{})
	view := h.buildDiagnosticsView(
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/diagnostics", nil),
		diagnose.Input{Now: testNow},
		diagnose.Result{Checks: []diagnose.Check{
			{Name: "logs-off", Outcome: diagnose.CheckUnavailable,
				Because: "log following is off, so nothing in the logs has been read", SourceOff: true},
			{Name: "history-off", Outcome: diagnose.CheckUnavailable,
				Because: "the object timeline is not recorded, so nothing can be counted over time", SourceOff: true},
			{Name: "scraper-stopped", Outcome: diagnose.CheckUnavailable,
				Because: "a reading of cnpg_collector_fencing_on is 1h0m0s old"},
		}},
	)

	var open, settled *CheckGroupView
	for i := range view.Groups {
		switch {
		case strings.Contains(view.Groups[i].Label, "could not run"):
			open = &view.Groups[i]
		case strings.Contains(view.Groups[i].Label, "switched off"):
			settled = &view.Groups[i]
		}
	}
	if open == nil || settled == nil {
		t.Fatalf("the two kinds of unavailable did not group apart: %+v", view.Groups)
	}
	if len(open.Checks) != 1 || open.Checks[0].Name != "scraper-stopped" {
		t.Errorf("the open group holds %+v, want only the source that stopped answering", open.Checks)
	}
	if len(settled.Checks) != 2 {
		t.Errorf("the settled group holds %d checks, want the two switched-off sources", len(settled.Checks))
	}
	// The one a reader must not miss stays open; the decision they have
	// already made stays out of the way.
	if !open.Open {
		t.Error("the group holding a source that stopped answering is collapsed")
	}
	if settled.Open {
		t.Error("the switched-off group is open, which is the noise this change removes")
	}
	// Every check is still on the page: grouping is presentation, never
	// omission.
	if view.Total != 3 {
		t.Errorf("Total = %d, want every check counted", view.Total)
	}
}
