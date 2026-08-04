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
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/observe"
)

// TestStateTokenClassifiesConservatively proves the presentation token
// never promotes an unrecognized or absent state to a healthy one.
func TestStateTokenClassifiesConservatively(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"current":             "current",
		"stale":               "stale",
		"stale: watch broken": "stale",
		"absent: cluster not found in the namespace": "degraded",
		"degraded":             "degraded",
		"unknown: no snapshot": "unknown",
		"":                     "unknown",
		"healthy":              "unknown",
		"CURRENT":              "unknown",
	}
	for in, want := range cases {
		if got := stateToken(in); got != want {
			t.Errorf("stateToken(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStateTokenAccompaniesTheStateWord proves the styling hook is
// redundant reinforcement: wherever a data-state attribute appears the
// state word is still rendered as text, so a reader who never receives
// the stylesheet loses nothing.
func TestStateTokenAccompaniesTheStateWord(t *testing.T) {
	t.Parallel()
	snap := observe.Snapshot{
		Generation: 9,
		ObservedAt: testNow.Add(-150 * time.Second),
		Stale:      true,
		Cluster:    healthyFacts(),
	}
	src := staticSnapshots{
		snap: snap, ok: true,
		pods:   podsSnapshot(true, memberPod("orders-1", "primary")),
		podsOK: true,
	}
	h, _ := newTestHandler(t, src, kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/cluster/pods").Body.String()

	if !strings.Contains(body, `data-state="stale"`) {
		t.Error("stale snapshot carries no presentation token")
	}
	// The word survives alongside the token.
	if !strings.Contains(body, "stale — age 2m30s (generation 9)") {
		t.Error("state word lost when the token was added")
	}
	if strings.Contains(body, `data-state="current"`) {
		t.Error("stale page carries a current token")
	}
}

// TestEnhancementIsAdditive proves the scripts are same-origin
// references with no inline body, that enhancement-only controls are
// cloaked so they never appear when the script does not run, and that
// the panel bodies carry no attribute that would hide them by default.
func TestEnhancementIsAdditive(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, fullPage(), kube.FakeProber{}, Links{})
	body := get(t, h, http.MethodGet, "/cluster/pods").Body.String()

	for _, want := range []string{
		`<script src="/static/console.js" defer></script>`,
		`<script src="/static/htmx-2.0.10.min.js" defer></script>`,
		`<script src="/static/alpine.csp.js" defer></script>`,
		`<script src="/static/history-timeline.js" defer></script>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page misses %q", want)
		}
	}
	// No inline script body may exist: every script tag is immediately
	// closed after its src reference.
	if strings.Contains(body, "</script>") {
		rest := body
		for {
			i := strings.Index(rest, "<script")
			if i < 0 {
				break
			}
			end := strings.Index(rest[i:], "</script>")
			if end < 0 {
				t.Fatal("unterminated script tag")
			}
			tag := rest[i : i+end+len("</script>")]
			if !strings.HasSuffix(tag, `defer></script>`) {
				t.Errorf("script tag carries an inline body: %q", tag)
			}
			rest = rest[i+end:]
		}
	}
	// Every enhancement-only control is cloaked.
	for _, cloaked := range []string{
		`<div class="refresh" x-data="autoRefresh" x-cloak>`,
		`<div class="table-tools" x-cloak>`,
	} {
		if !strings.Contains(body, cloaked) {
			t.Errorf("enhancement control is not cloaked: %q", cloaked)
		}
	}
	// Panel bodies are plain visible markup. The enhancement layer hides
	// them by setting the hidden property at runtime, so the served
	// document carries no gate at all and is complete without a script.
	if !strings.Contains(body, `<div class="panel-body">`) {
		t.Error("panel body is not plain visible markup")
	}
	for _, gate := range []string{
		`class="panel-body" hidden`,
		`class="panel-body" style="display: none`,
	} {
		if strings.Contains(body, gate) {
			t.Errorf("panel body is gated in the served markup: %q", gate)
		}
	}
	// The markup carries no colon-prefixed directive: a strict XML
	// serialiser reads the colon as a namespace prefix and rejects it,
	// so behaviour is wired from the component instead.
	for _, colon := range []string{"x-on:", "x-bind:"} {
		if strings.Contains(body, colon) {
			t.Errorf("markup carries a colon-prefixed directive %q", colon)
		}
	}
}

// TestStaticAssetsAreServed proves the embedded stylesheet and both
// scripts are reachable at the paths the page references, since a
// missing asset would silently degrade the page.
func TestStaticAssetsAreServed(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, EmptySnapshots{}, kube.FakeProber{}, Links{})
	for _, path := range []string{
		"/static/app.css",
		"/static/console.js",
		"/static/htmx-2.0.10.min.js",
		"/static/alpine.csp.js",
		"/static/history-timeline.js",
		"/static/cytoscape-3.34.0.min.js",
		"/static/topology-cytoscape.js",
		"/static/elk-0.12.0.bundled.js",
		"/static/topology-elk.js",
		// Without this the browser requests /favicon.ico on every page
		// load, takes a 404, and logs a console error. img-src 'self'
		// rules out a data: URI, so it has to be a served asset.
		"/static/favicon.svg",
	} {
		rec := get(t, h, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s: served empty", path)
		}
	}
}

// TestEveryPageReferencesTheFavicon proves no rendered page falls back
// to the implicit /favicon.ico request, which the router answers 404.
func TestEveryPageReferencesTheFavicon(t *testing.T) {
	t.Parallel()
	raw, err := assets.ReadFile("templates/shell.html.tmpl")
	if err != nil {
		t.Fatalf("read shared shell: %v", err)
	}
	const want = `<link rel="icon" href="/static/favicon.svg">`
	if !strings.Contains(string(raw), want) {
		t.Error("shared page head does not reference the favicon")
	}
}

// TestEveryPageRendersTheSharedShell proves no page invents its own
// chrome: each one composes the same top bar and section map, so the
// console presents one shell whichever handler answered.
func TestEveryPageRendersTheSharedShell(t *testing.T) {
	t.Parallel()
	entries, err := assets.ReadDir("templates")
	if err != nil {
		t.Fatalf("read templates: %v", err)
	}
	// Partials are composed into a page rather than being one.
	partials := map[string]bool{
		"shell.html.tmpl":              true,
		"topology.html.tmpl":           true,
		"topology-cytoscape.html.tmpl": true,
		"topology-elk.html.tmpl":       true,
	}
	for _, entry := range entries {
		if partials[entry.Name()] {
			continue
		}
		raw, err := assets.ReadFile("templates/" + entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		body := string(raw)
		for _, want := range []string{
			`{{template "page-head"`,
			`{{template "shell-open" .Shell}}`,
			`{{template "shell-close"}}`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s misses %q", entry.Name(), want)
			}
		}
	}
}

// TestSummaryRestatesOnlyWhatThePageCarries holds the first and fourth
// conditions of the overview carve-out in AGENTS.md rule 8: every value
// the summary shows must already appear in the attributed sections
// below it, and the summary must never assert recoverability.
func TestSummaryRestatesOnlyWhatThePageCarries(t *testing.T) {
	t.Parallel()
	status := evidence.Status{HasReport: true, Snapshot: evidence.Snapshot{
		Generation: 2, ObservedAt: testNow.Add(-time.Minute), Report: completeReport(),
	}}
	h := newEvidenceHandler(t, observedCluster(false), status)
	body := get(t, h, http.MethodGet, "/").Body.String()

	page := buildPage(context.Background(), "orders", "payments", snapshots{
		window: time.Hour,
		cluster: observe.Snapshot{
			Generation: 7, ObservedAt: testNow.Add(-3 * time.Second), Cluster: healthyFacts(),
		},
		ok: true,
	}, testNow, Links{})
	if page.Summary == nil {
		t.Fatal("no summary built for a reported cluster")
	}

	// Every card value must be a substring the rendered page already
	// contains — the summary may reword a label, never a value.
	for _, group := range page.Summary.Groups {
		for _, card := range group.Cards {
			if card.Value == "" {
				t.Errorf("card %q has an empty value; absence must render %q", card.Label, unknown)
			}
			if card.Origin == "" {
				t.Errorf("card %q names no origin", card.Label)
			}
		}
	}

	// The rendered summary must not claim recoverability. Neither source
	// reports it, so the console cannot say it.
	lower := strings.ToLower(body)
	for _, forbidden := range []string{"restorable", "can be restored", "restore back to", "verified"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("page asserts recoverability: %q", forbidden)
		}
	}
}

// TestSummaryNeverPresentsAnUnobservedClusterAsHealthy proves the
// headline degrades with the page rather than defaulting to reassurance.
func TestSummaryNeverPresentsAnUnobservedClusterAsHealthy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		page      Page
		wantState string
	}{
		{"no snapshot", buildPage(context.Background(), "orders", "payments", snapshots{window: time.Hour}, testNow, Links{}), unknown},
		{"absent cluster", buildPage(context.Background(), "orders", "payments", snapshots{
			window:  time.Hour,
			cluster: observe.Snapshot{Generation: 2, ObservedAt: testNow, Cluster: observe.ClusterFacts{Present: false}},
			ok:      true,
		}, testNow, Links{}), "degraded"},
		{"stale", buildPage(context.Background(), "orders", "payments", snapshots{
			window: time.Hour,
			cluster: observe.Snapshot{
				Generation: 9, ObservedAt: testNow.Add(-150 * time.Second), Stale: true, Cluster: healthyFacts(),
			},
			ok: true,
		}, testNow, Links{}), "stale"},
	}
	for _, tc := range cases {
		if tc.page.Summary == nil {
			t.Errorf("%s: no summary", tc.name)
			continue
		}
		if got := tc.page.Summary.HeadlineState; got != tc.wantState {
			t.Errorf("%s: headline state = %q, want %q", tc.name, got, tc.wantState)
		}
		if strings.Contains(strings.ToLower(tc.page.Summary.Headline), "everything looks healthy") {
			t.Errorf("%s: headline reassures without grounds: %q", tc.name, tc.page.Summary.Headline)
		}
	}
}

// TestVendoredAlpineIsTheCSPBuild proves the embedded Alpine never
// reaches for new Function or eval, which is what lets the policy omit
// 'unsafe-eval'.
func TestVendoredAlpineIsTheCSPBuild(t *testing.T) {
	t.Parallel()
	raw, err := assets.ReadFile("static/alpine.csp.js")
	if err != nil {
		t.Fatalf("read vendored alpine: %v", err)
	}
	src := string(raw)
	for _, forbidden := range []string{"new Function(", "eval("} {
		if strings.Contains(src, forbidden) {
			t.Errorf("vendored Alpine contains %q; the CSP build must not", forbidden)
		}
	}
}

// TestVendoredHTMXIsPinned makes a dependency update an explicit source
// change rather than an unnoticed replacement of executable browser code.
func TestVendoredHTMXIsPinned(t *testing.T) {
	t.Parallel()
	raw, err := assets.ReadFile("static/htmx-2.0.10.min.js")
	if err != nil {
		t.Fatalf("read vendored htmx: %v", err)
	}
	const want = "71ea67185bfa8c98c39d31717c6fce5d852370fcdfd129db4543774d3145c0de"
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != want {
		t.Fatalf("vendored htmx digest = %s, want %s", got, want)
	}
}

// TestVendoredUPlotIsPinned does the same for the chart library.
func TestVendoredUPlotIsPinned(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]string{
		"static/uplot-1.6.32.min.js":  "19c8d4c6ad88929a79f4ae49d6f7161566dfd0ba3d15cc495e974f787eb78f1f",
		"static/uplot-1.6.32.min.css": "df630c6a8d6f8eeaff264b50f73ce5b114f646ffd9a0bb74f049b0a00135fa04",
	} {
		raw, err := assets.ReadFile(path)
		if err != nil {
			t.Fatalf("read vendored uPlot: %v", err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != want {
			t.Fatalf("vendored %s digest = %s, want %s", path, got, want)
		}
	}
}

// TestVendoredCytoscapeIsPinned does the same for the graph library, and
// asserts the property that lets it run at all: the served
// Content-Security-Policy has no 'unsafe-eval', so a build that reached
// for eval or new Function would be dead code in a browser.
func TestVendoredCytoscapeIsPinned(t *testing.T) {
	t.Parallel()
	raw, err := assets.ReadFile("static/cytoscape-3.34.0.min.js")
	if err != nil {
		t.Fatalf("read vendored Cytoscape: %v", err)
	}
	const want = "9c2a3bf2592e0b14a1f7bec07c03a54f16dedf32af9cd0af155c716aa6c87bc3"
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != want {
		t.Fatalf("vendored Cytoscape digest = %s, want %s", got, want)
	}
	for _, forbidden := range []string{"new Function", "eval("} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Errorf("vendored Cytoscape contains %q, which the served CSP forbids", forbidden)
		}
	}
}

// TestVendoredELKIsPinned does the same for the layout engine, and
// asserts the two properties that let it run at all under the served
// policy: no eval or new Function against 'script-src self', and no
// unconditional Worker against 'default-src none'. ELK reaches for a
// Worker only when the caller passes workerUrl or workerFactory, and
// the console passes neither.
func TestVendoredELKIsPinned(t *testing.T) {
	t.Parallel()
	raw, err := assets.ReadFile("static/elk-0.12.0.bundled.js")
	if err != nil {
		t.Fatalf("read vendored ELK: %v", err)
	}
	const want = "1222e44f953ce7746af23801e723708f8e6f436b8b377a6a5fc7552f34a307b3"
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != want {
		t.Fatalf("vendored ELK digest = %s, want %s", got, want)
	}
	for _, forbidden := range []string{"new Function", "eval("} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Errorf("vendored ELK contains %q, which the served CSP forbids", forbidden)
		}
	}
	// The console never asks for one, so no call site may exist that
	// does not first read a caller-supplied worker option.
	if got := bytes.Count(raw, []byte("new Worker")); got != 2 {
		t.Errorf("vendored ELK has %d Worker call sites, want the 2 gated on workerUrl/workerFactory", got)
	}
}

// The layout engines draw nothing on their own: the ELK panel is the
// console's own SVG, so it has to keep using the stylesheet's classes
// rather than growing a second appearance.
func TestELKPanelDrawsWithTheStylesheetsClasses(t *testing.T) {
	t.Parallel()
	raw, err := assets.ReadFile("static/topology-elk.js")
	if err != nil {
		t.Fatalf("read the ELK drawing layer: %v", err)
	}
	body := string(raw)
	for _, want := range []string{
		"'topo-node topo-'", "'topo-edge topo-edge-'", "'topo-' + row.c",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the ELK panel does not draw with %s", want)
		}
	}
	// Colour belongs to the stylesheet. A literal here would be a second
	// palette that the theme swap cannot reach.
	for _, forbidden := range []string{"fill=", "stroke=", "#0", "rgb("} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the ELK panel sets its own %q instead of leaving colour to the stylesheet", forbidden)
		}
	}
}

// fullPage is a source set that populates every enhanced section, so
// the additive-enhancement assertions see each control.
func fullPage() staticSnapshots {
	return staticSnapshots{
		snap:   observe.Snapshot{Generation: 7, ObservedAt: testNow.Add(-3 * time.Second), Cluster: healthyFacts()},
		ok:     true,
		pods:   podsSnapshot(false, memberPod("orders-1", "primary")),
		podsOK: true,
		events: observe.EventsSnapshot{
			Generation: 4, ObservedAt: testNow.Add(-2 * time.Second),
			Events: []observe.EventFacts{eventFacts("e1", "Pod", "orders-1", "Unhealthy", time.Minute)},
		},
		eventsOK: true,
	}
}
