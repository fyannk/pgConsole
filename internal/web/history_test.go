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
)

type fakeHistorySource struct {
	snap      history.Snapshot
	ok        bool
	revisions map[uint64]history.Revision
	diffs     map[uint64]history.Diff
}

func (f fakeHistorySource) Snapshot() (history.Snapshot, bool) { return f.snap, f.ok }
func (f fakeHistorySource) Revision(seq uint64) (history.Revision, bool) {
	rev, ok := f.revisions[seq]
	return rev, ok
}
func (f fakeHistorySource) Diff(seq uint64) (history.Diff, bool) {
	diff, ok := f.diffs[seq]
	return diff, ok
}

func newHistoryHandler(t *testing.T, source HistorySource) *Handler {
	t.Helper()
	base := staticSnapshots{}
	h, err := New(
		Config{ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour, LevelHeader: "X-PgToolBox-Level"},
		Sources{
			Cluster: base, Pods: base, Events: base, Backups: base, Poolers: base,
			PoolerPods: base, FailoverQuorum: base, ImageCatalogs: base,
			DatabaseObjects: base, History: source,
		},
		kube.FakeProber{}, fakeTailer{}, Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
		nil, nil, func() time.Time { return testNow }, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func TestHistoryRouteExistsOnlyWithReadSource(t *testing.T) {
	t.Parallel()
	without, _ := newTestHandler(t, EmptySnapshots{}, kube.FakeProber{}, Links{})
	if got := get(t, without, http.MethodGet, "/history").Code; got != http.StatusNotFound {
		t.Fatalf("disabled history status = %d, want 404", got)
	}

	with := newHistoryHandler(t, fakeHistorySource{})
	if got := get(t, with, http.MethodGet, "/history").Code; got != http.StatusOK {
		t.Fatalf("enabled history status = %d, want 200", got)
	}
}

func TestHistoryTimelineIsBoundedAndAttributesGaps(t *testing.T) {
	t.Parallel()
	entries := make([]history.Entry, 0, historyPageSize+1)
	for seq := uint64(historyPageSize + 1); seq > 0; seq-- {
		entries = append(entries, history.Entry{
			Seq: seq, Scope: "pods", Kind: "Pod", Namespace: "payments", Name: "orders-1",
			Change: history.ChangeStatus, Actor: history.Actor{Manager: "kubelet", Operation: "Update"},
			ObservedAt: testNow.Add(-time.Duration(seq) * time.Second), HasManifest: true,
			AfterGap: seq == historyPageSize+1, PrevObservedAt: testNow.Add(-10 * time.Minute),
		})
	}
	h := newHistoryHandler(t, fakeHistorySource{snap: history.Snapshot{Generation: 33, Entries: entries, Evicted: true}, ok: true})
	body := get(t, h, http.MethodGet, "/history").Body.String()
	if got := strings.Count(body, `data-history-seq=`); got != historyPageSize {
		t.Fatalf("rendered entries = %d, want %d", got, historyPageSize)
	}
	for _, want := range []string{
		"retention has evicted older revisions", "discovered after a gap", "self-declared field manager",
		`href="/history?before=2"`, "source: Kubernetes-reported object definitions",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("history page misses %q", want)
		}
	}
}

func TestHistoryViewBoundsLargeTimeline(t *testing.T) {
	t.Parallel()
	const retained = 20000
	entries := make([]history.Entry, 0, retained)
	for seq := uint64(retained); seq > 0; seq-- {
		entries = append(entries, history.Entry{
			Seq: seq, Kind: "Pod", Name: "orders-1", ObservedAt: testNow, Change: history.ChangeStatus,
		})
	}
	h := newHistoryHandler(t, fakeHistorySource{})
	view := h.buildHistoryView(history.Snapshot{Entries: entries}, 0)
	if len(view.Entries) != historyPageSize || !view.HasMore || view.NextURL != "/history?before=19901" {
		t.Fatalf("large timeline view = entries %d, more %v, next %q", len(view.Entries), view.HasMore, view.NextURL)
	}
}

func TestHistoryRevisionEscapesAndBoundsDefinition(t *testing.T) {
	t.Parallel()
	const hostile = `</code><script>alert(1)</script>`
	manifest := []byte(`{"apiVersion":"v1","kind":"Pod","spec":{"value":"` + hostile + `","large":"` + strings.Repeat("x", historyManifestMaxBytes) + `"}}`)
	source := fakeHistorySource{
		revisions: map[uint64]history.Revision{7: {
			Seq: 7, Kind: "Pod", Name: "orders-1", Change: history.ChangeSpec,
			ObservedAt: testNow.Add(-time.Minute), Actor: history.Actor{Manager: "manager"}, Manifest: manifest,
		}},
		diffs: map[uint64]history.Diff{7: {
			Seq: 7, HasBase: true, BaseSeq: 6, BaseObservedAt: testNow.Add(-2 * time.Minute),
			Entries: []history.DiffEntry{{Path: ".spec.value", Op: history.DiffChanged, Before: hostile, After: "safe"}},
		}},
	}
	h := newHistoryHandler(t, source)
	rec := get(t, h, http.MethodGet, "/history/revisions/7")
	if rec.Code != http.StatusOK {
		t.Fatalf("revision status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, hostile) || strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("history revision rendered hostile definition or diff without escaping")
	}
	for _, want := range []string{"manifest display was truncated at 256 KiB", "revision 6", ".spec.value"} {
		if !strings.Contains(body, want) {
			t.Errorf("revision page misses %q", want)
		}
	}
}

func TestHistoryRejectsInvalidCursorAndUnknownRevision(t *testing.T) {
	t.Parallel()
	h := newHistoryHandler(t, fakeHistorySource{})
	if got := get(t, h, http.MethodGet, "/history?before=not-a-number").Code; got != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d, want 400", got)
	}
	if got := get(t, h, http.MethodGet, "/history/revisions/99").Code; got != http.StatusNotFound {
		t.Fatalf("unknown revision status = %d, want 404", got)
	}
}
