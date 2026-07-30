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

package application

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/kube"
)

// staticFetcher serves one fixed report or error, with an empty
// backup collection.
type staticFetcher struct {
	report evidence.Report
	err    error
}

func (f staticFetcher) FetchSnapshot(context.Context) (evidence.Report, error) {
	return f.report, f.err
}

func (f staticFetcher) FetchBackups(context.Context, uint64, uint64) ([]evidence.RepoBackup, bool, error) {
	return nil, false, nil
}

// startWithFetcher assembles and serves the application with the given
// evidence fetcher wired.
func startWithFetcher(t *testing.T, fetcher evidence.Fetcher) (string, func()) {
	t.Helper()
	deps := Deps{Prober: kube.UnavailableProber{}, EvidenceFetcher: fetcher}
	app, err := New(testConfig(t), deps, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx) }()
	stop := func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}
	return "http://" + app.Addr(), stop
}

// waitForBody polls the page until the fragment appears or the bound
// expires, returning the last body.
func waitForBody(t *testing.T, url, fragment string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		var err error
		_, body, err = httpGet(t, url)
		if err == nil && strings.Contains(body, fragment) {
			return body
		}
		time.Sleep(20 * time.Millisecond)
	}
	return body
}

func TestAssemblyWiresEvidencePollerIntoThePage(t *testing.T) {
	t.Parallel()
	report := evidence.Report{
		Fingerprint: "sha256:" + strings.Repeat("ab", 32),
		ScopeKind:   "barman-server", ScopeName: "orders",
		Provider: "s3", Format: "barman-cloud",
		Completeness: "complete",
		Overall:      evidence.StateFact{State: "healthy", Code: "evidence-complete"},
		DetailsType:  "barman-cloud/v1alpha1",
	}
	base, stop := startWithFetcher(t, staticFetcher{report: report})
	defer stop()

	body := waitForBody(t, base+"/", "Repository evidence")
	if !strings.Contains(body, "sha256:"+strings.Repeat("ab", 32)) {
		t.Error("published report did not reach the page")
	}

	status, _, err := httpGet(t, base+"/readyz")
	if err != nil {
		t.Fatalf("GET readyz: %v", err)
	}
	if status != http.StatusServiceUnavailable {
		t.Errorf("readyz = %d, want the prober's own answer regardless of evidence", status)
	}
}

func TestAssemblyEvidenceFailureRendersUnknownPanel(t *testing.T) {
	t.Parallel()
	base, stop := startWithFetcher(t, staticFetcher{err: errors.New("dial refused")})
	defer stop()

	body := waitForBody(t, base+"/", "no successful sidecar contact yet")
	if !strings.Contains(body, "Repository evidence") {
		t.Error("failing sidecar did not render the unknown panel")
	}
}

func TestAssemblyWithoutFetcherHasNoRepositorySection(t *testing.T) {
	t.Parallel()
	logs := &bytes.Buffer{}
	base, stop := start(t, logs)
	defer stop()

	_, body, err := httpGet(t, base+"/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	if strings.Contains(body, "Repository evidence") {
		t.Error("disabled consumer renders a repository section")
	}
}
