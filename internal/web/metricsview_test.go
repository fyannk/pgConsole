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
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/metrics"
)

func newMetricsHandler(t *testing.T, source MetricsSource) *Handler {
	t.Helper()
	base := staticSnapshots{}
	h, err := New(
		Config{ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour, LevelHeader: "X-PgToolBox-Level"},
		Sources{
			Cluster: base, Pods: base, Events: base, Backups: base, Poolers: base,
			PoolerPods: base, FailoverQuorum: base, ImageCatalogs: base,
			DatabaseObjects: base, Metrics: source,
		},
		kube.FakeProber{}, fakeTailer{}, Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
		nil, nil, func() time.Time { return testNow }, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func sampledMetrics() *metrics.Store {
	store := metrics.NewStore(metrics.Limits{Interval: 10 * time.Second})
	base := testNow.Add(-time.Minute)
	for i := 0; i < 3; i++ {
		at := base.Add(time.Duration(i) * 10 * time.Second)
		store.Observe("orders-1", at,
			map[string]float64{"connections": float64(5 + i), "database-size": 2 << 30},
			map[string]float64{
				"postgres-version":   18.4,
				"in-recovery":        0,
				"last-archived-time": float64(at.Add(-time.Minute).Unix()),
				"last-failed-backup": 0,
			})
		if i != 1 { // orders-2 misses the middle sweep: an honest gap.
			store.Observe("orders-2", at,
				map[string]float64{"connections": 2},
				map[string]float64{"postgres-version": 18.4, "in-recovery": 1})
		}
	}
	return store
}

func TestMetricsRouteExistsOnlyWithSource(t *testing.T) {
	t.Parallel()
	without, _ := newTestHandler(t, EmptySnapshots{}, kube.FakeProber{}, Links{})
	if got := get(t, without, http.MethodGet, "/cluster/metrics").Code; got != http.StatusNotFound {
		t.Fatalf("disabled metrics status = %d, want 404", got)
	}
	body := get(t, without, http.MethodGet, "/").Body.String()
	if !strings.Contains(body, "Instance metrics — disabled in this deployment") {
		t.Error("the disabled deployment does not state metrics as off in the sidebar")
	}

	with := newMetricsHandler(t, sampledMetrics())
	if got := get(t, with, http.MethodGet, "/cluster/metrics").Code; got != http.StatusOK {
		t.Fatalf("enabled metrics status = %d, want 200", got)
	}
}

func TestMetricsScreenStatesFactsBeforeAnyScript(t *testing.T) {
	t.Parallel()
	h := newMetricsHandler(t, sampledMetrics())
	body := get(t, h, http.MethodGet, "/cluster/metrics").Body.String()
	for _, want := range []string{
		"<h1>Metrics</h1>", "Scraped every", "Connections",
		`data-metric-key="connections"`,
		"orders-1", "orders-2", "2.0 GiB",
		"source: instance-reported metrics",
		"no samples for this series in the retained window",
		`href="/cluster/metrics"`,
		// The tiles state their claim, and when it was claimed.
		"PostgreSQL version", "18.4", "In recovery", "no — accepting writes",
		// The explanation is served markup opened by the browser's own
		// popover wiring, so it is reachable with scripting off.
		"What this means, and why it matters", "What to watch for",
		`popovertarget="note-metrics-cluster-connections"`,
		`<div class="metric-note" id="note-metrics-cluster-connections" popover>`,
		// The sections and their introductions are part of the document.
		"Write-ahead log", "Sessions and contention",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics screen misses %q", want)
		}
	}
	// Every explanation the catalog carries must reach the page: a
	// dashboard that shows a line it cannot explain is the thing this
	// screen exists not to be.
	for _, def := range metrics.Catalog {
		if !strings.Contains(body, template.HTMLEscapeString(def.Note.Watch)) {
			t.Errorf("series %q reaches the screen without its explanation", def.Key)
		}
	}
	for _, def := range metrics.Instants {
		if !strings.Contains(body, template.HTMLEscapeString(def.Note.Watch)) {
			t.Errorf("tile %q reaches the screen without its explanation", def.Key)
		}
	}
	// Every claim on this screen has one origin, so it is stated once
	// for the screen. Repeating it under each of seventy-odd panels made
	// it furniture; stating it nowhere would break rule 8.
	if got := strings.Count(body, "source: instance-reported metrics"); got != 1 {
		t.Errorf("the screen attributes its claims %d times, want exactly once", got)
	}
	if !strings.Contains(body, `<footer class="metrics-origin">`) {
		t.Error("the origin is not stated in the page footer")
	}
}

// One tab per instance, plus the cluster tab that holds them together.
// A pod tab must carry only its own instance's rows: the whole point of
// splitting them is that a reader on orders-2's tab is looking at
// orders-2.
func TestMetricsScreenTabsScopeToOneInstance(t *testing.T) {
	t.Parallel()
	h := newMetricsHandler(t, sampledMetrics())
	body := get(t, h, http.MethodGet, "/cluster/metrics").Body.String()
	for _, want := range []string{
		`data-tab="metrics-cluster"`,
		`data-tab="metrics-pod-orders-1"`,
		`data-tab="metrics-pod-orders-2"`,
		`data-metrics-instance="orders-1"`,
		`data-metric-instance="orders-2"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics screen misses %q", want)
		}
	}

	// Each panel scoped to orders-2 must state orders-2's numbers and
	// no one else's. Cut the document at that tab's panel and look.
	tab := body[strings.Index(body, `id="metrics-pod-orders-2"`):]
	if next := strings.Index(tab, `id="metrics-pod-orders-3"`); next > 0 {
		tab = tab[:next]
	}
	if strings.Contains(tab, "<td>orders-1</td>") {
		t.Error("the orders-2 tab carries orders-1's rows")
	}
	if !strings.Contains(tab, "<td>orders-2</td>") {
		t.Error("the orders-2 tab is missing its own rows")
	}
}

// With no script every tab panel stays visible, so nothing the screen
// serves is reachable only by clicking. The tablist narrows it; it does
// not gate it.
func TestMetricsTabPanelsAreNotHiddenInTheMarkup(t *testing.T) {
	t.Parallel()
	h := newMetricsHandler(t, sampledMetrics())
	body := get(t, h, http.MethodGet, "/cluster/metrics").Body.String()
	for _, panel := range []string{"metrics-cluster", "metrics-pod-orders-1"} {
		open := strings.Index(body, `id="`+panel+`"`)
		if open < 0 {
			t.Fatalf("panel %q is missing", panel)
		}
		tag := body[strings.LastIndex(body[:open], "<"):]
		tag = tag[:strings.Index(tag, ">")]
		if strings.Contains(tag, "hidden") {
			t.Errorf("panel %q is served hidden, so its content needs script to reach", panel)
		}
	}
}

func TestMetricsSeriesEndpointAlignsInstancesAndRefusesUnknowns(t *testing.T) {
	t.Parallel()
	h := newMetricsHandler(t, sampledMetrics())

	rec := get(t, h, http.MethodGet, "/cluster/metrics/series?key=connections")
	if rec.Code != http.StatusOK {
		t.Fatalf("series status = %d, want 200", rec.Code)
	}
	var payload struct {
		Unit      string  `json:"unit"`
		Times     []int64 `json:"times"`
		Instances []struct {
			Name   string     `json:"name"`
			Values []*float64 `json:"values"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if len(payload.Times) != 3 || len(payload.Instances) != 2 {
		t.Fatalf("payload shape = %d times, %d instances", len(payload.Times), len(payload.Instances))
	}
	if payload.Instances[1].Name != "orders-2" || payload.Instances[1].Values[1] != nil {
		t.Fatal("the missed sweep did not read back as a null gap")
	}

	if got := get(t, h, http.MethodGet, "/cluster/metrics/series?key=nope").Code; got != http.StatusNotFound {
		t.Fatalf("unknown key status = %d, want 404", got)
	}
	if got := get(t, h, http.MethodGet, "/cluster/metrics/series?key=connections&window=huge").Code; got != http.StatusBadRequest {
		t.Fatalf("unknown window status = %d, want 400", got)
	}
	if got := get(t, h, http.MethodGet, "/cluster/metrics/series?key=connections&window=retention").Code; got != http.StatusOK {
		t.Fatalf("retention window status = %d, want 200", got)
	}
}

// The per-pod tabs fetch their charts scoped to one instance, so the
// endpoint takes an instance — and must refuse one the store does not
// track, rather than answering for a name a caller invented.
func TestMetricsSeriesEndpointScopesToATrackedInstance(t *testing.T) {
	t.Parallel()
	h := newMetricsHandler(t, sampledMetrics())

	rec := get(t, h, http.MethodGet, "/cluster/metrics/series?key=connections&instance=orders-2")
	if rec.Code != http.StatusOK {
		t.Fatalf("scoped series status = %d, want 200", rec.Code)
	}
	var payload struct {
		Instances []struct {
			Name string `json:"name"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if len(payload.Instances) != 1 || payload.Instances[0].Name != "orders-2" {
		t.Fatalf("scoped payload = %v, want orders-2 alone", payload.Instances)
	}

	if got := get(t, h, http.MethodGet, "/cluster/metrics/series?key=connections&instance=orders-9").Code; got != http.StatusNotFound {
		t.Fatalf("untracked instance status = %d, want 404", got)
	}
}
