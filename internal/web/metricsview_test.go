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
		store.Observe("orders-1", at, map[string]float64{"connections": float64(5 + i), "database-size": 2 << 30})
		if i != 1 { // orders-2 misses the middle sweep: an honest gap.
			store.Observe("orders-2", at, map[string]float64{"connections": 2})
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
		"Instance metrics", "Scraped every", "Connections",
		"data-metric-series=", `data-metric-key="connections"`,
		"orders-1", "orders-2", "2.0 GiB",
		"source: instance-reported metrics",
		"no samples for this series in the retained window",
		`href="/cluster/metrics"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics screen misses %q", want)
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
