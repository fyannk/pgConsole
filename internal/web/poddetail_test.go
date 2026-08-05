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
	// The baseline sees the revision stated but not linked, no logs tab,
	// and no raw definition anywhere in the served markup.
	if strings.Contains(body, `href="/history/revisions/3"`) {
		t.Error("baseline links a revision the route would refuse")
	}
	if !strings.Contains(body, "details require the poweruser or dba level") {
		t.Error("baseline does not state why the revision detail is absent")
	}
	if strings.Contains(body, `data-tab="pod-logs"`) || !strings.Contains(body, "log access requires the poweruser or dba level") {
		t.Error("logs gate is not stated at the baseline")
	}
	if strings.Contains(body, `"metadata":{"name":"orders-1"}`) || strings.Contains(body, "pod-raw") {
		t.Error("the raw definition rendered below the gate")
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
	} {
		if !strings.Contains(body, want) {
			t.Errorf("elevated pod detail misses %q", want)
		}
	}
}

func TestRawTailIsGatedPlainText(t *testing.T) {
	t.Parallel()
	h := newPodDetailHandler(t, podDetailSources(), nil)
	if got := get(t, h, http.MethodGet, "/logs/orders-1?raw=1").Code; got != http.StatusForbidden {
		t.Fatalf("ungated raw tail status = %d, want 403", got)
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
