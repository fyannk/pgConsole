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
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/instancestatus"
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/observe"
)

// TestTriageFollowsTheDiagnosticsGate proves triage exists exactly
// where diagnostics does and at the same level: disabled means 404,
// below poweruser means 403.
func TestTriageFollowsTheDiagnosticsGate(t *testing.T) {
	t.Parallel()
	off := newDiagnosticsHandler(t, false, staticSnapshots{})
	if rec := getWithHeaders(t, off, "/triage", dba); rec.Code != http.StatusNotFound {
		t.Errorf("disabled triage = %d, want 404", rec.Code)
	}
	on := newDiagnosticsHandler(t, true, staticSnapshots{})
	view := map[string]string{"X-Forwarded-User": "v@corp", "X-PgToolBox-Level": "view"}
	if rec := getWithHeaders(t, on, "/triage", view); rec.Code != http.StatusForbidden {
		t.Errorf("view-level triage = %d, want 403", rec.Code)
	}
	power := map[string]string{"X-Forwarded-User": "p@corp", "X-PgToolBox-Level": "poweruser"}
	if rec := getWithHeaders(t, on, "/triage", power); rec.Code != http.StatusOK {
		t.Errorf("poweruser triage = %d, want 200", rec.Code)
	}
}

// TestTriageStartsAtTheStepThatFound proves a hibernated cluster lands
// on the first step of the connection playbook, links the finding to
// its card on the diagnostics screen, and that a step nothing here can
// observe hands the reader the command instead of an answer.
func TestTriageStartsAtTheStepThatFound(t *testing.T) {
	t.Parallel()
	major := 17
	facts := observe.ClusterFacts{Present: true, PostgresMajorVersion: &major, Phase: "Cluster in healthy state",
		CurrentPrimary: "orders-1", Conditions: []observe.Condition{{Type: "cnpg.io/hibernation", Status: "True", Reason: "Hibernated"}}}
	src := staticSnapshots{ok: true, snap: observe.Snapshot{Cluster: facts}, podsOK: true,
		pods: observe.PodsSnapshot{Pods: []observe.PodFacts{{Name: "orders-1", Containers: []observe.ContainerFacts{
			{Name: "bootstrap-controller", Init: true, Image: "ghcr.io/cloudnative-pg/cloudnative-pg:1.30.0"},
			{Name: "postgres", Image: "ghcr.io/cloudnative-pg/postgresql:17.5"}}}}}}
	h := newDiagnosticsHandler(t, true, src)
	power := map[string]string{"X-Forwarded-User": "p@corp", "X-PgToolBox-Level": "poweruser"}
	body := getWithHeaders(t, h, "/triage", power).Body.String()
	for _, want := range []string{
		`<section class="panel playbook" id="cannot-connect">`,
		"start at step 1: Is the cluster deliberately down?",
		`data-start="true"`,
		`href="/diagnostics#finding-cnpg-hibernated"`,
		"Look yourself", "kubectl -n &lt;namespace&gt; get networkpolicies",
		"could not be judged",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("triage misses %q", want)
		}
	}
	// The anchor the link points at exists on the diagnostics screen.
	diagnostics := getWithHeaders(t, h, "/diagnostics", power).Body.String()
	if !strings.Contains(diagnostics, `id="finding-cnpg-hibernated"`) {
		t.Error("the diagnostics screen carries no anchor for the finding triage links to")
	}
}

type staticInstanceStatus struct {
	snap  instancestatus.Snapshot
	swept bool
}

func (s staticInstanceStatus) CurrentInstanceStatus() (instancestatus.Snapshot, bool) {
	return s.snap, s.swept
}

// TestPodsScreenCarriesTheInstancesOwnReports proves the roster shows
// each instance manager's report under its own attribution, marks a
// report the sweep stopped refreshing, names an instance it could not
// read, and says so before the first sweep.
func TestPodsScreenCarriesTheInstancesOwnReports(t *testing.T) {
	t.Parallel()
	failed := testNow.Add(-time.Hour)
	source := staticInstanceStatus{swept: true, snap: instancestatus.Snapshot{
		Interval: 10 * time.Second, SweptAt: testNow,
		Readings: map[string]instancestatus.Reading{
			"orders-1": {Instance: "orders-1", ObservedAt: testNow, IsPrimary: true, IsArchivingWAL: true,
				LastArchivedWAL: "0005", LastFailedWAL: "0006", LastFailedWALTime: &failed,
				TimelineID: 3, CurrentLSN: "0/6000060", ReadyWALFiles: 4, PendingRestart: true, InstanceManagerVersion: "1.30.0"},
			"orders-2": {Instance: "orders-2", ObservedAt: testNow.Add(-10 * time.Minute), InstanceManagerVersion: "1.30.0"},
		},
		Failing: map[string]string{"orders-3": "unavailable"},
	}}
	build := func(status InstanceStatusSource) *Handler {
		t.Helper()
		logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
		h, err := New(Config{ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour, LevelHeader: "X-PgToolBox-Level"},
			Sources{Cluster: staticSnapshots{}, Pods: staticSnapshots{}, Events: staticSnapshots{}, Backups: staticSnapshots{},
				Poolers: staticSnapshots{}, PoolerPods: staticSnapshots{}, FailoverQuorum: staticSnapshots{},
				ImageCatalogs: staticSnapshots{}, DatabaseObjects: staticSnapshots{}, InstanceStatus: status},
			kube.UnavailableProber{}, nil, Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
			nil, nil, func() time.Time { return testNow }, logger)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return h
	}
	power := map[string]string{"X-Forwarded-User": "p@corp", "X-PgToolBox-Level": "poweruser"}
	body := getWithHeaders(t, build(source), "/cluster/pods", power).Body.String()
	for _, want := range []string{
		"<td>orders-1</td><td>primary</td><td>3</td><td>0/6000060</td><td>failing — last failed 0006</td><td>4</td><td>yes</td><td>1.30.0</td>",
		`<tr data-state="stale"><td>orders-2</td>`, "— stale",
		"could not read: orders-3: unavailable",
		"source: instance-reported",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pods screen misses %q", want)
		}
	}
	unswept := getWithHeaders(t, build(staticInstanceStatus{}), "/cluster/pods", power).Body.String()
	if !strings.Contains(unswept, "No sweep yet") {
		t.Error("an unswept source is not said to be so")
	}
}
