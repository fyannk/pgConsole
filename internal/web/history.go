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
	"fmt"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/fyannk/pgConsole/internal/history"
)

const (
	historyPageSize         = 100
	historyManifestMaxBytes = 256 * 1024
)

// HistoryView is the bounded, newest-first object-definition timeline.
type HistoryView struct {
	// Shell is the shared application chrome.
	Shell ShellView
	// Generation is the store generation at this read.
	Generation uint64
	// Entries are the bounded timeline page.
	Entries []HistoryEntryView
	// Evicted reports that retention removed entries older than the retained
	// timeline.
	Evicted bool
	// HasMore reports that another retained page exists.
	HasMore bool
	// Before is the active exclusive sequence cursor; zero is the newest page.
	Before uint64
	// NextURL requests the next retained page when HasMore is true.
	NextURL string
}

// HistoryEntryView is one manifest-free timeline entry.
type HistoryEntryView struct {
	// Seq is the stable retained revision handle.
	Seq uint64
	// Object is the Kubernetes kind and object name.
	Object string
	// Namespace is the Kubernetes namespace reported on the object.
	Namespace string
	// Scope is the capture pump that observed the object.
	Scope string
	// Change is the closed revision classification.
	Change string
	// ObservedAt is the application observation time in UTC.
	ObservedAt string
	// Age is the elapsed time since this process observed the revision.
	Age string
	// Manager is the self-declared Kubernetes field manager, or unknown.
	Manager string
	// Operation is the managed-fields operation, when one was reported.
	Operation string
	// GapWindow names the unobserved interval when the change followed a gap.
	GapWindow string
	// Coalesced is the number of additional status transitions folded in.
	Coalesced int
	// HasManifest reports that a scrubbed definition can be opened.
	HasManifest bool
}

// HistoryRevisionView is one bounded scrubbed manifest and its on-demand
// structural diff.
type HistoryRevisionView struct {
	// Shell is the shared application chrome.
	Shell ShellView
	// Seq is the retained revision handle.
	Seq uint64
	// Object is the Kubernetes kind and object name.
	Object string
	// Change is the revision classification.
	Change string
	// ObservedAt is the application observation time in UTC.
	ObservedAt string
	// Manager is the self-declared Kubernetes field manager, or unknown.
	Manager string
	// Operation is the reported managed-fields operation.
	Operation string
	// Manifest is the scrubbed, bounded JSON definition.
	Manifest string
	// ManifestTruncated reports that the display byte ceiling cut the JSON.
	ManifestTruncated bool
	// DiffAvailable reports that the revision remained retained long enough to
	// compute its diff after the manifest read.
	DiffAvailable bool
	// HasBase reports that the previous retained definition was available.
	HasBase bool
	// BaseSeq is the baseline revision handle when HasBase is true.
	BaseSeq uint64
	// BaseObservedAt is the baseline's application observation time.
	BaseObservedAt string
	// DiffEntries are the already-bounded structural differences.
	DiffEntries []HistoryDiffEntryView
	// DiffTruncated reports that the domain diff entry ceiling was reached.
	DiffTruncated bool
}

// HistoryDiffEntryView is one bounded changed path.
type HistoryDiffEntryView struct {
	// Path is the structural Kubernetes field path.
	Path string
	// Operation is added, removed, or changed.
	Operation string
	// Before is the bounded previous JSON value.
	Before string
	// After is the bounded newer JSON value.
	After string
	// Manager is the unambiguous self-declared field manager, when known.
	Manager string
}

func (h *Handler) handleHistory(w http.ResponseWriter, r *http.Request) {
	before, err := parseHistoryCursor(r.URL.Query().Get("before"))
	if err != nil {
		h.renderDenied(w, r, http.StatusBadRequest, "invalid history cursor")
		return
	}
	snap, _ := h.sources.History.Snapshot()
	view := h.buildHistoryView(snap, before)
	view.Shell = h.shell(r, "history")
	h.renderPage(w, "history", "history.html.tmpl", view)
}

func (h *Handler) handleHistoryRevision(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseUint(r.PathValue("seq"), 10, 64)
	if err != nil || seq == 0 {
		http.NotFound(w, r)
		return
	}
	rev, ok := h.sources.History.Revision(seq)
	if !ok {
		http.NotFound(w, r)
		return
	}
	view := buildHistoryRevisionView(rev)
	view.Shell = h.shell(r, "history")
	if diff, retained := h.sources.History.Diff(seq); retained {
		view.DiffAvailable = true
		view.HasBase = diff.HasBase
		view.BaseSeq = diff.BaseSeq
		view.DiffTruncated = diff.Truncated
		if diff.HasBase {
			view.BaseObservedAt = diff.BaseObservedAt.UTC().Format(time.RFC3339)
		}
		for _, entry := range diff.Entries {
			view.DiffEntries = append(view.DiffEntries, HistoryDiffEntryView{
				Path: entry.Path, Operation: string(entry.Op), Before: entry.Before,
				After: entry.After, Manager: entry.Manager,
			})
		}
	}
	h.renderPage(w, "history-revision", "history-revision.html.tmpl", view)
}

func parseHistoryCursor(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	seq, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || seq == 0 {
		return 0, fmt.Errorf("invalid cursor")
	}
	return seq, nil
}

func (h *Handler) buildHistoryView(snap history.Snapshot, before uint64) HistoryView {
	view := HistoryView{Generation: snap.Generation, Evicted: snap.Evicted, Before: before}
	for _, entry := range snap.Entries {
		if before != 0 && entry.Seq >= before {
			continue
		}
		if len(view.Entries) == historyPageSize {
			view.HasMore = true
			break
		}
		manager := entry.Actor.Manager
		if manager == "" {
			manager = "unknown"
		}
		item := HistoryEntryView{
			Seq: entry.Seq, Object: entry.Kind + "/" + entry.Name,
			Namespace: entry.Namespace, Scope: entry.Scope, Change: string(entry.Change),
			ObservedAt: entry.ObservedAt.UTC().Format(time.RFC3339),
			Age:        formatAge(h.now().Sub(entry.ObservedAt)),
			Manager:    manager, Operation: entry.Actor.Operation,
			Coalesced: entry.Coalesced, HasManifest: entry.HasManifest,
		}
		if entry.AfterGap {
			item.GapWindow = formatHistoryGap(entry.PrevObservedAt, entry.ObservedAt)
		}
		view.Entries = append(view.Entries, item)
	}
	if view.HasMore && len(view.Entries) > 0 {
		view.NextURL = fmt.Sprintf("/history?before=%d", view.Entries[len(view.Entries)-1].Seq)
	}
	return view
}

func formatHistoryGap(previous, observed time.Time) string {
	if previous.IsZero() {
		return "inside an unobserved window ending " + observed.UTC().Format(time.RFC3339)
	}
	return "between " + previous.UTC().Format(time.RFC3339) + " and " + observed.UTC().Format(time.RFC3339)
}

func buildHistoryRevisionView(rev history.Revision) HistoryRevisionView {
	manager := rev.Actor.Manager
	if manager == "" {
		manager = "unknown"
	}
	manifest, truncated := renderHistoryManifest(rev.Manifest)
	return HistoryRevisionView{
		Seq: rev.Seq, Object: rev.Kind + "/" + rev.Name, Change: string(rev.Change),
		ObservedAt: rev.ObservedAt.UTC().Format(time.RFC3339), Manager: manager,
		Operation: rev.Actor.Operation, Manifest: manifest, ManifestTruncated: truncated,
	}
}

func renderHistoryManifest(raw []byte) (string, bool) {
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err == nil {
		raw = pretty.Bytes()
	}
	if len(raw) <= historyManifestMaxBytes {
		return string(raw), false
	}
	raw = raw[:historyManifestMaxBytes]
	for len(raw) > 0 && !utf8.Valid(raw) {
		raw = raw[:len(raw)-1]
	}
	return string(raw), true
}
