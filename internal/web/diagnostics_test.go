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
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/observe"
)

// newDiagnosticsHandler builds a handler with diagnostics on or off and
// the given backup catalog, which is the only input the first detector
// reads.
func newDiagnosticsHandler(t *testing.T, allow bool, snapshots staticSnapshots) *Handler {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h, err := New(Config{
		ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour,
		AllowLogs: true, LevelHeader: "X-PgToolBox-Level", AllowDiagnostics: allow,
	},
		Sources{Cluster: snapshots, Pods: snapshots, Events: snapshots, Backups: snapshots,
			Poolers: snapshots, PoolerPods: snapshots, FailoverQuorum: snapshots,
			ImageCatalogs: snapshots, DatabaseObjects: snapshots, Infrastructure: snapshots},
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
