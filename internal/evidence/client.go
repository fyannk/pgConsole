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
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	v1alpha1 "github.com/fyannk/pgObjectStoreViewer/api/evidence/v1alpha1"

	"github.com/fyannk/pgconsole/internal/redact"
)

// Contract constants of the evidence API channel.
const (
	// requestDeadline is the contract's pinned hard deadline for every
	// API request.
	requestDeadline = 5 * time.Second
	// snapshotMaxBytes is the contract's snapshot response ceiling.
	snapshotMaxBytes = 256 * 1024
	// tokenFileMaxBytes is the contract's mounted token file ceiling.
	tokenFileMaxBytes = 128
	// tokenLength is the exact unpadded base64url token length.
	tokenLength = 43
	// mediaType is the versioned evidence media type every response
	// must carry.
	mediaType = "application/vnd.objectstoreviewer.evidence.v1alpha1+json"
	// snapshotPath is the snapshot route.
	snapshotPath = "/api/v1alpha1/snapshot"
	// backupsPath is the generation-consistent backup collection route.
	backupsPath = "/api/v1alpha1/backups"
	// pageMaxBytes is the contract's collection response ceiling.
	pageMaxBytes = 1024 * 1024
	// pageRequestLimit is the contract's maximum page size, requested
	// outright to minimize round trips.
	pageRequestLimit = 200
	// maxAssemblyPages bounds one cursor chain; a producer serving
	// more pages than the assembly ceiling could ever accept is
	// misbehaving, not large.
	maxAssemblyPages = 20
)

// MaxRepositoryBackups bounds the consumer-side backup assembly. A
// larger collection is cut visibly: matched correlations stay valid,
// while absence conclusions become unknown.
const MaxRepositoryBackups = 1000

// Expectation is the operator-configured identity the evidence
// responses must match exactly. The cluster UID is deliberately absent:
// correlation is observed-UID-only and happens at rendering against
// the console's own observation, never against injected configuration.
type Expectation struct {
	// Fingerprint is the full expected destination fingerprint.
	Fingerprint string
	// BarmanServer is the exact expected Barman server name.
	BarmanServer string
	// Namespace is the configured target cluster namespace.
	Namespace string
}

// Client fetches and validates evidence snapshots over the pod-private
// Unix socket. It is safe for concurrent use.
type Client struct {
	http   *http.Client
	token  string
	expect Expectation
}

// failure is a categorized consumer-side error carrying its closed
// failure kind. Its message is the kind alone; transport detail stays
// wrapped and never crosses an output boundary.
type failure struct {
	kind  FailureKind
	cause error
}

// Error returns the failure kind and nothing else.
func (f *failure) Error() string { return "evidence: " + string(f.kind) }

// Unwrap exposes the cause for in-process branching only.
func (f *failure) Unwrap() error { return f.cause }

// Category maps the failure kind onto the console's closed redaction
// categories for logging.
func (f *failure) Category() redact.Category {
	switch f.kind {
	case FailureTimeout:
		return redact.CategoryTimeout
	case FailureCanceled:
		return redact.CategoryCanceled
	case FailureUnauthenticated:
		return redact.CategoryForbidden
	case FailureUnavailable, FailureBusy, FailureIncompatible, FailurePublicationChanged:
		return redact.CategoryUnavailable
	default:
		return redact.CategoryInternal
	}
}

// KindOf reports the closed failure kind of a client error. Errors
// from outside the package map onto the transport-shaped kinds.
func KindOf(err error) FailureKind {
	var f *failure
	if errors.As(err, &f) {
		return f.kind
	}
	switch redact.Categorize(err) {
	case redact.CategoryTimeout:
		return FailureTimeout
	case redact.CategoryCanceled:
		return FailureCanceled
	default:
		return FailureUnavailable
	}
}

// fail builds a kind-carrying error.
func fail(kind FailureKind, cause error) error {
	return &failure{kind: kind, cause: cause}
}

// NewClient validates the mounted token file and builds the
// socket-bound client. Every validation failure here happens strictly
// before the console listens; no error ever contains the token, the
// socket path, or file content.
func NewClient(socketPath, tokenFile string, expect Expectation) (*Client, error) {
	token, err := loadToken(tokenFile)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
		MaxIdleConns:    2,
		IdleConnTimeout: time.Minute,
	}
	return &Client{
		http:   &http.Client{Transport: transport},
		token:  token,
		expect: expect,
	}, nil
}

// SocketPathFromURL reduces the validated configuration value — a
// unix:// URI or an absolute path — to the socket path.
func SocketPathFromURL(raw string) string {
	return strings.TrimPrefix(raw, "unix://")
}

// loadToken reads and validates the pod-local bearer token: a regular
// file of at most 128 bytes holding exactly 43 unpadded base64url
// characters, with one optional trailing line feed.
func loadToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", redact.NewError("evidence token", redact.CategoryUnavailable, err)
	}
	if !info.Mode().IsRegular() {
		return "", redact.NewError("evidence token", redact.CategoryInternal,
			errors.New("token file must be a regular file"))
	}
	if info.Size() > tokenFileMaxBytes {
		return "", redact.NewError("evidence token", redact.CategoryInternal,
			errors.New("token file exceeds the contract bound"))
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the path is the validated operator-mounted token file from configuration, never request data.
	if err != nil {
		return "", redact.NewError("evidence token", redact.CategoryUnavailable, err)
	}
	token := strings.TrimSuffix(string(raw), "\n")
	if len(token) != tokenLength || !base64urlOnly(token) {
		return "", redact.NewError("evidence token", redact.CategoryInternal,
			errors.New("token file does not hold one unpadded base64url token"))
	}
	return token, nil
}

// base64urlOnly reports whether the value uses only the unpadded
// base64url alphabet.
func base64urlOnly(value string) bool {
	for _, character := range value {
		switch {
		case character >= 'A' && character <= 'Z':
		case character >= 'a' && character <= 'z':
		case character >= '0' && character <= '9':
		case character == '-' || character == '_':
		default:
			return false
		}
	}
	return true
}

// getJSON performs one authenticated, bounded GET under the contract's
// per-request deadline and returns the body. Every error carries a
// closed FailureKind and no response or transport detail.
func (c *Client) getJSON(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestDeadline)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://evidence"+path, nil)
	if err != nil {
		return nil, fail(FailureInvalid, err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fail(transportKind(ctx, err), err)
	}
	defer func() { _ = response.Body.Close() }()

	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, fail(FailureUnauthenticated, nil)
	case http.StatusTooManyRequests:
		return nil, fail(FailureBusy, nil)
	case http.StatusConflict:
		return nil, fail(FailurePublicationChanged, nil)
	default:
		return nil, fail(FailureUnavailable,
			fmt.Errorf("evidence status %d", response.StatusCode))
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, mediaType) {
		return nil, fail(FailureIncompatible, errors.New("unexpected media type"))
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, fail(transportKind(ctx, err), err)
	}
	if int64(len(body)) > maxBytes {
		return nil, fail(FailureInvalid, errors.New("response exceeds its ceiling"))
	}
	return body, nil
}

// FetchSnapshot performs one bounded snapshot request, validates the
// response with the producer module's own invariants, checks the
// configured identity expectation, and returns the redacted
// projection.
func (c *Client) FetchSnapshot(ctx context.Context) (Report, error) {
	body, err := c.getJSON(ctx, snapshotPath, snapshotMaxBytes)
	if err != nil {
		return Report{}, err
	}
	var snapshot v1alpha1.RepositoryEvidenceSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return Report{}, fail(FailureInvalid, err)
	}
	if snapshot.APIVersion != v1alpha1.APIVersion || snapshot.Kind != v1alpha1.SnapshotKind {
		return Report{}, fail(FailureIncompatible, errors.New("incompatible version or kind"))
	}
	if err := snapshot.Validate(); err != nil {
		return Report{}, fail(FailureInvalid, err)
	}
	if err := c.checkIdentity(snapshot.Identity); err != nil {
		return Report{}, fail(FailureIdentityMismatch, err)
	}
	return project(snapshot), nil
}

// FetchBackups assembles the backup collection of one publication
// identity through generation-consistent pages. Every page is
// validated with the producer module's invariants and must carry the
// requested revision and generation exactly; a publication change
// surfaces as its own failure kind and discards the partial assembly.
// The consumer ceiling cuts visibly: the second return reports
// truncation.
func (c *Client) FetchBackups(ctx context.Context, revision, generation uint64) ([]RepoBackup, bool, error) {
	items := []RepoBackup{}
	cursor := ""
	for range maxAssemblyPages {
		path := fmt.Sprintf("%s?revision=%d&limit=%d", backupsPath, revision, pageRequestLimit)
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		body, err := c.getJSON(ctx, path, pageMaxBytes)
		if err != nil {
			return nil, false, err
		}
		var page v1alpha1.BarmanBackupPage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, false, fail(FailureInvalid, err)
		}
		if page.APIVersion != v1alpha1.APIVersion || page.Kind != v1alpha1.BarmanBackupPageKind {
			return nil, false, fail(FailureIncompatible, errors.New("incompatible page version or kind"))
		}
		if err := page.Validate(); err != nil {
			return nil, false, fail(FailureInvalid, err)
		}
		if page.Revision != revision || page.EvidenceGeneration != generation {
			return nil, false, fail(FailureInvalid, errors.New("page publication identity mismatch"))
		}
		for _, item := range page.Items {
			if len(items) >= MaxRepositoryBackups {
				return items, true, nil
			}
			items = append(items, projectBackup(item))
		}
		if page.NextCursor == nil {
			return items, false, nil
		}
		cursor = *page.NextCursor
	}
	return nil, false, fail(FailureInvalid, errors.New("cursor chain exceeds the assembly bound"))
}

// projectBackup reduces one validated backup item to the redacted
// projection.
func projectBackup(item v1alpha1.BarmanBackup) RepoBackup {
	backup := RepoBackup{
		Server:              item.Server,
		BackupID:            item.BackupID,
		Result:              StateFact{State: string(item.State), Code: item.Reason.Code},
		Timeline:            item.Timeline,
		BeginAt:             item.BeginAt,
		EndAt:               item.EndAt,
		StoredArtifactBytes: item.StoredArtifactBytes,
	}
	if item.Status != nil {
		backup.Status = *item.Status
	}
	if item.BackupType != nil {
		backup.BackupType = *item.BackupType
	}
	return backup
}

// transportKind classifies a transport error against the request
// deadline and the caller's cancellation.
func transportKind(ctx context.Context, err error) FailureKind {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return FailureTimeout
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return FailureCanceled
	default:
		return FailureUnavailable
	}
}

// checkIdentity enforces the operator-configured expectation: exact
// destination fingerprint, exact Barman server scope, and the target
// namespace. Evidence for any other repository or cluster is a
// mismatch, never a lesser answer.
func (c *Client) checkIdentity(identity v1alpha1.Identity) error {
	if identity.Repository.DestinationFingerprint != c.expect.Fingerprint {
		return errors.New("destination fingerprint mismatch")
	}
	if identity.Repository.Scope.Name != c.expect.BarmanServer {
		return errors.New("barman server mismatch")
	}
	if identity.Cluster.Namespace != c.expect.Namespace {
		return errors.New("cluster namespace mismatch")
	}
	return nil
}

// project reduces a validated snapshot to the redacted console
// projection: states, codes, counts, identifiers, and times. Producer
// messages are dropped here and nowhere later.
func project(s v1alpha1.RepositoryEvidenceSnapshot) Report {
	report := Report{
		ProducerVersion:    s.Producer.Version,
		ClusterNamespace:   s.Identity.Cluster.Namespace,
		ClusterUID:         s.Identity.Cluster.UID,
		Provider:           s.Identity.Repository.Provider,
		Format:             s.Identity.Repository.Format,
		ScopeKind:          s.Identity.Repository.Scope.Kind,
		ScopeName:          s.Identity.Repository.Scope.Name,
		Fingerprint:        s.Identity.Repository.DestinationFingerprint,
		Revision:           s.Revision,
		EvidenceGeneration: s.EvidenceGeneration,
		StartedAt:          s.StartedAt,
		CompletedAt:        s.CompletedAt,
		LastAttemptAt:      s.LastAttemptAt,
		Completeness:       string(s.Completeness),
		SourceStale:        s.Stale,
		Overall:            StateFact{State: string(s.State), Code: s.Reason.Code},
		DetailsType:        s.Details.Type,
	}
	if s.Identity.Cluster.Name != nil {
		report.ClusterName = *s.Identity.Cluster.Name
	}
	for _, capability := range s.Capabilities {
		report.Capabilities = append(report.Capabilities, CapabilityFact{
			ID:      string(capability.ID),
			Support: string(capability.Support),
			State:   string(capability.State),
			Code:    capability.Reason.Code,
		})
	}
	report.Inventory = InventoryFacts{
		Known:               s.Inventory.Known,
		ObjectCount:         s.Inventory.ObjectCount,
		StoredBytes:         s.Inventory.StoredBytes,
		UnscopedObjectCount: s.Inventory.UnscopedObjectCount,
		PagesExamined:       s.Inventory.PagesExamined,
		ObjectsExamined:     s.Inventory.ObjectsExamined,
	}
	if s.Inventory.LastFailureCategory != nil {
		report.Inventory.LastFailureCategory = string(*s.Inventory.LastFailureCategory)
	}
	if !s.Details.Unknown() && s.Details.BarmanCloud != nil {
		report.Barman = projectBarman(*s.Details.BarmanCloud)
	}
	return report
}

// projectBarman reduces the recognized barman-cloud summary variant.
func projectBarman(b v1alpha1.BarmanCloudSummary) *BarmanFacts {
	facts := &BarmanFacts{
		BackupItems:               b.BackupItems,
		WALRangeItems:             b.WALRangeItems,
		WALGapItems:               b.WALGapItems,
		RecoveryPathItems:         b.RecoveryPathItems,
		StructurallyUsableBackups: b.StructurallyUsableBackups,
		WAL:                       StateFact{State: string(b.WAL.State), Code: b.WAL.Reason.Code},
		Timeline:                  StateFact{State: string(b.Timeline.State), Code: b.Timeline.Reason.Code},
		Coverage:                  StateFact{State: string(b.Coverage.State), Code: b.Coverage.Reason.Code},
		Retention: RetentionFacts{
			VisibleBackups:            b.Retention.VisibleBackups,
			StructurallyUsableBackups: b.Retention.StructurallyUsableBackups,
			OldestCompletionAt:        b.Retention.OldestCompletionAt,
			NewestCompletionAt:        b.Retention.NewestCompletionAt,
			MinimumConfigured:         b.Retention.MinimumConfigured,
			MinimumRedundancy:         b.Retention.MinimumRedundancy,
			Result:                    StateFact{State: string(b.Retention.State), Code: b.Retention.Reason.Code},
		},
		LatestArchiveReceiptAt: b.LatestArchiveReceiptAt,
		RangesTruncated:        b.RangesTruncated,
		DiagnosticsTruncated:   b.DiagnosticsTruncated,
	}
	if b.BackupStates != nil {
		facts.BackupStates = &StateCountFacts{
			Healthy:   b.BackupStates.Healthy,
			Warning:   b.BackupStates.Warning,
			Unhealthy: b.BackupStates.Unhealthy,
			Unknown:   b.BackupStates.Unknown,
		}
	}
	if b.WALCounts != nil {
		facts.WALCounts = &WALCountFacts{
			Segment:       b.WALCounts.Segment,
			Partial:       b.WALCounts.Partial,
			History:       b.WALCounts.History,
			BackupHistory: b.WALCounts.BackupHistory,
			Unknown:       b.WALCounts.Unknown,
			Duplicate:     b.WALCounts.Duplicate,
		}
	}
	return facts
}
