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
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/history"
)

// timeline builds an input whose object timeline holds the entries.
func timeline(entries ...history.Entry) Input {
	return Input{Now: now, HasHistory: true, History: history.Snapshot{Entries: entries}}
}

// pod is one observation of a pod incarnation.
func pod(name, uid string, minutesAgo int) history.Entry {
	return history.Entry{
		Kind: "Pod", Name: name, UID: uid, Change: history.ChangeCreated,
		ObservedAt: now.Add(-time.Duration(minutesAgo) * time.Minute),
	}
}

// TestHistoryIncarnationsCountsIdentitiesNotEdits proves the
// replacement condition: several edits of one object are one identity
// and no finding, while the same name under several identities inside
// the window is the replacement it reports. Identities outside the
// window do not count, and a timeline that was never recorded is
// could-not-run rather than clear.
func TestHistoryIncarnationsCountsIdentitiesNotEdits(t *testing.T) {
	t.Parallel()
	rule := Rule{ID: "replaced", Summary: "Replaced.", When: HistoryIncarnations{
		Kind: "Pod", Count: 3, Within: time.Hour}}

	if check, _ := evaluateRule(rule, Input{Now: now}); check.Outcome != CheckUnavailable ||
		!strings.Contains(check.Because, "not recorded") {
		t.Errorf("outcome = %v (%q), want could-not-run naming the missing timeline", check.Outcome, check.Because)
	}

	// One incarnation observed three times is one object being edited.
	edits := timeline(pod("orders-1", "uid-a", 40), pod("orders-1", "uid-a", 20), pod("orders-1", "uid-a", 5))
	if check, _ := evaluateRule(rule, edits); check.Outcome != CheckClear {
		t.Errorf("outcome = %v for one identity observed three times, want clear", check.Outcome)
	}

	// Two identities is a replacement, not yet a loop.
	if check, _ := evaluateRule(rule, timeline(pod("orders-1", "uid-a", 40), pod("orders-1", "uid-b", 5))); check.Outcome != CheckClear {
		t.Errorf("outcome = %v below the count, want clear", check.Outcome)
	}

	// Three identities inside the window is the finding.
	loop := timeline(
		pod("orders-1", "uid-a", 50), pod("orders-1", "uid-b", 30), pod("orders-1", "uid-c", 10),
		pod("orders-2", "uid-d", 10),
	)
	check, findings := evaluateRule(rule, loop)
	if check.Outcome != CheckMatched || len(findings) != 1 {
		t.Fatalf("outcome = %v with %d findings, want one on orders-1", check.Outcome, len(findings))
	}
	if findings[0].Subject != (EntityRef{Kind: "Pod", Name: "orders-1"}) ||
		findings[0].ID != "replaced/pod/orders-1" {
		t.Errorf("finding = %s on %+v, want the replaced pod", findings[0].ID, findings[0].Subject)
	}
	if !strings.Contains(findings[0].Summary, "replaced 3 times") ||
		!strings.Contains(findings[0].Evidence[0].Detail, "3 distinct object identities") {
		t.Errorf("finding does not state the count: %+v", findings[0])
	}

	// The third identity falls outside the window, so the window is
	// what decides rather than the timeline's whole length.
	stale := timeline(
		pod("orders-1", "uid-a", 200), pod("orders-1", "uid-b", 30), pod("orders-1", "uid-c", 10))
	if check, _ := evaluateRule(rule, stale); check.Outcome != CheckClear {
		t.Errorf("outcome = %v with one identity outside the window, want clear", check.Outcome)
	}
}

// TestHistoryChangesNamesTheWriters proves the churn condition counts
// only the change kinds the rule names, and quotes the field managers
// the API server attributed the writes to.
func TestHistoryChangesNamesTheWriters(t *testing.T) {
	t.Parallel()
	rule := Rule{ID: "rewritten", Summary: "Rewritten.", When: HistoryChanges{
		Changes: []history.Change{history.ChangeSpec}, Count: 3, Within: time.Hour}}
	entry := func(change history.Change, manager string, minutesAgo int) history.Entry {
		return history.Entry{
			Kind: "Cluster", Name: "orders", UID: "uid-a", Change: change,
			Actor:      history.Actor{Manager: manager},
			ObservedAt: now.Add(-time.Duration(minutesAgo) * time.Minute),
		}
	}

	// Status transitions are not definition rewrites, whatever their
	// number: the rule names which records count.
	statuses := timeline(
		entry(history.ChangeStatus, "cnpg", 30), entry(history.ChangeStatus, "cnpg", 20),
		entry(history.ChangeStatus, "cnpg", 10), entry(history.ChangeStatus, "cnpg", 5))
	if check, _ := evaluateRule(rule, statuses); check.Outcome != CheckClear {
		t.Errorf("outcome = %v counting status transitions, want clear", check.Outcome)
	}

	churn := timeline(
		entry(history.ChangeSpec, "argocd-controller", 40),
		entry(history.ChangeSpec, "mutating-webhook", 30),
		entry(history.ChangeSpec, "argocd-controller", 10),
		entry(history.ChangeStatus, "cnpg", 5))
	check, findings := evaluateRule(rule, churn)
	if check.Outcome != CheckMatched || len(findings) != 1 {
		t.Fatalf("outcome = %v with %d findings, want one", check.Outcome, len(findings))
	}
	if !strings.Contains(findings[0].Summary, "changed 3 times") {
		t.Errorf("summary does not count only the rewrites: %q", findings[0].Summary)
	}
	writers := findings[0].Evidence[1].Detail
	if !strings.Contains(writers, "argocd-controller (2)") || !strings.Contains(writers, "mutating-webhook (1)") {
		t.Errorf("evidence does not name the writers and their shares: %q", writers)
	}
}

// TestTimelineCountsDiscloseAContactGap proves an entry the console
// only discovered after losing contact is counted but flagged, because
// its timing is bounded rather than known.
func TestTimelineCountsDiscloseAContactGap(t *testing.T) {
	t.Parallel()
	rule := Rule{ID: "replaced", Summary: "Replaced.", When: HistoryIncarnations{
		Kind: "Pod", Count: 2, Within: time.Hour}}
	gapped := pod("orders-1", "uid-b", 20)
	gapped.AfterGap = true
	_, findings := evaluateRule(rule, timeline(pod("orders-1", "uid-a", 40), gapped))
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want one", findings)
	}
	if detail := findings[0].Evidence[0].Detail; !strings.Contains(detail, "1 of them discovered after a contact gap") {
		t.Errorf("evidence does not disclose the gap: %q", detail)
	}
}
