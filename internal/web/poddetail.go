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
	"html/template"
	"net/http"
	"sort"
	"time"

	"github.com/fyannk/pgConsole/internal/authz"
	"github.com/fyannk/pgConsole/internal/history"
	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

// PodDetailView is one instance pod's screen: its observed status, the
// merged per-pod timeline, and — above the same gates the standalone
// routes enforce — the bounded log tail and the retained raw
// definition. Every fact is an observation or a report, and each panel
// attributes its own source.
type PodDetailView struct {
	Shell ShellView
	// Meta is the pod snapshot's freshness.
	Meta SectionMeta
	// The observed status facts; unreported reads "unknown".
	Name       string
	Role       string
	Phase      string
	PhaseState string
	Ready      string
	Restarts   string
	Node       string
	IP         string
	Image      string
	Started    Stamp
	// Cluster-reported facts shown on the primary only.
	IsPrimary     bool
	Timeline      string
	WriteEndpoint string
	// Timeline is the merged per-pod history, newest first.
	History []PodTimelineEntry
	// HistoryAvailable distinguishes an empty timeline from a disabled
	// history store.
	HistoryAvailable bool
	// CanInspect gates the raw-definition dialog, same tier as the
	// history revision routes.
	CanInspect bool
	// Raw is the pretty-printed retained definition; empty when none is
	// retained or the viewer is below the gate.
	Raw template.HTML
	// RawSeq is the revision the raw definition came from.
	RawSeq uint64
	// CanTailLogs gates the logs tab, same tier as the log routes.
	CanTailLogs bool
	// LogsGate states why the logs tab is absent when it is.
	LogsGate string
	// Logs carries the tail when CanTailLogs; nil otherwise.
	Logs *PodLogsView
}

// PodLogsView is the embedded bounded tail.
type PodLogsView struct {
	// State is the failure wording when the tail could not be fetched.
	State string
	// Bounds states the tail's limits.
	Bounds string
	// Content is the bounded tail itself.
	Content string
	// Truncated reports the byte ceiling cut the oldest lines.
	Truncated bool
	// RawURL is the plain-text route the follow enhancement polls.
	RawURL string
}

// PodTimelineEntry is one merged timeline row: a Kubernetes event or a
// retained definition revision, in the words its source reported.
type PodTimelineEntry struct {
	// Kind selects the marker: created, changed, warning, deleted,
	// normal.
	Kind string
	// Title is the reported reason or the revision's change token.
	Title string
	// Tag is the short badge text.
	Tag string
	// Object is "Kind/name".
	Object string
	// Body is the reported message or the self-declared actor line.
	Body string
	// Age and Stamp render the when column.
	Age   string
	Stamp string
	// StampISO is the same instant machine-readable, so the browser may
	// restate the short stamp above in the reader’s own zone.
	StampISO string
	// Seq links a revision's detail when non-zero and the viewer clears
	// the gate.
	Seq uint64
	// at orders the merge; never rendered.
	at time.Time
}

// handlePodDetail renders one member pod. An unknown or non-member pod
// is not found: the roster is the same membership-verified snapshot
// every other screen trusts.
func (h *Handler) handlePodDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("pod")
	if !podNamePattern.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	snap, ok := h.sources.Pods.CurrentPods()
	if !ok {
		h.renderDenied(w, r, http.StatusNotFound, "no pod snapshot")
		return
	}
	var pod *observe.PodFacts
	for i := range snap.Pods {
		if snap.Pods[i].Name == name {
			pod = &snap.Pods[i]
			break
		}
	}
	if pod == nil {
		h.renderDenied(w, r, http.StatusNotFound, "no such member pod")
		return
	}

	now := h.now()
	view := PodDetailView{
		Meta:     buildMeta(snap.Generation, snap.ObservedAt, snap.Stale, now),
		Name:     pod.Name,
		Role:     orUnknown(pod.Role),
		Phase:    orUnknown(pod.Phase),
		Node:     orUnknown(pod.Node),
		IP:       orUnknown(pod.IP),
		Image:    orUnknown(pod.Image),
		Ready:    unknown,
		Restarts: unknown,
		Started:  Stamp{Text: unknown},
	}
	if pod.Deleting {
		view.Phase += " — deleting"
	}
	if pod.Ready != nil {
		view.Ready = fmt.Sprintf("%t", *pod.Ready)
	}
	if pod.Restarts != nil {
		view.Restarts = fmt.Sprintf("%d", *pod.Restarts)
	}
	if pod.Started != nil {
		view.Started = stampAt(*pod.Started)
	}
	row := PodRowView{Ready: view.Ready, Phase: view.Phase}
	view.PhaseState = podState(row)

	// Cluster-reported facts ride only on the pod the operator names as
	// primary, in the operator's own vocabulary.
	if clusterSnap, ok := h.sources.Cluster.Current(); ok && clusterSnap.Cluster.Present {
		if clusterSnap.Cluster.CurrentPrimary == pod.Name {
			view.IsPrimary = true
			view.WriteEndpoint = h.cfg.ClusterName + "-rw"
			view.Timeline = unknown
			if clusterSnap.Cluster.TimelineID != nil {
				view.Timeline = fmt.Sprintf("%d", *clusterSnap.Cluster.TimelineID)
			}
		}
	}

	view.History, view.HistoryAvailable = h.buildPodTimeline(pod.Name, 0, now)

	access := h.requestAccess(r)
	elevated := access.hasIdentity && access.level >= authz.TierPowerUser
	view.CanInspect = elevated
	if elevated {
		view.Raw, view.RawSeq = h.retainedDefinition(pod.Name)
	}

	switch {
	case !h.cfg.AllowLogs:
		view.LogsGate = "log access is disabled in this deployment"
	case !elevated:
		view.LogsGate = "log access requires the poweruser or dba level"
	default:
		view.CanTailLogs = true
		view.Logs = h.podLogs(r, pod.Name)
	}

	view.Shell = h.shell(r, "cluster-pods")
	h.renderPage(w, "pod-detail", "pod-detail.html.tmpl", view)
}

// podLogs fetches the bounded tail for the embedded logs tab. The same
// closed request-time exception as the standalone route: request-scoped,
// bounded, never cached.
func (h *Handler) podLogs(r *http.Request, pod string) *PodLogsView {
	logs := &PodLogsView{RawURL: "/logs/" + pod + "?raw=1"}
	if h.tailer == nil {
		logs.State = "unavailable: no Kubernetes access"
		return logs
	}
	tail, err := h.tailer.TailLogs(r.Context(), pod)
	switch {
	case err == nil:
		logs.Bounds = fmt.Sprintf("last %d lines, at most %d bytes", tail.LineLimit, tail.ByteLimit)
		logs.Content = tail.Content
		logs.Truncated = tail.TruncatedByBytes
	case redact.Categorize(err) == redact.CategoryForbidden:
		logs.State = "not granted: pods/log"
	default:
		logs.State = "unavailable: " + redact.Safe(err)
	}
	return logs
}

// retainedDefinition resolves the newest retained, scrubbed definition
// of the pod from the history store, pretty-printed.
func (h *Handler) retainedDefinition(pod string) (template.HTML, uint64) {
	if h.sources.History == nil {
		return "", 0
	}
	snap, ok := h.sources.History.Snapshot()
	if !ok {
		return "", 0
	}
	for _, entry := range snap.Entries { // newest first
		if entry.Kind != "Pod" || entry.Name != pod || !entry.HasManifest {
			continue
		}
		rev, ok := h.sources.History.Revision(entry.Seq)
		if !ok {
			return "", 0
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, rev.Manifest, "", "  "); err != nil {
			return highlightJSON(string(rev.Manifest)), entry.Seq
		}
		return highlightJSON(pretty.String()), entry.Seq
	}
	return "", 0
}

// buildPodTimeline merges the pod-involved events and the pod's
// retained revisions, newest first. An empty name merges every instance
// pod, for the roster screen's recent-history panel. Each entry keeps
// its source's words; nothing is narrated.
func (h *Handler) buildPodTimeline(name string, bound int, now time.Time) ([]PodTimelineEntry, bool) {
	var entries []PodTimelineEntry

	if events, ok := h.sources.Events.CurrentEvents(); ok {
		for _, ev := range events.Events {
			if ev.Kind != "Pod" {
				continue
			}
			if name != "" && ev.Object != name {
				continue
			}
			kind := "normal"
			if ev.Type == "Warning" {
				kind = "warning"
			}
			entry := PodTimelineEntry{
				Kind:   kind,
				Title:  orUnknown(ev.Reason),
				Tag:    kind,
				Object: "Pod/" + ev.Object,
				Body:   ev.Message,
				at:     ev.LastSeen,
			}
			if ev.Count > 1 {
				entry.Body = fmt.Sprintf("%s (reported %d times)", entry.Body, ev.Count)
			}
			entries = append(entries, entry)
		}
	}

	historyAvailable := h.sources.History != nil
	if historyAvailable {
		if snap, ok := h.sources.History.Snapshot(); ok {
			for _, e := range snap.Entries {
				if e.Kind != "Pod" {
					continue
				}
				if name != "" && e.Name != name {
					continue
				}
				entries = append(entries, podRevisionEntry(e))
			}
		}
	}

	sort.SliceStable(entries, func(a, b int) bool { return entries[a].at.After(entries[b].at) })
	if bound > 0 && len(entries) > bound {
		entries = entries[:bound]
	}
	for i := range entries {
		entries[i].Age = formatAge(now.Sub(entries[i].at))
		entries[i].Stamp = entries[i].at.UTC().Format("01-02 15:04")
		entries[i].StampISO = entries[i].at.UTC().Format(time.RFC3339)
	}
	return entries, historyAvailable
}

// podRevisionEntry renders one retained revision as a timeline row, in
// the history store's own change vocabulary.
func podRevisionEntry(e history.Entry) PodTimelineEntry {
	kind, title := "normal", "status transition"
	switch e.Change {
	case history.ChangeCreated:
		kind, title = "created", "pod created"
	case history.ChangeFirstObserved:
		kind, title = "created", "definition first observed"
	case history.ChangeSpec:
		kind, title = "changed", "definition changed"
	case history.ChangeDeleted, history.ChangeDeletedUnobserved:
		kind, title = "deleted", "pod deleted"
	}
	entry := PodTimelineEntry{
		Kind:   kind,
		Title:  title,
		Tag:    string(e.Change),
		Object: "Pod/" + e.Name,
		at:     e.ObservedAt,
	}
	if e.Actor.Manager != "" {
		entry.Body = e.Actor.Manager
		if e.Actor.Operation != "" {
			entry.Body += " — " + e.Actor.Operation
		}
		entry.Body += " (self-declared field manager)"
	}
	if e.HasManifest {
		entry.Seq = e.Seq
	}
	return entry
}
