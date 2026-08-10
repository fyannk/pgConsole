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

	"github.com/fyannk/pgConsole/internal/history"
	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/observe"
)

// podDetailSources is a member pod with an event and a retained
// revision, so the detail's timeline has both source kinds.
func podDetailSources() staticSnapshots {
	started := testNow.Add(-26 * time.Hour)
	pod := memberPod("orders-1", "primary")
	pod.IP = "10.42.3.17"
	pod.Started = &started
	ready := true
	restarts := 0
	pod.Containers = []observe.ContainerFacts{
		{Name: "postgres", Image: "pg:16", Ready: &ready, Restarts: &restarts, State: "running"},
		{Name: "plugin-barman-cloud", Image: "barman:0.5", Ready: &ready, Restarts: &restarts, State: "running"},
	}
	return staticSnapshots{
		snap:   observe.Snapshot{Generation: 7, ObservedAt: testNow.Add(-3 * time.Second), Cluster: healthyFacts()},
		ok:     true,
		pods:   podsSnapshot(false, pod),
		podsOK: true,
		events: observe.EventsSnapshot{
			Generation: 4, ObservedAt: testNow.Add(-2 * time.Second),
			Events: []observe.EventFacts{{
				Name: "e1", Kind: "Pod", Object: "orders-1", Type: "Warning",
				Reason: "Unhealthy", Message: "Liveness probe failed", Count: 3,
				LastSeen: testNow.Add(-4 * time.Minute),
			}},
		},
		eventsOK: true,
		// A pooler and one pod it owns, so the pooler roster has a
		// membership proof of its own to be tested against.
		poolers: observe.PoolersSnapshot{
			Generation: 5, ObservedAt: testNow.Add(-2 * time.Second),
			Poolers: []observe.PoolerFacts{{Name: "orders-pool-rw", UID: "p1", Type: "rw", Phase: "active"}},
		},
		poolersOK:    true,
		poolerPods:   podsSnapshot(false, memberPod("orders-pool-rw-abc", "orders-pool-rw")),
		poolerPodsOK: true,
	}
}

func podDetailHistory() fakeHistorySource {
	return fakeHistorySource{
		ok: true,
		snap: history.Snapshot{Generation: 5, Entries: []history.Entry{{
			Seq: 3, Scope: "pods", Kind: "Pod", Namespace: "payments", Name: "orders-1",
			Change: history.ChangeSpec, Actor: history.Actor{Manager: "cloudnative-pg", Operation: "Update"},
			ObservedAt: testNow.Add(-10 * time.Minute), HasManifest: true,
		}}},
		revisions: map[uint64]history.Revision{3: {
			Seq: 3, Kind: "Pod", Name: "orders-1",
			Manifest: []byte(`{"kind":"Pod","metadata":{"name":"orders-1"}}`),
		}},
	}
}

func newPodDetailHandler(t *testing.T, src staticSnapshots, hist HistorySource) *Handler {
	t.Helper()
	h, err := New(
		Config{ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour, AllowLogs: true, LevelHeader: "X-PgToolBox-Level"},
		Sources{
			Cluster: src, Pods: src, Events: src, Backups: src, Poolers: src,
			PoolerPods: src, FailoverQuorum: src, ImageCatalogs: src,
			DatabaseObjects: src, History: hist,
		},
		kube.FakeProber{}, fakeTailer{tail: observe.LogTail{Content: "LOG: ready to accept connections", LineLimit: 200, ByteLimit: 1 << 20}},
		Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
		nil, nil, func() time.Time { return testNow }, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func TestPodDetailRefusesNonMembers(t *testing.T) {
	t.Parallel()
	h := newPodDetailHandler(t, podDetailSources(), nil)
	if got := get(t, h, http.MethodGet, "/cluster/pods/intruder").Code; got != http.StatusNotFound {
		t.Fatalf("non-member status = %d, want 404", got)
	}
	if got := get(t, h, http.MethodGet, "/cluster/pods/UPPER").Code; got != http.StatusNotFound {
		t.Fatalf("malformed name status = %d, want 404", got)
	}
}

func TestPodDetailStatesFactsAndMergedTimeline(t *testing.T) {
	t.Parallel()
	h := newPodDetailHandler(t, podDetailSources(), podDetailHistory())
	body := get(t, h, http.MethodGet, "/cluster/pods/orders-1").Body.String()
	for _, want := range []string{
		"<h1>orders-1</h1>", "10.42.3.17", "node-a",
		"Write endpoint", "orders-rw", // primary-only, operator-reported
		"Unhealthy", "Liveness probe failed (reported 3 times)",
		"definition changed", "cloudnative-pg — Update (self-declared field manager)",
		"times are when this pgConsole process observed them",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pod detail misses %q", want)
		}
	}
	// This screen is itself poweruser-only, so everyone who reaches it
	// may also open a revision: the link is always there. The gate
	// inside stays as defence — it is what would hold if the screen's
	// own tier ever moved — and is exercised through the view level
	// below, which cannot reach the screen at all.
	if !strings.Contains(body, `href="/history/revisions/3"`) {
		t.Error("a reader who reached this screen cannot open its revision")
	}
	for _, level := range []string{"view", "bogus", ""} {
		headers := map[string]string{"X-Forwarded-User": "alice"}
		if level != "" {
			headers["X-PgToolBox-Level"] = level
		}
		if got := getWithHeaders(t, h, "/cluster/pods/orders-1", headers).Code; got != http.StatusForbidden {
			t.Errorf("pod detail at level %q = %d, want 403", level, got)
		}
	}
}

// Every absolute moment carries both halves: the UTC text is the claim
// a reader with no script sees, and the RFC3339 twin beside it is the
// only thing that lets the browser restate that same instant locally. A
// relative age must not carry the marker — rewriting "4m ago" into a
// date changes what the cell says.
func TestPodDetailStampsCarryTheirMachineReadableTwin(t *testing.T) {
	t.Parallel()
	h := newPodDetailHandler(t, podDetailSources(), podDetailHistory())
	body := get(t, h, http.MethodGet, "/cluster/pods/orders-1").Body.String()

	started := strings.Index(body, "<dt>Started</dt>")
	if started < 0 {
		t.Fatal("pod detail states no start time")
	}
	cell := body[started : started+220]
	if !strings.Contains(cell, `<time datetime="`) || !strings.Contains(cell, "data-local") {
		t.Errorf("the start time cannot be restated locally: %q", cell)
	}
	if !strings.Contains(cell, "Z</time>") {
		t.Errorf("the start time is not stated in UTC: %q", cell)
	}
	// The timeline's own stamps too: the age stays a plain age, and the
	// absolute stamp beside it carries the twin.
	if !strings.Contains(body, ` ago</b><time datetime="`) {
		t.Error("a timeline entry states a time the browser cannot restate")
	}
	if strings.Contains(body, ` ago</b><time datetime="" `) {
		t.Error("an entry with no known time still claims one")
	}
}

func TestPodDetailAboveTheGateCarriesLogsAndRawDefinition(t *testing.T) {
	t.Parallel()
	h := newPodDetailHandler(t, podDetailSources(), podDetailHistory())
	body := getWithHeaders(t, h, "/cluster/pods/orders-1", powerUser).Body.String()
	for _, want := range []string{
		`data-tab="pod-logs"`, "LOG: ready to accept connections",
		`data-log-src="/logs/orders-1?raw=1"`,
		"Raw definition", "retained revision 3",
		// The manifest arrives highlighted, and every segment is still
		// escaped inside its span — the colouring must not become a way
		// for a value to emit markup.
		`<span class="j-key">&#34;kind&#34;</span>: <span class="j-str">&#34;Pod&#34;</span>`,
		`href="/history/revisions/3"`,
		// Each container links to its own tail: the instance roster is
		// the one the follower covers, so the addressed route exists.
		`href="/logs/orders-1/plugin-barman-cloud"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("elevated pod detail misses %q", want)
		}
	}
}

func TestRawTailIsGatedPlainText(t *testing.T) {
	t.Parallel()
	h := newPodDetailHandler(t, podDetailSources(), nil)
	for _, headers := range []map[string]string{
		{},
		{"X-Forwarded-User": "alice"},
		{"X-Forwarded-User": "alice", "X-PgToolBox-Level": "view"},
	} {
		if got := getWithHeaders(t, h, "/logs/orders-1?raw=1", headers).Code; got != http.StatusForbidden {
			t.Fatalf("raw tail for %v = %d, want 403", headers, got)
		}
	}
	rec := getWithHeaders(t, h, "/logs/orders-1?raw=1", powerUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("raw tail status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("raw tail content type = %q", ct)
	}
	if rec.Body.String() != "LOG: ready to accept connections" {
		t.Fatalf("raw tail body = %q", rec.Body.String())
	}
}

func TestClusterPodsCarriesRecentTimelineAndRowLinks(t *testing.T) {
	t.Parallel()
	h := newPodDetailHandler(t, podDetailSources(), podDetailHistory())
	body := get(t, h, http.MethodGet, "/cluster/pods").Body.String()
	for _, want := range []string{
		"Recent pod history", "Unhealthy", "definition changed",
		`data-href="/cluster/pods/orders-1"`, `href="/cluster/pods/orders-1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pods roster misses %q", want)
		}
	}
}

// The pooler roster gets the same screen the instance roster does, and
// through its own membership proof: a pod the pooler watch never
// reported is not found there, whatever else the cluster runs.
func TestPoolerPodDetailUsesTheirOwnRoster(t *testing.T) {
	t.Parallel()
	h := newPodDetailHandler(t, podDetailSources(), podDetailHistory())

	// An instance pod is not a pooler pod, and vice versa.
	if got := get(t, h, http.MethodGet, "/poolers/pods/orders-1").Code; got != http.StatusNotFound {
		t.Errorf("an instance pod resolved on the pooler roster: status %d", got)
	}
	if got := get(t, h, http.MethodGet, "/cluster/pods/orders-1").Code; got != http.StatusOK {
		t.Fatalf("instance pod detail status = %d, want 200", got)
	}
	body := get(t, h, http.MethodGet, "/cluster/pods/orders-1").Body.String()
	for _, want := range []string{`href="/cluster/pods"`, "Instance pods", "<dt>Role</dt>"} {
		if !strings.Contains(body, want) {
			t.Errorf("instance pod detail misses %q", want)
		}
	}
	// The roster screen links each row at its own detail route, and the
	// detail names the roster it came from rather than the other one.
	roster := get(t, h, http.MethodGet, "/poolers/pods").Body.String()
	if !strings.Contains(roster, `data-href="/poolers/pods/orders-pool-rw-abc"`) {
		t.Error("the pooler roster does not open its pods")
	}
	detail := get(t, h, http.MethodGet, "/poolers/pods/orders-pool-rw-abc")
	if detail.Code != http.StatusOK {
		t.Fatalf("pooler pod detail status = %d, want 200", detail.Code)
	}
	for _, want := range []string{`href="/poolers/pods"`, "Pooler pods", "<dt>Pooler</dt>"} {
		if !strings.Contains(detail.Body.String(), want) {
			t.Errorf("pooler pod detail misses %q", want)
		}
	}
	// A pooler pod has no primary, so it never carries the write
	// endpoint or the timeline the operator reports for one.
	if strings.Contains(detail.Body.String(), "Write endpoint") {
		t.Error("a pooler pod claims the cluster's write endpoint")
	}
}

// The follow poll asks the raw route for the tail alone. Serving it the
// rendered page instead is invisible on first paint — the server-side
// content is right — and only shows up when the first refresh writes
// the page's own markup into the log pane.
func TestRawLogTailsServeTextNotAPage(t *testing.T) {
	t.Parallel()
	h := newPodDetailHandler(t, podDetailSources(), podDetailHistory())
	for _, url := range []string{
		"/logs/orders-1?raw=1",
		"/poolers/logs/orders-pool-rw-abc?raw=1",
	} {
		rec := getWithHeaders(t, h, url,
			map[string]string{"X-Forwarded-User": "alice", "X-PgToolBox-Level": "dba"})
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", url, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("%s content-type = %q, want text/plain", url, ct)
		}
		if strings.Contains(rec.Body.String(), "<html") || strings.Contains(rec.Body.String(), "<pre") {
			t.Errorf("%s served markup into the log pane", url)
		}
	}
}
