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

	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/observe"
)

// fakeEvidence serves a fixed evidence status.
type fakeEvidence struct {
	status evidence.Status
}

func (f fakeEvidence) CurrentEvidence() evidence.Status { return f.status }

// newEvidenceHandler builds a Handler with the repository-evidence
// source wired next to the usual snapshot sources.
func newEvidenceHandler(t *testing.T, snapshots allSources, status evidence.Status) *Handler {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h, err := New(Config{ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour, AllowLogs: true},
		Sources{Cluster: snapshots, Pods: snapshots, Events: snapshots, Backups: snapshots, Poolers: snapshots, FailoverQuorum: snapshots, ImageCatalogs: snapshots, DatabaseObjects: snapshots, Evidence: fakeEvidence{status: status}},
		kube.FakeProber{}, fakeTailer{}, Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
		nil, nil, func() time.Time { return testNow }, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// uintp returns a pointer to v.
func uintp(v uint64) *uint64 { return &v }

// completeReport is a validated-projection fixture matching the
// observed cluster UID below.
func completeReport() evidence.Report {
	completed := testNow.Add(-10 * time.Minute)
	receipt := testNow.Add(-2 * time.Minute)
	return evidence.Report{
		ProducerVersion:    "0.9.0",
		ClusterNamespace:   "payments",
		ClusterUID:         "uid-1234",
		Provider:           "s3",
		Format:             "barman-cloud",
		ScopeKind:          "barman-server",
		ScopeName:          "orders",
		Fingerprint:        "sha256:" + strings.Repeat("ab", 32),
		Revision:           5,
		EvidenceGeneration: 3,
		CompletedAt:        &completed,
		LastAttemptAt:      &completed,
		Completeness:       "complete",
		Overall:            evidence.StateFact{State: "healthy", Code: "evidence-complete"},
		Capabilities: []evidence.CapabilityFact{
			{ID: "catalog-listing", Support: "supported", State: "healthy", Code: "evidence-complete"},
			{ID: "wal-continuity", Support: "supported", State: "warning", Code: "wal-gap-candidate"},
		},
		Inventory: evidence.InventoryFacts{
			Known:       true,
			ObjectCount: uintp(42), StoredBytes: uintp(1024), UnscopedObjectCount: uintp(1),
		},
		DetailsType: "barman-cloud/v1alpha1",
		Barman: &evidence.BarmanFacts{
			BackupItems:               uintp(2),
			StructurallyUsableBackups: uintp(2),
			BackupStates:              &evidence.StateCountFacts{Healthy: 2},
			WALCounts:                 &evidence.WALCountFacts{Segment: 40, Partial: 1, History: 1},
			WAL:                       evidence.StateFact{State: "healthy", Code: "wal-contiguous"},
			Timeline:                  evidence.StateFact{State: "healthy", Code: "timeline-linear"},
			Coverage:                  evidence.StateFact{State: "healthy", Code: "coverage-frontier"},
			Retention: evidence.RetentionFacts{
				VisibleBackups:            uintp(2),
				StructurallyUsableBackups: uintp(2),
				MinimumConfigured:         true,
				MinimumRedundancy:         uintp(1),
				Result:                    evidence.StateFact{State: "healthy", Code: "retention-met"},
			},
			LatestArchiveReceiptAt: &receipt,
		},
	}
}

// observedCluster returns a cluster snapshot whose observed UID matches
// the report fixture.
func observedCluster(stale bool) staticSnapshots {
	facts := healthyFacts()
	facts.UID = "uid-1234"
	return staticSnapshots{
		snap: observe.Snapshot{Generation: 7, ObservedAt: testNow.Add(-3 * time.Second), Stale: stale, Cluster: facts},
		ok:   true,
	}
}

func TestHandlerBackupEvidenceRendersRepositoryEvidence(t *testing.T) {
	t.Parallel()
	status := evidence.Status{
		HasReport: true,
		Snapshot: evidence.Snapshot{
			Generation: 4,
			ObservedAt: testNow.Add(-20 * time.Second),
			Report:     completeReport(),
		},
	}
	h := newEvidenceHandler(t, observedCluster(false), status)
	body := get(t, h, http.MethodGet, "/backups/evidence").Body.String()
	for _, want := range []string{
		"Repository evidence",
		"sha256:" + strings.Repeat("ab", 32),
		"barman-server orders",
		"s3 barman-cloud",
		"matches the observed cluster UID (current observation)",
		"healthy (evidence-complete)",
		"3 (revision 5)",
		"2 (2 structurally usable)",
		"healthy (wal-contiguous)",
		"healthy (retention-met)",
		"minimum redundancy 1",
		"42 objects, 1024 bytes stored, 1 outside the scope",
		"warning (wal-gap-candidate)",
		"source: repository-evidence",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("repository section misses %q", want)
		}
	}
}

func TestHandlerBackupEvidenceUnknownWithoutContact(t *testing.T) {
	t.Parallel()
	status := evidence.Status{Failure: evidence.FailureUnavailable}
	h := newEvidenceHandler(t, observedCluster(false), status)

	body := get(t, h, http.MethodGet, "/backups/evidence").Body.String()
	if !strings.Contains(body, "Repository evidence") {
		t.Error("enabled consumer without contact renders no repository panel")
	}
	if !strings.Contains(body, "no successful sidecar contact yet (unavailable)") {
		t.Error("panel does not carry the failure kind")
	}
	// The independence of the two sources is the point: a silent sidecar
	// must not take the operator-reported cluster section down with it,
	// and that section is now its own screen.
	if cluster := get(t, h, http.MethodGet, "/cluster/status").Body.String(); !strings.Contains(cluster, "Cluster in healthy state") {
		t.Error("sidecar absence degraded the cluster section")
	}
	if rec := get(t, h, http.MethodGet, "/readyz"); rec.Code != http.StatusOK {
		t.Errorf("readiness = %d with the sidecar absent", rec.Code)
	}
}

func TestHandlerBackupEvidenceStaleRetentionVisible(t *testing.T) {
	t.Parallel()
	status := evidence.Status{
		HasReport: true,
		Failure:   evidence.FailureTimeout,
		Snapshot: evidence.Snapshot{
			Generation: 4,
			ObservedAt: testNow.Add(-10 * time.Minute),
			Stale:      true,
			Failure:    evidence.FailureTimeout,
			Report:     completeReport(),
		},
	}
	h := newEvidenceHandler(t, observedCluster(false), status)
	body := get(t, h, http.MethodGet, "/backups/evidence").Body.String()
	if !strings.Contains(body, "sidecar contact: stale") || !strings.Contains(body, "timeout") {
		t.Error("stale contact line missing its state or failure kind")
	}
	if !strings.Contains(body, "sha256:"+strings.Repeat("ab", 32)) {
		t.Error("stale retention lost the last-good report")
	}
}

func TestHandlerBackupEvidenceSidecarStalenessIsDistinct(t *testing.T) {
	t.Parallel()
	report := completeReport()
	report.SourceStale = true
	report.Overall = evidence.StateFact{State: "unknown", Code: "refresh-failed"}
	status := evidence.Status{
		HasReport: true,
		Snapshot: evidence.Snapshot{
			Generation: 4,
			ObservedAt: testNow.Add(-20 * time.Second),
			Report:     report,
		},
	}
	h := newEvidenceHandler(t, observedCluster(false), status)
	body := get(t, h, http.MethodGet, "/backups/evidence").Body.String()
	if !strings.Contains(body, "sidecar contact: current") {
		t.Error("console contact staleness blended with the sidecar's")
	}
	if !strings.Contains(body, "the sidecar reports its evidence no longer reflects the latest refresh attempt") {
		t.Error("sidecar-reported staleness not rendered")
	}
}

func TestHandlerBackupEvidenceIdentityLines(t *testing.T) {
	t.Parallel()
	status := evidence.Status{
		HasReport: true,
		Snapshot:  evidence.Snapshot{Generation: 1, ObservedAt: testNow, Report: completeReport()},
	}

	noCluster := newEvidenceHandler(t, staticSnapshots{}, status)
	if body := get(t, noCluster, http.MethodGet, "/backups/evidence").Body.String(); !strings.Contains(body, "no observed cluster identity to compare against") {
		t.Error("missing-observation identity line absent")
	}

	staleCluster := newEvidenceHandler(t, observedCluster(true), status)
	if body := get(t, staleCluster, http.MethodGet, "/backups/evidence").Body.String(); !strings.Contains(body, "matches a stale cluster observation — not current agreement") {
		t.Error("stale-observation identity line absent")
	}

	other := observedCluster(false)
	other.snap.Cluster.UID = "uid-9999"
	mismatch := newEvidenceHandler(t, other, status)
	if body := get(t, mismatch, http.MethodGet, "/backups/evidence").Body.String(); !strings.Contains(body, "bound to a different cluster incarnation") {
		t.Error("mismatch identity line absent")
	}
}

func TestHandlerBackupEvidenceUnknownVariantExplicit(t *testing.T) {
	t.Parallel()
	report := completeReport()
	report.Barman = nil
	report.DetailsType = "pgbackrest/v9"
	status := evidence.Status{
		HasReport: true,
		Snapshot:  evidence.Snapshot{Generation: 1, ObservedAt: testNow, Report: report},
	}
	h := newEvidenceHandler(t, observedCluster(false), status)
	body := get(t, h, http.MethodGet, "/backups/evidence").Body.String()
	if !strings.Contains(body, "format details unknown: unrecognized variant pgbackrest/v9") {
		t.Error("unknown variant not rendered explicitly")
	}
	if strings.Contains(body, "WAL continuity") {
		t.Error("unknown variant rendered format-owned conclusions")
	}
}

func TestHandlerIndexRepositoryAbsentWhenDisabled(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, observedCluster(false), kube.FakeProber{}, Links{})
	if body := get(t, h, http.MethodGet, "/").Body.String(); strings.Contains(body, "Repository evidence") {
		t.Error("disabled consumer still renders a repository section")
	}
}
