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
	"github.com/fyannk/pgConsole/internal/logstream"
	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

var powerHeaders = map[string]string{"X-Forwarded-User": "op@corp", "X-PgToolBox-Level": "poweruser"}

// newLogsHandler builds a handler with an optional retained buffer and a
// scripted tailer, so a case can hold the two log sources in any
// combination — including the one that matters most, a buffer that
// outlives a pod the tailer no longer finds.
func newLogsHandler(t *testing.T, buffer *logstream.Buffer, tailer LogTailer) *Handler {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h, err := New(Config{
		ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour,
		AllowLogs: true, LevelHeader: "X-PgToolBox-Level",
	},
		Sources{Cluster: staticSnapshots{}, Pods: staticSnapshots{}, Events: staticSnapshots{},
			Backups: staticSnapshots{}, Poolers: staticSnapshots{}, PoolerPods: staticSnapshots{},
			FailoverQuorum: staticSnapshots{}, ImageCatalogs: staticSnapshots{},
			DatabaseObjects: staticSnapshots{}, Infrastructure: staticSnapshots{},
			LogBuffer: buffer},
		kube.UnavailableProber{}, tailer, Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
		nil, nil, func() time.Time { return testNow }, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// seededBuffer holds one stream with a gap in the middle, the shape a
// container restart leaves behind.
func seededBuffer(t *testing.T) *logstream.Buffer {
	t.Helper()
	b := logstream.NewBuffer(1<<20, 1<<20, time.Hour)
	b.Observe(logstream.Line{Pod: "orders-1", Container: "plugin-barman-cloud",
		Text: "before the restart", At: testNow.Add(-10 * time.Minute)})
	b.Gap("orders-1", "plugin-barman-cloud", testNow.Add(-5*time.Minute),
		"stream ended; lines emitted before it was reopened were not observed")
	b.Observe(logstream.Line{Pod: "orders-1", Container: "plugin-barman-cloud",
		Text: "after the restart", At: testNow.Add(-time.Minute)})
	return b
}

// TestAddressedLogsServeTheRetainedStreamFirst proves use case one: with
// retention on, the screen is powered by the continuous follow — and the
// record outlives the container. The tailer answers not-found, as it
// would for a dead pod, and the reader still gets the stream.
func TestAddressedLogsServeTheRetainedStreamFirst(t *testing.T) {
	t.Parallel()
	deadPod := fakeTailer{err: redact.NewError("log tail", redact.CategoryNotFound, nil)}
	h := newLogsHandler(t, seededBuffer(t), deadPod)

	rec := getWithHeaders(t, h, "/logs/orders-1/plugin-barman-cloud", powerHeaders)
	if rec.Code != http.StatusOK {
		t.Fatalf("retained view = %d, want 200 even though the live tail is not found", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"before the restart",
		"after the restart",
		"lines emitted before it was reopened were not observed", // the gap, visible
		"best effort",
		"observation times by the console",
		`href="/logs/orders-1/plugin-barman-cloud?raw=1"`, // the live tail, one click away
	} {
		if !strings.Contains(body, want) {
			t.Errorf("retained view misses %q", want)
		}
	}
	// The gap must separate the two lines, not trail them: order is the
	// record.
	if strings.Index(body, "before the restart") > strings.Index(body, "reopened") ||
		strings.Index(body, "reopened") > strings.Index(body, "after the restart") {
		t.Error("gap marker is not between the lines it separates")
	}
}

// TestAddressedLogsFallBackToTheLiveTail proves the fallback in both
// directions retention can be absent: no buffer at all, and a buffer
// that holds nothing for the container asked about.
func TestAddressedLogsFallBackToTheLiveTail(t *testing.T) {
	t.Parallel()
	live := fakeTailer{tail: observe.LogTail{Content: "live kubelet lines", LineLimit: 200, ByteLimit: 4096}}
	for name, buffer := range map[string]*logstream.Buffer{
		"no buffer":     nil,
		"unheld stream": seededBuffer(t), // holds barman, not postgres
		"retention off": logstream.NewBuffer(0, 0, time.Hour),
	} {
		h := newLogsHandler(t, buffer, live)
		rec := getWithHeaders(t, h, "/logs/orders-1/postgres", powerHeaders)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: live fallback = %d, want 200", name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "live kubelet lines") {
			t.Errorf("%s: live fallback did not serve the tail", name)
		}
		if strings.Contains(rec.Body.String(), "best effort") {
			t.Errorf("%s: live view claims to be retained", name)
		}
	}
}

// TestUnaddressedLogsOfferTheRetainedStreams proves the plain route
// keeps its meaning — the live PostgreSQL tail — while offering the
// streams the console holds, which is how a reader reaches a sidecar's
// or a dead container's record.
func TestUnaddressedLogsOfferTheRetainedStreams(t *testing.T) {
	t.Parallel()
	live := fakeTailer{tail: observe.LogTail{Content: "live kubelet lines", LineLimit: 200, ByteLimit: 4096}}
	h := newLogsHandler(t, seededBuffer(t), live)

	body := getWithHeaders(t, h, "/logs/orders-1", powerHeaders).Body.String()
	if !strings.Contains(body, "live kubelet lines") {
		t.Error("plain route no longer serves the live tail")
	}
	if !strings.Contains(body, `href="/logs/orders-1/plugin-barman-cloud"`) {
		t.Error("retained stream not offered from the plain route")
	}
	// And with retention off, the section is absent rather than empty.
	bare := newLogsHandler(t, nil, live)
	if strings.Contains(getWithHeaders(t, bare, "/logs/orders-1", powerHeaders).Body.String(), "Retained streams") {
		t.Error("retained-streams section rendered with retention off")
	}
}

// TestRawStaysLive proves ?raw=1 is the kubelet's answer even when a
// retained stream exists: the follow enhancement polls it for fresh
// lines, and serving it retained content would freeze the poll at the
// buffer's state.
func TestRawStaysLive(t *testing.T) {
	t.Parallel()
	live := fakeTailer{tail: observe.LogTail{Content: "live kubelet lines", LineLimit: 200, ByteLimit: 4096}}
	h := newLogsHandler(t, seededBuffer(t), live)
	rec := getWithHeaders(t, h, "/logs/orders-1/plugin-barman-cloud?raw=1", powerHeaders)
	if body := rec.Body.String(); !strings.Contains(body, "live kubelet lines") ||
		strings.Contains(body, "before the restart") {
		t.Errorf("raw served retained content: %q", body)
	}
}
