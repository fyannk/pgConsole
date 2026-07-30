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

package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1alpha1 "github.com/fyannk/pgObjectStoreViewer/api/evidence/v1alpha1"
)

// Canary values that must never survive the projection: they stand in
// for the free-text producer messages the contract forbids proxying.
const (
	canaryMessage = "CANARY-message-with-bucket-url-s3://secret-bucket/prefix"
	testToken     = "0123456789abcdefghijklmnopqrstuvwxyzABCDEF-"
)

// testFingerprint is a contract-shaped destination fingerprint.
var testFingerprint = "sha256:" + strings.Repeat("ab", 32)

// testExpectation matches the fixtures below.
func testExpectation() Expectation {
	return Expectation{
		Fingerprint:  testFingerprint,
		BarmanServer: "orders",
		Namespace:    "payments",
	}
}

// allCapabilities returns the complete sorted capability set in the
// producer's canonical order, every entry unknown.
func allCapabilities() []v1alpha1.Capability {
	ids := []v1alpha1.CapabilityID{
		v1alpha1.CatalogListing,
		v1alpha1.DependencyValidation,
		v1alpha1.EncryptedMetadata,
		v1alpha1.ObjectInventory,
		v1alpha1.RecoveryCoverage,
		v1alpha1.RetentionExpectation,
		v1alpha1.StructuralValidation,
		v1alpha1.TimelineTraversal,
		v1alpha1.WALContinuity,
	}
	capabilities := make([]v1alpha1.Capability, 0, len(ids))
	for _, id := range ids {
		capabilities = append(capabilities, v1alpha1.Capability{
			ID:      id,
			Support: v1alpha1.SupportUnknown,
			State:   v1alpha1.Unknown,
			Reason:  v1alpha1.Reason{Code: "no-completed-scan", Message: canaryMessage},
		})
	}
	return capabilities
}

// unscannedSnapshot is a valid pre-first-scan publication.
func unscannedSnapshot() v1alpha1.RepositoryEvidenceSnapshot {
	name := "orders"
	return v1alpha1.RepositoryEvidenceSnapshot{
		APIVersion: v1alpha1.APIVersion,
		Kind:       v1alpha1.SnapshotKind,
		Producer:   v1alpha1.Producer{Name: v1alpha1.ProducerName, Version: "0.9.0"},
		Identity: v1alpha1.Identity{
			Cluster: v1alpha1.ClusterIdentity{Namespace: "payments", UID: "uid-1234", Name: &name},
			Repository: v1alpha1.RepositoryIdentity{
				Provider:               "s3",
				Format:                 "barman-cloud",
				DestinationFingerprint: testFingerprint,
				Scope:                  v1alpha1.ScopeIdentity{Kind: "barman-server", Name: "orders"},
			},
		},
		Revision:     1,
		Completeness: v1alpha1.Unscanned,
		State:        v1alpha1.Unknown,
		Reason:       v1alpha1.Reason{Code: "no-completed-scan", Message: canaryMessage},
		Capabilities: allCapabilities(),
		Inventory:    v1alpha1.InventorySummary{},
		Details: mustDetails(fmt.Sprintf(
			`{"type":%q,"barman_cloud":{"wal":{"state":"unknown","reason":{"code":"no-completed-scan","message":%q}},"timeline":{"state":"unknown","reason":{"code":"no-completed-scan","message":%q}},"coverage":{"state":"unknown","reason":{"code":"no-completed-scan","message":%q}},"retention":{"minimum_configured":false,"policy_configured":false,"state":"unknown","reason":{"code":"no-completed-scan","message":%q}},"ranges_truncated":false,"diagnostics_truncated":false}}`,
			v1alpha1.BarmanDetailsType, canaryMessage, canaryMessage, canaryMessage, canaryMessage)),
	}
}

// completeSnapshot is a valid complete-evidence publication with a
// recognized Barman summary.
func completeSnapshot() v1alpha1.RepositoryEvidenceSnapshot {
	started := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	completed := started.Add(2 * time.Minute)
	two := uint64(2)
	minimum := uint64(1)
	snapshot := unscannedSnapshot()
	snapshot.Revision = 5
	snapshot.EvidenceGeneration = 3
	snapshot.StartedAt = &started
	snapshot.CompletedAt = &completed
	snapshot.LastAttemptAt = &completed
	snapshot.Completeness = v1alpha1.Complete
	snapshot.State = v1alpha1.Healthy
	snapshot.Reason = v1alpha1.Reason{Code: "evidence-complete", Message: canaryMessage}
	for index := range snapshot.Capabilities {
		snapshot.Capabilities[index].Support = v1alpha1.Supported
		snapshot.Capabilities[index].State = v1alpha1.Healthy
		snapshot.Capabilities[index].Reason = v1alpha1.Reason{Code: "evidence-complete", Message: canaryMessage}
	}
	snapshot.Inventory = v1alpha1.InventorySummary{
		Known:               true,
		ObjectCount:         &two,
		StoredBytes:         &two,
		UnscopedObjectCount: &minimum,
		PagesExamined:       4,
		ObjectsExamined:     2,
	}
	snapshot.Details = mustDetails(fmt.Sprintf(
		`{"type":%q,"barman_cloud":{"backup_items":2,"structurally_usable_backups":2,"backup_states":{"healthy":2,"warning":0,"unhealthy":0,"unknown":0},"wal_counts":{"segment":40,"partial":1,"history":1,"backup_history":0,"unknown":0,"duplicate":0},"wal":{"state":"healthy","reason":{"code":"wal-contiguous","message":%q}},"timeline":{"state":"healthy","reason":{"code":"timeline-linear","message":%q}},"coverage":{"state":"healthy","reason":{"code":"coverage-frontier","message":%q}},"retention":{"visible_backups":2,"structurally_usable_backups":2,"oldest_completion_at":"2026-07-27T06:00:00Z","newest_completion_at":"2026-07-29T06:00:00Z","minimum_configured":true,"minimum_redundancy":1,"policy_configured":false,"state":"healthy","reason":{"code":"retention-met","message":%q}},"latest_archive_receipt_at":"2026-07-29T06:01:00Z","ranges_truncated":false,"diagnostics_truncated":false}}`,
		v1alpha1.BarmanDetailsType, canaryMessage, canaryMessage, canaryMessage, canaryMessage))
	return snapshot
}

// mustDetails builds a Details union through its own unmarshaler, the
// only way the producer type accepts a payload.
func mustDetails(raw string) v1alpha1.Details {
	var details v1alpha1.Details
	if err := json.Unmarshal([]byte(raw), &details); err != nil {
		panic(err)
	}
	return details
}

// writeToken writes a valid token file and returns its path.
func writeToken(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(content), 0o400); err != nil {
		t.Fatal(err)
	}
	return path
}

// sidecar serves the given handler on a real Unix socket and returns
// the socket path.
func sidecar(t *testing.T, handler http.Handler) string {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "evidence.sock")
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socketPath
}

// serveSnapshot returns a handler that checks the bearer token and
// serves the given snapshot with the contract media type.
func serveSnapshot(t *testing.T, snapshot v1alpha1.RepositoryEvidenceSnapshot) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != snapshotPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", mediaType)
		if err := json.NewEncoder(w).Encode(snapshot); err != nil {
			t.Errorf("encode snapshot: %v", err)
		}
	})
}

// newTestClient builds a client against the given handler.
func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	client, err := NewClient(sidecar(t, handler), writeToken(t, testToken+"\n"), testExpectation())
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestFetchSnapshotProjectsCompleteEvidence(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, serveSnapshot(t, completeSnapshot()))

	report, err := client.FetchSnapshot(context.Background())
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}
	if report.Fingerprint != testFingerprint || report.ScopeName != "orders" || report.ClusterUID != "uid-1234" {
		t.Errorf("identity projection = %q %q %q", report.Fingerprint, report.ScopeName, report.ClusterUID)
	}
	if report.Revision != 5 || report.EvidenceGeneration != 3 || report.Completeness != "complete" {
		t.Errorf("publication projection = %d %d %q", report.Revision, report.EvidenceGeneration, report.Completeness)
	}
	if report.Overall.State != "healthy" || report.Overall.Code != "evidence-complete" {
		t.Errorf("overall = %+v", report.Overall)
	}
	if len(report.Capabilities) != 9 || report.Capabilities[0].ID != "catalog-listing" {
		t.Errorf("capabilities = %+v", report.Capabilities)
	}
	if report.Barman == nil {
		t.Fatal("Barman projection missing")
	}
	if report.Barman.WAL.State != "healthy" || report.Barman.Retention.Result.Code != "retention-met" {
		t.Errorf("barman projection = %+v %+v", report.Barman.WAL, report.Barman.Retention.Result)
	}
	if report.Barman.BackupItems == nil || *report.Barman.BackupItems != 2 {
		t.Errorf("backup items = %v", report.Barman.BackupItems)
	}
}

func TestFetchSnapshotNeverCarriesProducerMessages(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, serveSnapshot(t, completeSnapshot()))

	report, err := client.FetchSnapshot(context.Background())
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}
	rendered := fmt.Sprintf("%+v", report)
	if strings.Contains(rendered, "CANARY") || strings.Contains(rendered, "secret-bucket") {
		t.Error("projection carries producer message content")
	}
}

func TestFetchSnapshotUnknownDetailsVariantStaysUnknown(t *testing.T) {
	t.Parallel()
	snapshot := unscannedSnapshot()
	snapshot.Details = mustDetails(`{"type":"pgbackrest/v9","future_payload":{"secret":"CANARY"}}`)
	client := newTestClient(t, serveSnapshot(t, snapshot))

	report, err := client.FetchSnapshot(context.Background())
	if err != nil {
		t.Fatalf("FetchSnapshot: %v", err)
	}
	if report.Barman != nil {
		t.Error("unknown variant produced a Barman projection")
	}
	if report.DetailsType != "pgbackrest/v9" {
		t.Errorf("DetailsType = %q", report.DetailsType)
	}
	if rendered := fmt.Sprintf("%+v", report); strings.Contains(rendered, "CANARY") {
		t.Error("unknown variant payload crossed the boundary")
	}
}

func TestFetchSnapshotFailureKinds(t *testing.T) {
	t.Parallel()
	wrongFingerprint := unscannedSnapshot()
	wrongFingerprint.Identity.Repository.DestinationFingerprint = "sha256:" + strings.Repeat("cd", 32)
	wrongServer := unscannedSnapshot()
	wrongServer.Identity.Repository.Scope.Name = "other"
	wrongServer.Identity.Repository.Scope.Kind = "barman-server"
	wrongNamespace := unscannedSnapshot()
	wrongNamespace.Identity.Cluster.Namespace = "other"
	newerVersion := unscannedSnapshot()
	newerVersion.APIVersion = "evidence.objectstoreviewer.io/v1alpha2"
	invalid := unscannedSnapshot()
	invalid.State = v1alpha1.Healthy

	cases := []struct {
		name    string
		handler http.Handler
		want    FailureKind
	}{
		{
			name: "unauthenticated",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			}),
			want: FailureUnauthenticated,
		},
		{
			name: "busy",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusTooManyRequests)
			}),
			want: FailureBusy,
		},
		{
			name: "server failure",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}),
			want: FailureUnavailable,
		},
		{
			name: "wrong media type",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("{}"))
			}),
			want: FailureIncompatible,
		},
		{
			name:    "newer schema version",
			handler: serveSnapshot(t, newerVersion),
			want:    FailureIncompatible,
		},
		{
			name:    "invalid snapshot invariants",
			handler: serveSnapshot(t, invalid),
			want:    FailureInvalid,
		},
		{
			name: "oversized response",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", mediaType)
				_, _ = w.Write([]byte(`{"pad":"` + strings.Repeat("x", snapshotMaxBytes) + `"}`))
			}),
			want: FailureInvalid,
		},
		{
			name:    "fingerprint mismatch",
			handler: serveSnapshot(t, wrongFingerprint),
			want:    FailureIdentityMismatch,
		},
		{
			name:    "barman server mismatch",
			handler: serveSnapshot(t, wrongServer),
			want:    FailureIdentityMismatch,
		},
		{
			name:    "namespace mismatch",
			handler: serveSnapshot(t, wrongNamespace),
			want:    FailureIdentityMismatch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := newTestClient(t, tc.handler)
			_, err := client.FetchSnapshot(context.Background())
			if err == nil {
				t.Fatal("FetchSnapshot succeeded")
			}
			if kind := KindOf(err); kind != tc.want {
				t.Errorf("kind = %q, want %q", kind, tc.want)
			}
			if text := err.Error(); strings.Contains(text, "CANARY") || strings.Contains(text, "sha256:") {
				t.Errorf("error text carries response detail: %q", text)
			}
		})
	}
}

func TestFetchSnapshotAbsentSidecarIsUnavailable(t *testing.T) {
	t.Parallel()
	client, err := NewClient(filepath.Join(t.TempDir(), "missing.sock"), writeToken(t, testToken), testExpectation())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.FetchSnapshot(context.Background())
	if err == nil {
		t.Fatal("FetchSnapshot succeeded without a socket")
	}
	if kind := KindOf(err); kind != FailureUnavailable {
		t.Errorf("kind = %q, want %q", kind, FailureUnavailable)
	}
}

func TestFetchSnapshotSlowSidecarTimesOut(t *testing.T) {
	t.Parallel()
	blocked := make(chan struct{})
	t.Cleanup(func() { close(blocked) })
	client := newTestClient(t, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-blocked
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := client.FetchSnapshot(ctx)
	if err == nil {
		t.Fatal("FetchSnapshot succeeded against a hung sidecar")
	}
	if kind := KindOf(err); kind != FailureTimeout {
		t.Errorf("kind = %q, want %q", kind, FailureTimeout)
	}
}

func TestNewClientTokenValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		wantErr bool
	}{
		{name: "exact token", content: testToken},
		{name: "token with trailing newline", content: testToken + "\n"},
		{name: "empty file", content: "", wantErr: true},
		{name: "short token", content: testToken[:20], wantErr: true},
		{name: "padded base64", content: testToken[:41] + "==", wantErr: true},
		{name: "embedded newline", content: testToken[:21] + "\n" + testToken[22:], wantErr: true},
		{name: "oversized file", content: strings.Repeat("a", tokenFileMaxBytes+1), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewClient("/run/evidence.sock", writeToken(t, tc.content), testExpectation())
			if got := err != nil; got != tc.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), testToken[:10]) {
				t.Error("error text carries token content")
			}
		})
	}
}

func TestNewClientRejectsMissingAndSymlinkedToken(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := NewClient("/run/evidence.sock", filepath.Join(dir, "absent"), testExpectation()); err == nil {
		t.Error("NewClient accepted a missing token file")
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(testToken), 0o400); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient("/run/evidence.sock", link, testExpectation()); err == nil {
		t.Error("NewClient accepted a symlinked token file")
	}
}

// repoBackupItem builds one valid wire backup item.
func repoBackupItem(id string, state v1alpha1.State) v1alpha1.BarmanBackup {
	status := "DONE"
	return v1alpha1.BarmanBackup{
		Server:   "orders",
		BackupID: id,
		Status:   &status,
		State:    state,
		Reason:   v1alpha1.Reason{Code: "structural-evidence", Message: canaryMessage},
	}
}

// servePages returns a handler serving /backups pages keyed by cursor,
// next to the given snapshot on /snapshot.
func servePages(t *testing.T, snapshot v1alpha1.RepositoryEvidenceSnapshot, pages map[string]v1alpha1.BarmanBackupPage) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", mediaType)
		switch r.URL.Path {
		case snapshotPath:
			_ = json.NewEncoder(w).Encode(snapshot)
		case backupsPath:
			page, ok := pages[r.URL.Query().Get("cursor")]
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(page)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// pageHeader builds the common valid page envelope.
func pageHeader(revision, generation uint64, next *string) v1alpha1.PageHeader {
	return v1alpha1.PageHeader{
		APIVersion:         v1alpha1.APIVersion,
		Kind:               v1alpha1.BarmanBackupPageKind,
		Revision:           revision,
		EvidenceGeneration: generation,
		NextCursor:         next,
	}
}

func TestFetchBackupsAssemblesCursorChain(t *testing.T) {
	t.Parallel()
	next := "cursor-2"
	pages := map[string]v1alpha1.BarmanBackupPage{
		"": {PageHeader: pageHeader(5, 3, &next), Items: []v1alpha1.BarmanBackup{
			repoBackupItem("20260728T060000", v1alpha1.Healthy),
			repoBackupItem("20260729T060000", v1alpha1.Warning),
		}},
		next: {PageHeader: pageHeader(5, 3, nil), Items: []v1alpha1.BarmanBackup{
			repoBackupItem("20260729T180000", v1alpha1.Unhealthy),
		}},
	}
	client := newTestClient(t, servePages(t, completeSnapshot(), pages))

	backups, truncated, err := client.FetchBackups(context.Background(), 5, 3)
	if err != nil {
		t.Fatalf("FetchBackups: %v", err)
	}
	if truncated || len(backups) != 3 {
		t.Fatalf("assembly = %d items, truncated %v", len(backups), truncated)
	}
	if backups[0].BackupID != "20260728T060000" || backups[2].Result.State != "unhealthy" {
		t.Errorf("projection = %+v", backups)
	}
	if backups[0].Status != "DONE" || backups[0].Result.Code != "structural-evidence" {
		t.Errorf("first item projection = %+v", backups[0])
	}
	if rendered := fmt.Sprintf("%+v", backups); strings.Contains(rendered, "CANARY") {
		t.Error("backup projection carries producer message content")
	}
}

func TestFetchBackupsPublicationChangeIsItsOwnKind(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", mediaType)
		w.WriteHeader(http.StatusConflict)
	}))
	_, _, err := client.FetchBackups(context.Background(), 5, 3)
	if kind := KindOf(err); kind != FailurePublicationChanged {
		t.Errorf("kind = %q, want %q", kind, FailurePublicationChanged)
	}
}

func TestFetchBackupsRejectsForeignPublicationIdentity(t *testing.T) {
	t.Parallel()
	pages := map[string]v1alpha1.BarmanBackupPage{
		"": {PageHeader: pageHeader(9, 8, nil), Items: []v1alpha1.BarmanBackup{}},
	}
	client := newTestClient(t, servePages(t, completeSnapshot(), pages))
	_, _, err := client.FetchBackups(context.Background(), 5, 3)
	if kind := KindOf(err); kind != FailureInvalid {
		t.Errorf("kind = %q, want %q", kind, FailureInvalid)
	}
}

func TestFetchBackupsConsumerCeilingCutsVisibly(t *testing.T) {
	t.Parallel()
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", mediaType)
		start := 0
		if cursor := r.URL.Query().Get("cursor"); cursor != "" {
			_, _ = fmt.Sscanf(cursor, "at-%d", &start)
		}
		items := make([]v1alpha1.BarmanBackup, 0, 200)
		for index := start; index < start+200; index++ {
			items = append(items, repoBackupItem(fmt.Sprintf("20260729T%06d", index), v1alpha1.Healthy))
		}
		next := fmt.Sprintf("at-%d", start+200)
		page := v1alpha1.BarmanBackupPage{PageHeader: pageHeader(5, 3, &next), Items: items}
		_ = json.NewEncoder(w).Encode(page)
	}))

	backups, truncated, err := client.FetchBackups(context.Background(), 5, 3)
	if err != nil {
		t.Fatalf("FetchBackups: %v", err)
	}
	if !truncated || len(backups) != MaxRepositoryBackups {
		t.Errorf("assembly = %d items, truncated %v", len(backups), truncated)
	}
}
