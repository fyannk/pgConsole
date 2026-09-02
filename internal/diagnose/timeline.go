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

package diagnose

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fyannk/pgConsole/internal/history"
)

// The conditions here read the object timeline, which is the one input
// that carries the past rather than the present. Everything else the
// engine reads is a current snapshot, so a state that is wrong only
// because it keeps happening — a pod replaced over and over, a
// definition rewritten by two controllers in turn — is invisible to
// every other check. Nothing here needs state of its own: the store
// already retains the timeline, and a run just counts what is in it.
//
// Two properties of that store shape every claim made from it. It
// coalesces consecutive status transitions of one object inside a
// window, and it evicts old revisions to stay inside its bounds. Both
// mean a count taken from it is a floor: the timeline can under-report
// what happened and never over-report it. So a match here is sound, and
// an absence rules nothing out — the same footing as the log checks,
// and every rule built on these conditions says so.

// windowed is the entries of one bounded kind observed inside the
// trailing window, grouped by object name in a stable order.
func windowed(in Input, kind string, span time.Duration) ([]string, map[string][]history.Entry) {
	cutoff := in.Now.Add(-span)
	byName := map[string][]history.Entry{}
	for _, entry := range in.History.Entries {
		if kind != "" && entry.Kind != kind {
			continue
		}
		if entry.ObservedAt.Before(cutoff) {
			continue
		}
		byName[entry.Name] = append(byName[entry.Name], entry)
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, byName
}

// gapNote reports how many of the entries were discovered only after a
// contact gap, whose timing is therefore bounded rather than exact.
func gapNote(entries []history.Entry) string {
	gapped := 0
	for _, entry := range entries {
		if entry.AfterGap {
			gapped++
		}
	}
	if gapped == 0 {
		return ""
	}
	return fmt.Sprintf(", %d of them discovered after a contact gap, so their timing is bounded rather than exact", gapped)
}

// newest is the most recent observation time among the entries.
func newest(entries []history.Entry) time.Time {
	var at time.Time
	for _, entry := range entries {
		if entry.ObservedAt.After(at) {
			at = entry.ObservedAt
		}
	}
	return at
}

// HistoryIncarnations matches an object name the timeline has seen
// under several distinct identities inside the window — the same name
// created, destroyed and created again. Counting identities rather than
// change records is what separates a replacement from an edit: a pod
// that is modified stays one object, while a pod that is replaced is a
// new one wearing the old one's name.
type HistoryIncarnations struct {
	// Kind bounds the objects considered; empty considers every kind
	// the timeline records.
	Kind string
	// Count is the number of distinct identities that matches.
	Count int
	// Within is the trailing window.
	Within time.Duration
}

func (c HistoryIncarnations) describe() string {
	subject := "an object"
	if c.Kind != "" {
		subject = "a " + c.Kind
	}
	return fmt.Sprintf("%s replaced at least %d times within %s", subject, c.Count, c.Within)
}

func (c HistoryIncarnations) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if reason := historyUnavailable(in); reason != "" {
		return nil, reason
	}
	names, byName := windowed(in, c.Kind, c.Within)
	var matches []conditionMatch
	for _, name := range names {
		entries := byName[name]
		seen := map[string]bool{}
		for _, entry := range entries {
			if entry.UID != "" {
				seen[entry.UID] = true
			}
		}
		if len(seen) < c.Count {
			continue
		}
		kind := entries[0].Kind
		matches = append(matches, conditionMatch{
			idSuffix: "/" + strings.ToLower(kind) + "/" + name,
			subject:  EntityRef{Kind: kind, Name: name},
			at:       newest(entries),
			summary: fmt.Sprintf("%s %s has been replaced %d times in the last %s.",
				kind, name, len(seen), c.Within),
			evidence: []Evidence{{
				Origin: "console-observed timeline",
				Object: kind + "/" + name,
				Detail: fmt.Sprintf("%d distinct object identities under this name since %s%s",
					len(seen), in.Now.Add(-c.Within).UTC().Format("15:04:05Z"), gapNote(entries)),
			}},
			link:      "/history",
			linkLabel: "History",
		})
	}
	return matches, ""
}

// HistoryChanges matches an object the timeline records changing too
// often inside the window. The changes that count are named by the
// rule, because the kinds mean different things: a definition rewritten
// again and again is two writers disagreeing, while a status moving
// again and again is the operator reconciling.
type HistoryChanges struct {
	// Kind bounds the objects considered; empty considers every kind.
	Kind string
	// Changes are the change classifications that count.
	Changes []history.Change
	// Count is the number of matching records that matches.
	Count int
	// Within is the trailing window.
	Within time.Duration
}

func (c HistoryChanges) describe() string {
	subject := "an object"
	if c.Kind != "" {
		subject = "a " + c.Kind
	}
	kinds := make([]string, len(c.Changes))
	for i, change := range c.Changes {
		kinds[i] = string(change)
	}
	return fmt.Sprintf("%s with at least %d %s records within %s",
		subject, c.Count, strings.Join(kinds, " or "), c.Within)
}

func (c HistoryChanges) evaluate(_ string, in Input) ([]conditionMatch, string) {
	if reason := historyUnavailable(in); reason != "" {
		return nil, reason
	}
	names, byName := windowed(in, c.Kind, c.Within)
	var matches []conditionMatch
	for _, name := range names {
		var counted []history.Entry
		for _, entry := range byName[name] {
			for _, change := range c.Changes {
				if entry.Change == change {
					counted = append(counted, entry)
					break
				}
			}
		}
		if len(counted) < c.Count {
			continue
		}
		kind := counted[0].Kind
		detail := fmt.Sprintf("%d records since %s%s", len(counted),
			in.Now.Add(-c.Within).UTC().Format("15:04:05Z"), gapNote(counted))
		evidence := []Evidence{{
			Origin: "console-observed timeline",
			Object: kind + "/" + name,
			Detail: detail,
		}}
		if actors := actorsOf(counted); actors != "" {
			// The field manager is the API server's own attribution of
			// who wrote, which is the whole answer when the question is
			// what keeps rewriting this.
			evidence = append(evidence, Evidence{
				Origin: "Kubernetes-reported",
				Object: kind + "/" + name,
				Detail: "last-touching field managers: " + actors,
			})
		}
		matches = append(matches, conditionMatch{
			idSuffix:  "/" + strings.ToLower(kind) + "/" + name,
			subject:   EntityRef{Kind: kind, Name: name},
			at:        newest(counted),
			summary:   fmt.Sprintf("%s %s changed %d times in the last %s.", kind, name, len(counted), c.Within),
			evidence:  evidence,
			link:      "/history",
			linkLabel: "History",
		})
	}
	return matches, ""
}

// actorsOf lists the distinct field managers that touched the entries,
// most frequent first, empty when none was attributed.
func actorsOf(entries []history.Entry) string {
	counts := map[string]int{}
	for _, entry := range entries {
		if entry.Actor.Manager != "" {
			counts[entry.Actor.Manager]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	managers := make([]string, 0, len(counts))
	for manager := range counts {
		managers = append(managers, manager)
	}
	sort.Slice(managers, func(a, b int) bool {
		if counts[managers[a]] != counts[managers[b]] {
			return counts[managers[a]] > counts[managers[b]]
		}
		return managers[a] < managers[b]
	})
	parts := make([]string, len(managers))
	for i, manager := range managers {
		parts[i] = fmt.Sprintf("%s (%d)", manager, counts[manager])
	}
	return strings.Join(parts, ", ")
}
