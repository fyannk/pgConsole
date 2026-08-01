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
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

// testNow is the fixed rendering instant of all handler tests.
var testNow = time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

// staticSnapshots serves fixed snapshots for every section.
type staticSnapshots struct {
	snap       observe.Snapshot
	ok         bool
	pods       observe.PodsSnapshot
	podsOK     bool
	events     observe.EventsSnapshot
	eventsOK   bool
	backups    observe.BackupsSnapshot
	backupsOK  bool
	poolers    observe.PoolersSnapshot
	poolersOK  bool
	quorum     observe.FailoverQuorumSnapshot
	quorumOK   bool
	catalogs   observe.ImageCatalogsSnapshot
	catalogsOK bool
}

func (s staticSnapshots) Current() (observe.Snapshot, bool) {
	return s.snap, s.ok
}

func (s staticSnapshots) CurrentPods() (observe.PodsSnapshot, bool) {
	return s.pods, s.podsOK
}

func (s staticSnapshots) CurrentEvents() (observe.EventsSnapshot, bool) {
	return s.events, s.eventsOK
}

func (s staticSnapshots) CurrentBackups() (observe.BackupsSnapshot, bool) {
	return s.backups, s.backupsOK
}

func (s staticSnapshots) CurrentPoolers() (observe.PoolersSnapshot, bool) {
	return s.poolers, s.poolersOK
}

func (s staticSnapshots) CurrentFailoverQuorum() (observe.FailoverQuorumSnapshot, bool) {
	return s.quorum, s.quorumOK
}

func (s staticSnapshots) CurrentImageCatalogs() (observe.ImageCatalogsSnapshot, bool) {
	return s.catalogs, s.catalogsOK
}

// allSources is the snapshot-supplier bundle of the tests.
type allSources interface {
	SnapshotSource
	PodsSource
	EventsSource
	BackupsSource
	PoolersSource
	FailoverQuorumSource
	ImageCatalogsSource
}

// newTestHandlerFull builds a Handler with explicit log configuration,
// logging into the returned buffer.
func newTestHandlerFull(t *testing.T, snapshots allSources, prober ReadinessProber, links Links, allowLogs bool, tailer LogTailer) (*Handler, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	h, err := New(Config{ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour, AllowLogs: allowLogs, LevelHeader: "X-PgToolBox-Level", Links: links},
		Sources{Cluster: snapshots, Pods: snapshots, Events: snapshots, Backups: snapshots, Poolers: snapshots, FailoverQuorum: snapshots, ImageCatalogs: snapshots},
		prober, tailer, Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
		nil, nil, func() time.Time { return testNow }, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h, logs
}

// newLeveledHandler builds a Handler with the trusted level header
// configured, so the proxy-asserted level drives display and gating.
func newLeveledHandler(t *testing.T, snapshots allSources) (*Handler, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	h, err := New(Config{ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour, AllowLogs: true, LevelHeader: "X-PgToolBox-Level"},
		Sources{Cluster: snapshots, Pods: snapshots, Events: snapshots, Backups: snapshots, Poolers: snapshots, FailoverQuorum: snapshots, ImageCatalogs: snapshots},
		kube.FakeProber{}, fakeTailer{},
		Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
		nil, nil, func() time.Time { return testNow }, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h, logs
}

// newTestHandler builds a Handler with logs enabled and the default
// tailer.
func newTestHandler(t *testing.T, snapshots allSources, prober ReadinessProber, links Links) (*Handler, *bytes.Buffer) {
	t.Helper()
	return newTestHandlerFull(t, snapshots, prober, links, true, fakeTailer{})
}

// get performs a request against the full route set.
func get(t *testing.T, h *Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	return rec
}

// intp returns a pointer to v.
func intp(v int) *int { return &v }

// healthyFacts is a fully reported cluster.
func healthyFacts() observe.ClusterFacts {
	return observe.ClusterFacts{
		Present:              true,
		Phase:                "Cluster in healthy state",
		CurrentPrimary:       "orders-1",
		TargetPrimary:        "orders-1",
		DesiredInstances:     intp(3),
		ReadyInstances:       intp(3),
		TimelineID:           intp(4),
		Image:                "ghcr.io/cloudnative-pg/postgresql:16.4",
		PostgresMajorVersion: intp(16),
		Conditions: []observe.Condition{
			{Type: "Ready", Status: "True", Reason: "ClusterIsReady", Message: "Cluster is Ready"},
		},
	}
}

// requiredHeaders are set on every response by the middleware.
var requiredHeaders = map[string]string{
	"Cache-Control":           "no-store",
	"X-Content-Type-Options":  "nosniff",
	"X-Frame-Options":         "DENY",
	"Referrer-Policy":         "no-referrer",
	"Content-Security-Policy": "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
}

func TestHandlerSecurityHeadersOnEveryResponse(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, EmptySnapshots{}, kube.UnavailableProber{}, Links{})
	for _, path := range []string{"/", "/healthz", "/readyz", "/static/app.css", "/nonexistent"} {
		rec := get(t, h, http.MethodGet, path)
		for name, want := range requiredHeaders {
			if got := rec.Header().Get(name); got != want {
				t.Errorf("%s: header %s = %q, want %q", path, name, got, want)
			}
		}
	}
}

func TestHandlerHealthzProvesLivenessOnly(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, EmptySnapshots{}, kube.UnavailableProber{}, Links{})
	rec := get(t, h, http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}
	if body := rec.Body.String(); body != "ok\n" {
		t.Fatalf("healthz body = %q, want constant", body)
	}
}

func TestHandlerReadyzNotReadyWithoutAPIProbe(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, EmptySnapshots{}, kube.UnavailableProber{}, Links{})
	rec := get(t, h, http.MethodGet, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want 503", rec.Code)
	}
	if body := rec.Body.String(); body != "not ready\n" {
		t.Fatalf("readyz body = %q, want constant", body)
	}
}

func TestHandlerReadyzReadyWithHealthyProbe(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, EmptySnapshots{}, kube.FakeProber{}, Links{})
	rec := get(t, h, http.MethodGet, "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz status = %d, want 200", rec.Code)
	}
}

// TestHandlerReadyzRevealsNoProbeDetail proves the readiness body stays
// constant and the log carries only a category, even when the probe
// error embeds hostile detail.
func TestHandlerReadyzRevealsNoProbeDetail(t *testing.T) {
	t.Parallel()
	const canary = "sekret-canary"
	hostile := redact.NewError("probe "+canary, redact.CategoryTimeout, io.ErrUnexpectedEOF)
	h, logs := newTestHandler(t, EmptySnapshots{}, kube.FakeProber{Err: hostile}, Links{})
	rec := get(t, h, http.MethodGet, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), canary) {
		t.Error("readyz body leaks probe detail")
	}
	if strings.Contains(logs.String(), canary) {
		t.Error("readyz log leaks probe detail")
	}
	if !strings.Contains(logs.String(), string(redact.CategoryTimeout)) {
		t.Error("readyz log misses the error category")
	}
}

func TestHandlerIndexRendersUnknownShellWithoutSnapshot(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, EmptySnapshots{}, kube.UnavailableProber{}, Links{})
	rec := get(t, h, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"orders", "payments", "unknown: no snapshot", "none"} {
		if !strings.Contains(body, want) {
			t.Errorf("index body misses %q", want)
		}
	}
	if strings.Contains(body, "healthy") {
		t.Error("empty shell must not present the cluster as healthy")
	}
}

func TestHandlerIndexRendersOperatorReportedStatus(t *testing.T) {
	t.Parallel()
	snap := observe.Snapshot{
		Generation: 7,
		ObservedAt: testNow.Add(-3 * time.Second),
		Cluster:    healthyFacts(),
	}
	h, _ := newTestHandler(t, staticSnapshots{snap: snap, ok: true}, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	for _, want := range []string{
		"Cluster in healthy state",
		"orders-1",
		"3/3 ready",
		"ClusterIsReady",
		"ghcr.io/cloudnative-pg/postgresql:16.4",
		"current — age 3s (generation 7)",
		OriginOperator.Label(),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("index body misses %q", want)
		}
	}
}

// TestHandlerIndexStaleSnapshotIsVisible proves a broken watch renders
// stale, never as a healthy current cluster.
func TestHandlerIndexStaleSnapshotIsVisible(t *testing.T) {
	t.Parallel()
	snap := observe.Snapshot{
		Generation: 9,
		ObservedAt: testNow.Add(-150 * time.Second),
		Stale:      true,
		Cluster:    healthyFacts(),
	}
	h, _ := newTestHandler(t, staticSnapshots{snap: snap, ok: true}, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	if !strings.Contains(body, "stale — age 2m30s (generation 9)") {
		t.Errorf("stale snapshot not labeled: %s", body)
	}
	if strings.Contains(body, "current —") {
		t.Error("stale snapshot rendered as current")
	}
}

// TestHandlerIndexAbsentClusterIsExplicit proves a deleted cluster
// renders explicit absence, not an error and not an empty healthy page.
func TestHandlerIndexAbsentClusterIsExplicit(t *testing.T) {
	t.Parallel()
	snap := observe.Snapshot{Generation: 2, ObservedAt: testNow, Cluster: observe.ClusterFacts{Present: false}}
	h, _ := newTestHandler(t, staticSnapshots{snap: snap, ok: true}, kube.FakeProber{}, Links{})
	rec := get(t, h, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "absent: cluster not found in the namespace") {
		t.Error("absence not rendered explicitly")
	}
}

// TestHandlerIndexUnreportedFactsRenderUnknown proves fields absent in
// older CloudNativePG versions render unknown, never invented values.
func TestHandlerIndexUnreportedFactsRenderUnknown(t *testing.T) {
	t.Parallel()
	snap := observe.Snapshot{
		Generation: 1,
		ObservedAt: testNow,
		Cluster:    observe.ClusterFacts{Present: true, Phase: "Setting up primary"},
	}
	h, _ := newTestHandler(t, staticSnapshots{snap: snap, ok: true}, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	if got := strings.Count(body, ">unknown<"); got < 5 {
		t.Errorf("unreported facts rendered as unknown %d times, want at least 5", got)
	}
}

func TestHandlerLinkOutsRenderOnlyWhenConfigured(t *testing.T) {
	t.Parallel()
	links := Links{ObjectStoreViewer: "https://viewer.example.com/repo", Monitoring: ""}
	h, _ := newTestHandler(t, EmptySnapshots{}, kube.FakeProber{}, links)
	body := get(t, h, http.MethodGet, "/").Body.String()
	if !strings.Contains(body, `href="https://viewer.example.com/repo"`) {
		t.Error("configured link-out missing")
	}
	if !strings.Contains(body, `rel="noopener noreferrer"`) {
		t.Error("link-out misses the rel attributes")
	}
	if strings.Contains(body, "Monitoring") {
		t.Error("unconfigured link-out rendered")
	}
	if strings.Contains(body, "pgAdmin") {
		t.Error("unconfigured link-out rendered")
	}
}

func TestHandlerRejectsNonGET(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, EmptySnapshots{}, kube.UnavailableProber{}, Links{})
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := get(t, h, method, "/")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s / status = %d, want 405", method, rec.Code)
		}
	}
}

func TestHandlerUnknownRouteIs404(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, EmptySnapshots{}, kube.UnavailableProber{}, Links{})
	rec := get(t, h, http.MethodGet, "/operations")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown route status = %d, want 404", rec.Code)
	}
}

// TestHandlerTemplateEscapesHostileValues proves every rendered field
// passes through the HTML escaper, using a hostile view model injected
// below the validation layer.
func TestHandlerTemplateEscapesHostileValues(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, EmptySnapshots{}, kube.UnavailableProber{}, Links{})
	hostile := Page{
		ClusterName:   `<script>alert(1)</script>`,
		Namespace:     `"><img src=x onerror=alert(1)>`,
		SnapshotState: `<b>none</b>`,
		Cluster: &ClusterView{
			Origin: Origin(`<script>o</script>`),
			Phase:  `<style>*{display:none}</style>`,
			Conditions: []ConditionView{{
				Type: "Ready", Status: "True",
				Reason:  `<iframe src="https://evil.example"></iframe>`,
				Message: `</td><script>d()</script>`,
			}},
		},
		Panels: []Panel{{
			Title:  `<iframe src="https://evil.example"></iframe>`,
			Origin: Origin(`x`),
			State:  `<b>s</b>`,
			Detail: `</p><script>d()</script>`,
		}},
		Links: []Link{{Label: `<script>l</script>`, URL: `https://example.com/"><script>u</script>`}},
	}
	var out bytes.Buffer
	if err := h.tpl.ExecuteTemplate(&out, "page.html.tmpl", hostile); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	body := out.String()
	// The page embeds exactly two scripts of its own: the enhancement
	// layer and the vendored Alpine CSP build. Both are same-origin src
	// references with no inline body, and both must be present. Removing
	// them before the injection scan keeps that scan exact — a third
	// script tag from a hostile value still fails the check below.
	for _, own := range []string{
		`<script src="/static/console.js" defer></script>`,
		`<script src="/static/alpine.csp.js" defer></script>`,
	} {
		if !strings.Contains(body, own) {
			t.Fatalf("page is missing its own script tag %q", own)
		}
		body = strings.ReplaceAll(body, own, "")
	}
	for _, forbidden := range []string{"<script", "<iframe", "<style", "<img", "<b>"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("rendered output contains unescaped %q", forbidden)
		}
	}
}

// boolp returns a pointer to v.
func boolp(v bool) *bool { return &v }

// podsSnapshot builds a pods snapshot at the fixed test instant.
func podsSnapshot(stale bool, pods ...observe.PodFacts) observe.PodsSnapshot {
	return observe.PodsSnapshot{
		Generation: 5,
		ObservedAt: testNow.Add(-2 * time.Second),
		Stale:      stale,
		Pods:       pods,
	}
}

func memberPod(name, role string) observe.PodFacts {
	return observe.PodFacts{
		Name: name, UID: "u-" + name, Role: role, Phase: "Running",
		Ready: boolp(true), Restarts: intp(0), Node: "node-a",
		Image: "ghcr.io/cloudnative-pg/postgresql:16.4",
	}
}

func TestHandlerIndexRendersPodTable(t *testing.T) {
	t.Parallel()
	src := staticSnapshots{
		snap:   observe.Snapshot{Generation: 3, ObservedAt: testNow, Cluster: healthyFacts()},
		ok:     true,
		pods:   podsSnapshot(false, memberPod("orders-1", "primary"), memberPod("orders-2", "replica")),
		podsOK: true,
	}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	for _, want := range []string{
		"Instance pods",
		"orders-2",
		"replica",
		"node-a",
		"snapshot: current — age 2s (generation 5)",
		OriginKubernetes.Label(),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pod table misses %q", want)
		}
	}
	if strings.Contains(body, "disagreement") {
		t.Error("agreeing primary rendered as a disagreement")
	}
}

// TestHandlerIndexRendersPrimaryDisagreement proves both claims render
// with their origins when the operator and the pod labels conflict.
func TestHandlerIndexRendersPrimaryDisagreement(t *testing.T) {
	t.Parallel()
	src := staticSnapshots{
		snap:   observe.Snapshot{Generation: 3, ObservedAt: testNow, Cluster: healthyFacts()},
		ok:     true,
		pods:   podsSnapshot(false, memberPod("orders-1", "replica"), memberPod("orders-2", "primary")),
		podsOK: true,
	}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	for _, want := range []string{
		"disagreement on the primary instance",
		"current primary is orders-1",
		OriginOperator.Label(),
		"primary role label on orders-2",
		OriginKubernetes.Label(),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("disagreement misses %q", want)
		}
	}
}

func TestHandlerIndexPodSectionOwnStaleness(t *testing.T) {
	t.Parallel()
	src := staticSnapshots{
		snap:   observe.Snapshot{Generation: 3, ObservedAt: testNow, Cluster: healthyFacts()},
		ok:     true,
		pods:   podsSnapshot(true, memberPod("orders-1", "primary")),
		podsOK: true,
	}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	if !strings.Contains(body, "snapshot: stale — age 2s (generation 5)") {
		t.Error("stale pod snapshot not labeled in its own section")
	}
	if !strings.Contains(body, "current — age 0s (generation 3)") {
		t.Error("cluster snapshot line lost its independent freshness")
	}
}

func TestHandlerIndexPodUnknownsAndTruncation(t *testing.T) {
	t.Parallel()
	bare := observe.PodFacts{Name: "orders-9", UID: "u9"}
	snap := podsSnapshot(false, bare)
	snap.Truncated = true
	src := staticSnapshots{pods: snap, podsOK: true}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	if got := strings.Count(body, "<td>unknown</td>"); got < 5 {
		t.Errorf("unreported pod facts rendered unknown %d times, want at least 5", got)
	}
	if !strings.Contains(body, "truncated: more members matched than the display bound") {
		t.Error("truncation not visible")
	}
}

// fakeTailer is the default test tailer: it records the requested pod
// and returns scripted outcomes.
type fakeTailer struct {
	tail observe.LogTail
	err  error
}

func (f fakeTailer) TailLogs(_ context.Context, _ string) (observe.LogTail, error) {
	if f.err != nil {
		return observe.LogTail{}, f.err
	}
	if f.tail.LineLimit == 0 {
		f.tail = observe.LogTail{Content: "log line\n", LineLimit: 200, ByteLimit: 1048576}
	}
	return f.tail, nil
}

// eventFacts builds one candidate event at an age before the fixed
// test instant.
func eventFacts(name, kind, object, reason string, age time.Duration) observe.EventFacts {
	return observe.EventFacts{
		Name: name, UID: "u-" + name, Kind: kind, Object: object,
		Type: "Warning", Reason: reason, Message: "m", Count: 2,
		LastSeen: testNow.Add(-age),
	}
}

func TestHandlerIndexRendersEventsWithMembershipFilter(t *testing.T) {
	t.Parallel()
	src := staticSnapshots{
		pods:   podsSnapshot(false, memberPod("orders-1", "primary")),
		podsOK: true,
		events: observe.EventsSnapshot{
			Generation: 4,
			ObservedAt: testNow.Add(-1 * time.Second),
			Events: []observe.EventFacts{
				eventFacts("e1", "Cluster", "orders", "SwitchoverStarted", time.Minute),
				eventFacts("e2", "Pod", "orders-1", "BackOff", 2*time.Minute),
				// Prefix-matched candidate that is not a verified member:
				// selected at the boundary, refused at rendering.
				eventFacts("e3", "Pod", "orders-api-1", "Scheduled", 3*time.Minute),
			},
		},
		eventsOK: true,
	}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	for _, want := range []string{
		"SwitchoverStarted", "Cluster/orders",
		"BackOff", "Pod/orders-1",
		"window 1h0m0s",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("events section misses %q", want)
		}
	}
	if strings.Contains(body, "orders-api-1") {
		t.Error("non-member pod event rendered")
	}
}

// TestHandlerIndexEventsWindowAppliesAtRender proves an event ages out
// between publication and rendering.
func TestHandlerIndexEventsWindowAppliesAtRender(t *testing.T) {
	t.Parallel()
	src := staticSnapshots{
		podsOK: true,
		events: observe.EventsSnapshot{
			Generation: 2,
			ObservedAt: testNow.Add(-30 * time.Minute),
			Events: []observe.EventFacts{
				eventFacts("young", "Cluster", "orders", "Fresh", 10*time.Minute),
				eventFacts("aged", "Cluster", "orders", "Expired", 2*time.Hour),
			},
		},
		eventsOK: true,
	}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	if !strings.Contains(body, "Fresh") {
		t.Error("in-window event missing")
	}
	if strings.Contains(body, "Expired") {
		t.Error("out-of-window event rendered")
	}
}

// TestHandlerIndexPodEventsWithheldWithoutMembership proves pod events
// are withheld, visibly, when no pod snapshot can verify membership.
func TestHandlerIndexPodEventsWithheldWithoutMembership(t *testing.T) {
	t.Parallel()
	src := staticSnapshots{
		events: observe.EventsSnapshot{
			Generation: 1,
			ObservedAt: testNow,
			Events: []observe.EventFacts{
				eventFacts("p", "Pod", "orders-1", "BackOff", time.Minute),
			},
		},
		eventsOK: true,
	}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	if strings.Contains(body, "BackOff") {
		t.Error("pod event rendered without membership verification")
	}
	if !strings.Contains(body, "pod events withheld: membership unknown") {
		t.Error("withholding not visible")
	}
}

func TestHandlerIndexEventsTruncationVisible(t *testing.T) {
	t.Parallel()
	src := staticSnapshots{
		podsOK: true,
		events: observe.EventsSnapshot{
			Generation: 1, ObservedAt: testNow, Truncated: true,
			Events: []observe.EventFacts{eventFacts("e", "Cluster", "orders", "R", time.Minute)},
		},
		eventsOK: true,
	}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	if !strings.Contains(body, "truncated: more events matched than the display bound") {
		t.Error("event truncation not visible")
	}
}

func TestHandlerIndexRendersAttributedBackupsAndEvidencePath(t *testing.T) {
	t.Parallel()
	started := testNow.Add(-2 * time.Hour)
	stopped := testNow.Add(-90 * time.Minute)
	src := staticSnapshots{
		backups: observe.BackupsSnapshot{
			Generation: 6, ObservedAt: testNow.Add(-4 * time.Second),
			Backups: []observe.BackupFacts{
				{Name: "plugin-backup", Phase: "completed", Method: "plugin", CreatedAt: started, StartedAt: &started, StoppedAt: &stopped},
				{Name: "snapshot-backup", Phase: "running", Method: "volumeSnapshot", CreatedAt: testNow.Add(-time.Hour)},
			},
			ScheduledBackups: []observe.ScheduledBackupFacts{{Name: "daily", Method: "plugin", Schedule: "0 0 2 * * *", Suspended: boolp(false)}},
			ObjectStore:      observe.ObjectStoreReference{Name: "orders-store", State: observe.ObjectStorePresent},
		},
		backupsOK: true,
	}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{ObjectStoreViewer: "https://viewer.example.com/orders"})
	body := get(t, h, http.MethodGet, "/").Body.String()
	for _, want := range []string{
		"plugin-backup", "completed — operator-reported claim", "Last completed backup age: 1h30m",
		"snapshot-backup", "volumeSnapshot", "unknown (not collected)",
		"daily", "0 0 2 * * *", "Inspect repository structure in ObjectStoreViewer",
		"orders-store", "object metadata observed", OriginOperator.Label(), OriginKubernetes.Label(),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("backup sections miss %q", want)
		}
	}
	for _, prohibited := range []string{"verified", "restorable", "restore guaranteed", "exact pitr window"} {
		if strings.Contains(strings.ToLower(body), prohibited) {
			t.Errorf("backup page contains prohibited claim %q", prohibited)
		}
	}
}

func TestHandlerIndexObjectStoreUnknownDoesNotDegradeBackups(t *testing.T) {
	t.Parallel()
	src := staticSnapshots{
		backups: observe.BackupsSnapshot{
			Generation: 2, ObservedAt: testNow,
			Backups:     []observe.BackupFacts{{Name: "b1", Phase: "running", Method: "plugin"}},
			ObjectStore: observe.ObjectStoreReference{Name: "orders-store", State: observe.ObjectStoreUnknown},
		},
		backupsOK: true,
	}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	if !strings.Contains(body, "b1") || strings.Contains(body, "Backups</h2>\n      <p class=\"state\">unknown") {
		t.Fatal("ObjectStore denial degraded the Backup section")
	}
	if !strings.Contains(body, "unknown (permission, CRD, cluster, or object unavailable)") {
		t.Fatal("ObjectStore lookup failure is not visible as unknown")
	}
}

func TestHandlerIndexBackupSectionOwnStaleness(t *testing.T) {
	t.Parallel()
	src := staticSnapshots{
		snap: observe.Snapshot{Generation: 3, ObservedAt: testNow, Cluster: healthyFacts()}, ok: true,
		backups: observe.BackupsSnapshot{
			Generation: 8, ObservedAt: testNow.Add(-2 * time.Minute), Stale: true,
			Backups: []observe.BackupFacts{{Name: "last-good", Phase: "completed", Method: "plugin"}},
		},
		backupsOK: true,
	}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	if !strings.Contains(body, "snapshot: stale — age 2m00s (generation 8)") || !strings.Contains(body, "last-good") {
		t.Fatal("last-good Backup catalog is not visibly stale")
	}
	if !strings.Contains(body, "current — age 0s (generation 3)") {
		t.Fatal("backup staleness incorrectly degraded the cluster snapshot")
	}
}

func TestHandlerIndexBackupTruncationVisible(t *testing.T) {
	t.Parallel()
	src := staticSnapshots{backups: observe.BackupsSnapshot{
		Generation: 1, ObservedAt: testNow, BackupsTruncated: true, SchedulesTruncated: true,
	}, backupsOK: true}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/").Body.String()
	if !strings.Contains(body, "Backup catalog reached its safety or display bound") || !strings.Contains(body, "ScheduledBackup catalog reached its safety or display bound") {
		t.Fatal("backup catalog truncation is not visible")
	}
}

// logsGet issues a poweruser-authorized GET, the minimum level the log
// route admits.
func logsGet(t *testing.T, h *Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return getWithHeaders(t, h, path, powerUser)
}

func TestHandlerLogsRendersBoundedTail(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, staticSnapshots{}, kube.FakeProber{}, Links{})
	rec := logsGet(t, h, "/logs/orders-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("logs status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"log line",
		"last 200 lines, at most 1048576 bytes",
		"fetched on demand, never stored",
		OriginKubernetes.Label(),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("logs page misses %q", want)
		}
	}
}

// TestHandlerLogsEscapesHostileContent proves log content — which can
// carry query text and anything a client wrote — renders as text only.
func TestHandlerLogsEscapesHostileContent(t *testing.T) {
	t.Parallel()
	tailer := fakeTailer{tail: observe.LogTail{
		Content:   `ERROR: syntax error at "<script>alert(1)</script>"`,
		LineLimit: 200, ByteLimit: 4096,
	}}
	h, _ := newTestHandlerFull(t, staticSnapshots{}, kube.FakeProber{}, Links{}, true, tailer)
	body := logsGet(t, h, "/logs/orders-1").Body.String()
	if strings.Contains(body, "<script>") {
		t.Fatal("log content rendered unescaped")
	}
	if !strings.Contains(body, "syntax error") {
		t.Fatal("log content missing")
	}
}

func TestHandlerLogsTruncationVisible(t *testing.T) {
	t.Parallel()
	tailer := fakeTailer{tail: observe.LogTail{Content: "x", TruncatedByBytes: true, LineLimit: 200, ByteLimit: 4096}}
	h, _ := newTestHandlerFull(t, staticSnapshots{}, kube.FakeProber{}, Links{}, true, tailer)
	body := logsGet(t, h, "/logs/orders-1").Body.String()
	if !strings.Contains(body, "truncated: the byte ceiling cut this tail") {
		t.Fatal("byte truncation not visible")
	}
}

func TestHandlerLogsRefusalsAndRedaction(t *testing.T) {
	t.Parallel()
	const canary = "sekret-canary"
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantState  string
	}{
		{"non-member or absent pod", redact.NewError("log tail", redact.CategoryNotFound, nil), http.StatusNotFound, "no such member pod"},
		{"forbidden", redact.NewError("log tail", redact.CategoryForbidden, nil), http.StatusServiceUnavailable, "not granted: pods/log"},
		{"transport failure", redact.NewError("log tail "+canary, redact.CategoryTimeout, io.ErrUnexpectedEOF), http.StatusServiceUnavailable, "unavailable: timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h, logs := newTestHandlerFull(t, staticSnapshots{}, kube.FakeProber{}, Links{}, true, fakeTailer{err: tc.err})
			rec := logsGet(t, h, "/logs/orders-1")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tc.wantState) {
				t.Errorf("body misses %q", tc.wantState)
			}
			if strings.Contains(rec.Body.String(), canary) || strings.Contains(logs.String(), canary) {
				t.Error("hostile error detail leaked")
			}
		})
	}
}

// TestHandlerLogsDisabledModeHasNoRouteAndNoLink proves disabled mode
// removes the capability entirely: 404 route, no affordance, no column.
func TestHandlerLogsDisabledModeHasNoRouteAndNoLink(t *testing.T) {
	t.Parallel()
	src := staticSnapshots{
		pods:   podsSnapshot(false, memberPod("orders-1", "primary")),
		podsOK: true,
	}
	h, _ := newTestHandlerFull(t, src, kube.FakeProber{}, Links{}, false, nil)
	if rec := get(t, h, http.MethodGet, "/logs/orders-1"); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled logs route status = %d, want 404", rec.Code)
	}
	body := get(t, h, http.MethodGet, "/").Body.String()
	if strings.Contains(body, "/logs/") || strings.Contains(body, ">tail<") || strings.Contains(body, "<th>Logs</th>") {
		t.Error("disabled mode still renders a log affordance")
	}
}

func TestHandlerLogsLinkRenderedForMembers(t *testing.T) {
	t.Parallel()
	src := staticSnapshots{
		pods:   podsSnapshot(false, memberPod("orders-1", "primary")),
		podsOK: true,
	}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := getWithHeaders(t, h, "/", powerUser).Body.String()
	if !strings.Contains(body, `<a href="/logs/orders-1">tail</a>`) {
		t.Fatal("member pod misses its log link")
	}
}

// TestHandlerLogsAffordanceHiddenBelowPowerUser proves a viewer sees no
// log column or link — the affordance follows the route's poweruser gate.
func TestHandlerLogsAffordanceHiddenBelowPowerUser(t *testing.T) {
	t.Parallel()
	src := staticSnapshots{
		pods:   podsSnapshot(false, memberPod("orders-1", "primary")),
		podsOK: true,
	}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := getWithHeaders(t, h, "/", map[string]string{"X-Forwarded-User": "alice", "X-PgToolBox-Level": "view"}).Body.String()
	if strings.Contains(body, "/logs/") || strings.Contains(body, ">tail<") || strings.Contains(body, "<th>Logs</th>") {
		t.Error("viewer sees a log affordance above their level")
	}
}

func TestHandlerLogsRejectsMalformedPodNames(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, staticSnapshots{}, kube.FakeProber{}, Links{})
	for _, pod := range []string{"UPPER", "-lead", "trail-", "a_b", strings.Repeat("a", 300)} {
		rec := logsGet(t, h, "/logs/"+pod)
		if rec.Code != http.StatusNotFound {
			t.Errorf("pod %q status = %d, want 404", pod, rec.Code)
		}
	}
}

// getWithHeaders performs a request carrying the given headers.
func getWithHeaders(t *testing.T, h *Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	return rec
}

// TestHandlerBaselineOpenRegardlessOfLevel proves the read-only baseline
// renders for every level the proxy asserts — and even with no level and
// no identity at all — because reaching the console means the proxy
// already authenticated the request.
func TestHandlerBaselineOpenRegardlessOfLevel(t *testing.T) {
	t.Parallel()
	h, _ := newLeveledHandler(t, staticSnapshots{})
	cases := []map[string]string{
		{"X-Forwarded-User": "alice", "X-PgToolBox-Level": "view"},
		{"X-Forwarded-User": "alice", "X-PgToolBox-Level": "dba"},
		{"X-Forwarded-User": "alice", "X-PgToolBox-Level": "bogus"},
		{"X-Forwarded-User": "alice"},
		{},
	}
	for _, headers := range cases {
		rec := getWithHeaders(t, h, "/", headers)
		if rec.Code != http.StatusOK {
			t.Errorf("baseline status = %d for headers %v, want 200", rec.Code, headers)
		}
	}
}

// TestHandlerIdentityViewShowsProxyAssertedLevel proves the identity line
// carries the forwarded user and the parsed level, labeled proxy-asserted.
func TestHandlerIdentityViewShowsProxyAssertedLevel(t *testing.T) {
	t.Parallel()
	h, _ := newLeveledHandler(t, staticSnapshots{})
	rec := getWithHeaders(t, h, "/", map[string]string{"X-Forwarded-User": "alice", "X-PgToolBox-Level": "dba"})
	if !strings.Contains(rec.Body.String(), "alice — dba (proxy-asserted)") {
		t.Errorf("identity line missing proxy-asserted level; body:\n%s", rec.Body.String())
	}
}

// TestHandlerIdentityViewUnknownLevelRendersNone proves an unrecognized
// level renders as none rather than being echoed or elevated.
func TestHandlerIdentityViewUnknownLevelRendersNone(t *testing.T) {
	t.Parallel()
	h, _ := newLeveledHandler(t, staticSnapshots{})
	rec := getWithHeaders(t, h, "/", map[string]string{"X-Forwarded-User": "alice", "X-PgToolBox-Level": "root"})
	if !strings.Contains(rec.Body.String(), "alice — none (proxy-asserted)") {
		t.Errorf("unknown level not rendered as none; body:\n%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "root") {
		t.Error("unrecognized level value echoed into the page")
	}
}

// TestHandlerLogsRequirePowerUserLevel proves the log route sits above
// the baseline: a viewer and an unknown level are denied, poweruser and
// dba are admitted.
func TestHandlerLogsRequirePowerUserLevel(t *testing.T) {
	t.Parallel()
	h, _ := newLeveledHandler(t, staticSnapshots{})
	for _, level := range []string{"view", "bogus", ""} {
		headers := map[string]string{"X-Forwarded-User": "alice"}
		if level != "" {
			headers["X-PgToolBox-Level"] = level
		}
		if rec := getWithHeaders(t, h, "/logs/orders-1", headers); rec.Code != http.StatusForbidden {
			t.Errorf("logs at level %q = %d, want 403", level, rec.Code)
		}
	}
	for _, level := range []string{"poweruser", "dba"} {
		rec := getWithHeaders(t, h, "/logs/orders-1", map[string]string{"X-Forwarded-User": "alice", "X-PgToolBox-Level": level})
		if rec.Code != http.StatusOK {
			t.Errorf("logs at level %q = %d, want 200", level, rec.Code)
		}
	}
}

func TestHandlerProbesAndAssetsOpen(t *testing.T) {
	t.Parallel()
	h, _ := newLeveledHandler(t, staticSnapshots{})
	for _, path := range []string{"/healthz", "/readyz", "/static/app.css"} {
		rec := get(t, h, http.MethodGet, path)
		if rec.Code == http.StatusForbidden {
			t.Errorf("%s gated; probes and assets must not be", path)
		}
	}
}

// TestHandlerBoundsConditionMessages proves display-side message
// bounding on top of the boundary-side cap.
func TestHandlerBoundsConditionMessages(t *testing.T) {
	t.Parallel()
	facts := healthyFacts()
	facts.Conditions = []observe.Condition{{
		Type: "Ready", Status: "False", Reason: "r",
		Message: strings.Repeat("x", 1024),
	}}
	view := buildClusterView(facts)
	msg := view.Conditions[0].Message
	if len([]rune(msg)) > maxDisplayMessage+1 {
		t.Fatalf("display message length %d exceeds bound", len([]rune(msg)))
	}
	if !strings.HasSuffix(msg, "…") {
		t.Error("truncated message misses the ellipsis")
	}
}

// TestHandlerPoolersRendersAttributedRows proves the poolers screen
// reports what the operator says of each pooler, attributed, and that
// the pool mode and endpoint reach the page.
func TestHandlerPoolersRendersAttributedRows(t *testing.T) {
	t.Parallel()
	snapshots := staticSnapshots{
		poolers: observe.PoolersSnapshot{
			Generation: 3, ObservedAt: testNow.Add(-2 * time.Second),
			Poolers: []observe.PoolerFacts{{
				Name: "orders-rw", UID: "u1", Type: "rw", PoolMode: "transaction",
				ReadyInstances: 2, Phase: "active", Image: "pgbouncer:1.24",
			}},
		},
		poolersOK: true,
	}
	h, _ := newTestHandler(t, snapshots, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/poolers").Body.String()

	for _, want := range []string{"orders-rw", "rw — the write endpoint", "transaction", "active", "pgbouncer:1.24", "operator-reported"} {
		if !strings.Contains(body, want) {
			t.Errorf("poolers page misses %q", want)
		}
	}
}

// TestHandlerPoolersDistinguishesNotObservedFromNone proves the two
// claims are worded differently. An empty result means the operator
// reports no poolers; an absent snapshot means this build makes no
// claim at all, and rendering the first for the second would invent a
// fact.
func TestHandlerPoolersDistinguishesNotObservedFromNone(t *testing.T) {
	t.Parallel()
	none := staticSnapshots{poolers: observe.PoolersSnapshot{Generation: 1, ObservedAt: testNow}, poolersOK: true}
	h, _ := newTestHandler(t, none, kube.FakeProber{}, Links{})
	if body := get(t, h, http.MethodGet, "/poolers").Body.String(); !strings.Contains(body, "No poolers") {
		t.Error("an observed-empty pooler set does not say the operator reports none")
	}

	h, _ = newTestHandler(t, staticSnapshots{}, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/poolers").Body.String()
	if !strings.Contains(body, "No pooler snapshot yet") {
		t.Error("an absent pooler snapshot does not say the build makes no claim")
	}
	if strings.Contains(body, "No poolers") {
		t.Error("an absent snapshot rendered as an observed-empty set")
	}
}

// TestClusterStatusRendersTheFailoverQuorum proves the quorum panel
// distinguishes a cluster running one from a cluster that is not. "Not
// configured" is an observation of absence; a missing snapshot is the
// absence of an observation, and they must not read the same.
func TestClusterStatusRendersTheFailoverQuorum(t *testing.T) {
	t.Parallel()
	configured := staticSnapshots{
		snap: observe.Snapshot{Generation: 1, ObservedAt: testNow, Cluster: healthyFacts()}, ok: true,
		quorum: observe.FailoverQuorumSnapshot{
			Generation: 2, ObservedAt: testNow,
			Quorum: observe.FailoverQuorumFacts{
				Present: true, Method: "any", Primary: "orders-1",
				StandbyNumber: 1, Standbys: []string{"orders-2", "orders-3"},
			},
		},
		quorumOK: true,
	}
	h, _ := newTestHandler(t, configured, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/cluster/status").Body.String()
	for _, want := range []string{"Failover quorum", "orders-1", "orders-2", "operator-reported"} {
		if !strings.Contains(body, want) {
			t.Errorf("quorum panel misses %q", want)
		}
	}

	absent := configured
	absent.quorum = observe.FailoverQuorumSnapshot{Generation: 2, ObservedAt: testNow}
	h, _ = newTestHandler(t, absent, kube.FakeProber{}, Links{})
	if body := get(t, h, http.MethodGet, "/cluster/status").Body.String(); !strings.Contains(body, "Not configured") {
		t.Error("a cluster without a quorum does not say so")
	}
}

// TestImageCatalogViewResolvesTheClusterReference proves the panel is
// built from two separate observations — the reference on the Cluster
// and the catalog itself — and that each way the pairing can fail is a
// distinct, honest claim rather than a blank.
func TestImageCatalogViewResolvesTheClusterReference(t *testing.T) {
	t.Parallel()
	catalogs := observe.ImageCatalogsSnapshot{
		Generation: 1, ObservedAt: testNow,
		Catalogs: []observe.ImageCatalogFacts{{
			Name: "postgres", UID: "u1",
			Images: []observe.CatalogImageFacts{
				{Major: 16, Image: "pg:16"},
				{Major: 17, Image: "pg:17"},
			},
		}},
	}

	// No reference: the cluster names its image directly.
	if v := buildImageCatalogView(catalogs, nil, testNow); v.Referenced {
		t.Error("a cluster with no catalog reference reported one")
	}

	// Referenced and observed: the drawn major is marked.
	v := buildImageCatalogView(catalogs, &observe.ImageCatalogRef{Kind: "ImageCatalog", Name: "postgres", Major: 17}, testNow)
	if !v.Referenced || !v.Observable || !v.Found {
		t.Fatalf("view = %+v, want a resolved reference", v)
	}
	if len(v.Images) != 2 || !v.Images[1].Current || v.Images[0].Current {
		t.Errorf("images = %+v, want only major 17 marked as drawn", v.Images)
	}

	// Referenced but not present in the namespace.
	v = buildImageCatalogView(catalogs, &observe.ImageCatalogRef{Kind: "ImageCatalog", Name: "missing", Major: 17}, testNow)
	if !v.Referenced || !v.Observable || v.Found {
		t.Errorf("view = %+v, want a reference that resolved to nothing", v)
	}

	// Cluster-scoped: named honestly, content not claimed. This console's
	// authority is one namespace, so it must not report the catalog as
	// missing when it simply cannot look.
	v = buildImageCatalogView(catalogs, &observe.ImageCatalogRef{Kind: "ClusterImageCatalog", Name: "shared", Major: 17}, testNow)
	if !v.Referenced || v.Observable || v.Found || len(v.Images) != 0 {
		t.Errorf("view = %+v, want the reference shown and its content unclaimed", v)
	}
}
