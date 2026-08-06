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
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fyannk/pgConsole/internal/metrics"
)

// MetricsView is the charts screen: one tab holding every instance's
// lines together, then one tab per instance holding only its own.
//
// The text is the complete no-script content, on every tab. Each series
// panel states latest, min, max and average over the retained window;
// each tile states the latest point-in-time claim and how old it is;
// and every one of them carries its own explanation in a disclosure
// that is ordinary markup, open or closed. The chart is the only part
// that needs script, and it never carries a fact the text does not.
type MetricsView struct {
	// Shell is the shared chrome.
	Shell ShellView
	// Title heads the screen, and SeriesURL is the endpoint its charts
	// fetch from. Both differ per window so one screen cannot draw the
	// other's numbers.
	Title     string
	SeriesURL string
	// PageTitle suffixes the document title. AllTabLabel names the tab
	// holding every pod together, and Origin is the footer's one
	// attribution — all three name the roster, because the screen is
	// the same and the things it reports on are not.
	PageTitle   string
	AllTabLabel string
	Origin      string
	// Producer is what the warning calls the things being scraped.
	Producer string
	// Interval and Retention state the window's shape in words.
	Interval  string
	Retention string
	RawWindow string
	// Tabs are the cluster tab followed by one per instance.
	Tabs []MetricsTabView
	// HasSamples is false until the first sweep lands.
	HasSamples bool
}

// MetricsTabView is one tab: the whole catalog, scoped to one instance
// or to all of them.
type MetricsTabView struct {
	// ID is the tabpanel's element id.
	ID string
	// Label names the tab.
	Label string
	// Instance is the instance this tab is scoped to; empty is the
	// cluster tab, which shows every instance at once.
	Instance string
	// Selected marks the tab the tablist opens on.
	Selected bool
	// Blurb says in one sentence what this tab is scoped to.
	Blurb string
	// Groups are the catalog's sections, in catalog order.
	Groups []MetricGroupView
}

// MetricGroupView is one section of a tab.
type MetricGroupView struct {
	Key    string
	Title  string
	Blurb  string
	Tiles  []MetricTileView
	Panels []MetricSeriesView
}

// MetricNoteView is one instrument's explanation, plus the element id
// the trigger points at. The id has to be unique across the whole
// document, not just the tab: every tab carries the whole catalog, so
// "connections" appears once per instance and a key alone would collide
// three ways.
type MetricNoteView struct {
	ID    string
	Means string
	Why   string
	Watch string
}

// MetricTileView is one point-in-time reading, or several when the tab
// covers every instance.
type MetricTileView struct {
	Key     string
	Title   string
	Unit    string
	Summary string
	Note    MetricNoteView
	// Scope, when set, says which instances report this at all.
	Scope string
	// Readings are the per-instance claims, sorted by instance. A tab
	// scoped to one instance carries exactly one.
	Readings []MetricTileReading
	// Stamp, StampText and Age carry the observation time once for the
	// whole tile, which is the usual case: the readings come from one
	// sweep, so repeating the same timestamp under each of them is three
	// lines saying one thing. They are empty when the readings do not
	// agree on a time, and each reading then states its own.
	Stamp     string
	StampText string
	Age       string
}

// MetricTileReading is one instance's latest claim for one tile.
type MetricTileReading struct {
	Instance string
	// Value is the formatted reading, or "unknown" when the instance has
	// never reported it.
	Value string
	// ValueStamp is set only when the reading is itself a moment in time
	// — the last backup, the postmaster start — so the browser can
	// restate the value, not just its observation time. Empty otherwise,
	// including for a timestamp metric reporting "never".
	ValueStamp string
	// Stamp is the RFC3339 observation time for the <time> element's
	// datetime attribute, so the browser can restate it in the reader's
	// own zone. Empty when nothing was ever observed.
	Stamp string
	// StampText is the same moment as the server states it: UTC, which
	// is what a reader with no script sees and what the toggle returns
	// to. The attribute and the text must always name one instant.
	StampText string
	// Age is how long ago the claim was made.
	Age string
	// Reported is false when the instance has never reported this key,
	// which is a different thing from reporting a zero.
	Reported bool
}

// MetricSeriesView is one chart panel.
type MetricSeriesView struct {
	Key     string
	Title   string
	Unit    string
	Summary string
	Note    MetricNoteView
	// Scope, when set, says which instances report this at all.
	Scope string
	// Instance scopes the chart's fetch; empty means every instance.
	Instance string
	// Rows are the per-instance window summaries, sorted by instance.
	Rows []MetricStatRow
}

// MetricStatRow is one instance's summary in text.
type MetricStatRow struct {
	Instance string
	Latest   string
	Min      string
	Max      string
	Avg      string
}

// seriesPayload is the JSON shape the chart script consumes. Instances
// are an ordered list, so chart series keep a stable order and colour
// across refreshes.
type seriesPayload struct {
	Unit      string           `json:"unit"`
	Times     []int64          `json:"times"`
	Instances []instanceColumn `json:"instances"`
}

// instanceColumn is one instance's aligned values; null is a gap.
type instanceColumn struct {
	Name   string     `json:"name"`
	Values []*float64 `json:"values"`
}

// metricsScreen is one metrics window and the screen that renders it.
// The instances and the poolers get the same screen over different
// windows: same tabs, same tiles, same disclosures — a reader who has
// learnt one has learnt both — but never the same numbers, because the
// two run different software and their catalogs share no key.
type metricsScreen struct {
	source  MetricsSource
	current string
	title   string
	// seriesURL is the endpoint the charts fetch from, which differs
	// per screen so a chart can never be served the other window.
	seriesURL string
	// scopeAll and scopeOne word the tabs for whichever roster this is.
	scopeAll string
	scopeOne string
	pageTitle,
	allTab,
	origin,
	producer string
}

func (h *Handler) clusterMetricsScreen() metricsScreen {
	return metricsScreen{
		source: h.sources.Metrics, current: "cluster-metrics", title: "Metrics",
		seriesURL: "/cluster/metrics/series",
		pageTitle: "cluster metrics", allTab: "All instances",
		producer: "this cluster's instances",
		origin:   "source: instance-reported metrics — every number on this screen is a claim the instance's own metrics endpoint made, recorded verbatim; times are when this pgConsole process scraped them, not when the instance computed them. A sweep that failed is a gap in the line, never a value interpolated across it.",
		scopeAll: "Every instance the console has scraped, drawn together. Lines that should track each other and do not are the reason this tab exists.",
		scopeOne: "Only what %s reported. A panel that is empty here is a metric this instance does not serve — the primary-only ones say so.",
	}
}

func (h *Handler) poolerMetricsScreen() metricsScreen {
	return metricsScreen{
		source: h.sources.PoolerMetrics, current: "poolers-metrics", title: "Pooler metrics",
		seriesURL: "/poolers/metrics/series",
		pageTitle: "pooler metrics", allTab: "All pooler pods",
		producer: "this cluster's pooler pods",
		origin:   "source: pooler-reported metrics — every number on this screen is a claim the PgBouncer exporter running beside each pooler pod made, recorded verbatim; the console never connects to PgBouncer itself. Times are when this pgConsole process scraped them, and a sweep that failed is a gap in the line, never a value interpolated across it.",
		scopeAll: "Every pooler pod the console has scraped, drawn together. A pooler runs more than one pod, and a queue forming on one of them and not the others is the reason this tab exists.",
		scopeOne: "Only what %s reported. Each pod pools independently, so its queue and its server connections are its own.",
	}
}

// handleClusterMetrics renders the instance charts screen.
func (h *Handler) handleClusterMetrics(w http.ResponseWriter, r *http.Request) {
	h.renderMetrics(w, r, h.clusterMetricsScreen())
}

// handlePoolerMetrics renders the same screen over the pooler window.
func (h *Handler) handlePoolerMetrics(w http.ResponseWriter, r *http.Request) {
	h.renderMetrics(w, r, h.poolerMetricsScreen())
}

func (h *Handler) renderMetrics(w http.ResponseWriter, r *http.Request, screen metricsScreen) {
	view, err := h.buildMetricsView(screen)
	if err != nil {
		h.renderDenied(w, r, http.StatusInternalServerError, "metrics assembly failed")
		return
	}
	view.Shell = h.shell(r, screen.current)
	h.renderPage(w, screen.current, "cluster-metrics.html.tmpl", view)
}

// handleMetricsSeries serves one series as JSON for the chart. It
// answers only for catalog keys, known windows and instances the store
// actually tracks, so the endpoint can never be steered outside the
// curated surface.
//
// Charts fetch rather than read an inlined payload. With one catalog
// per tab and a tab per instance the inline form would have put every
// instance's whole raw window into the document several times over —
// megabytes of numbers, most of them for a tab nobody opened. The
// no-script contract is unchanged and is met the same way it always
// was: the window summary is stated in text beside every chart, on
// every tab, before any script runs.
func (h *Handler) handleMetricsSeries(w http.ResponseWriter, r *http.Request) {
	h.serveMetricsSeries(w, r, h.sources.Metrics)
}

// handlePoolerMetricsSeries serves the pooler window on its own route,
// so neither screen can be handed the other's numbers.
func (h *Handler) handlePoolerMetricsSeries(w http.ResponseWriter, r *http.Request) {
	h.serveMetricsSeries(w, r, h.sources.PoolerMetrics)
}

func (h *Handler) serveMetricsSeries(w http.ResponseWriter, r *http.Request, src MetricsSource) {
	if src == nil {
		http.NotFound(w, r)
		return
	}
	def, ok := src.Catalog().SeriesByKey(r.URL.Query().Get("key"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	tier := metrics.TierRaw
	switch r.URL.Query().Get("window") {
	case "", "raw":
	case "retention":
		tier = metrics.TierRollup
	default:
		h.renderDenied(w, r, http.StatusBadRequest, "unknown metrics window")
		return
	}
	instance := r.URL.Query().Get("instance")
	if instance != "" && !metricsTracks(src, instance) {
		http.NotFound(w, r)
		return
	}
	payload := seriesPayload2(src, def, tier, instance)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		h.logger.Warn("metrics series encode failed")
	}
}

// metricsTracks reports whether the store has ever observed the named
// instance, which is the only instance name the series endpoint serves.
func metricsTracks(src MetricsSource, instance string) bool {
	for _, name := range src.Instances() {
		if name == instance {
			return true
		}
	}
	return false
}

// seriesPayload reads one series from the store into the wire shape,
// optionally narrowed to one instance.
func seriesPayload2(src MetricsSource, def metrics.SeriesDef, tier metrics.Tier, instance string) seriesPayload {
	times, byInstance := src.Range(def.Key, tier)
	payload := seriesPayload{Unit: def.Unit, Times: times}
	names := make([]string, 0, len(byInstance))
	for name := range byInstance {
		if instance != "" && name != instance {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		payload.Instances = append(payload.Instances, instanceColumn{Name: name, Values: byInstance[name]})
	}
	// A per-instance read that matched nothing still carries the shared
	// time axis; dropping the axis too would make the chart script treat
	// an instance with no samples the same as a store with none.
	if instance != "" && len(payload.Instances) == 0 {
		payload.Times = nil
	}
	return payload
}

// noteView carries one catalog explanation into the view, giving it a
// document-unique element id built from the tab it renders on.
func noteView(tab, key string, note metrics.Note) MetricNoteView {
	return MetricNoteView{
		ID:    "note-" + tab + "-" + key,
		Means: note.Means,
		Why:   note.Why,
		Watch: note.Watch,
	}
}

// scopeNote turns a catalog scope into the sentence the panel shows, so
// an empty replica panel reads as "only the primary reports this"
// rather than as a fault.
func scopeNote(scope metrics.Scope) string {
	if scope == metrics.ScopePrimary {
		return "Only the instance that is not in recovery reports this: it is a view on work only a primary does. On a replica the panel is empty, and that is correct."
	}
	return ""
}

// buildMetricsView assembles the screen from the store: one cluster tab
// plus one tab per tracked instance, each carrying the whole catalog.
func (h *Handler) buildMetricsView(screen metricsScreen) (MetricsView, error) {
	src := screen.source
	if src == nil {
		return MetricsView{}, nil
	}
	view := MetricsView{
		Title:       screen.title,
		SeriesURL:   screen.seriesURL,
		PageTitle:   screen.pageTitle,
		AllTabLabel: screen.allTab,
		Origin:      screen.origin,
		Producer:    screen.producer,
		Interval:    formatSpan(src.Interval()),
		Retention:   formatSpan(src.Retention()),
		RawWindow:   formatSpan(metrics.DefaultRawWindow),
	}

	instances := src.Instances()
	// Read the store once per screen rather than once per tab: stats and
	// instants are both whole-store reads, and re-reading them per tab
	// would let two tabs of one page disagree about the same sweep.
	catalog := src.Catalog()
	stats := make(map[string]map[string]metrics.Stats, len(catalog.Series))
	for _, def := range catalog.Series {
		byInstance := src.SeriesStats(def.Key)
		if len(byInstance) > 0 {
			view.HasSamples = true
		}
		stats[def.Key] = byInstance
	}
	readings := src.InstantReadings()
	if len(readings) > 0 {
		view.HasSamples = true
	}
	now := h.now()

	view.Tabs = append(view.Tabs, h.buildMetricsTab("", instances, stats, readings, catalog, screen, now))
	for _, name := range instances {
		view.Tabs = append(view.Tabs, h.buildMetricsTab(name, []string{name}, stats, readings, catalog, screen, now))
	}
	if len(view.Tabs) > 0 {
		view.Tabs[0].Selected = true
	}
	return view, nil
}

// buildMetricsTab assembles one tab. scope is the instances whose rows
// and readings this tab shows; instance is empty on the cluster tab.
func (h *Handler) buildMetricsTab(
	instance string,
	scope []string,
	stats map[string]map[string]metrics.Stats,
	readings map[string]map[string]metrics.Instant,
	catalog metrics.Catalog,
	screen metricsScreen,
	now time.Time,
) MetricsTabView {
	tab := MetricsTabView{Instance: instance}
	if instance == "" {
		tab.ID, tab.Label = "metrics-cluster", screen.allTab
		_ = screen.scopeAll // the all-pods tab leads with the data, not a blurb
	} else {
		tab.ID, tab.Label = "metrics-pod-"+metricsTabID(instance), instance
		tab.Blurb = fmt.Sprintf(screen.scopeOne, instance)
	}

	inScope := make(map[string]bool, len(scope))
	for _, name := range scope {
		inScope[name] = true
	}

	for _, group := range catalog.Groups {
		view := MetricGroupView{Key: group.Key, Title: group.Title, Blurb: group.Blurb}

		for _, def := range catalog.Instants {
			if def.Group != group.Key {
				continue
			}
			tile := MetricTileView{
				Key: def.Key, Title: def.Title, Unit: def.Unit, Summary: def.Summary,
				Note:  noteView(tab.ID, def.Key, def.Note),
				Scope: scopeNote(def.Scope),
			}
			shared, uniform := int64(0), true
			for _, name := range scope {
				reading := MetricTileReading{Instance: name, Value: unknown}
				if instant, ok := readings[name][def.Key]; ok {
					reading.Value = formatInstant(def, instant.Value)
					reading.Reported = true
					if def.Render == metrics.RenderTimestamp && instant.Value > 0 {
						reading.ValueStamp = time.Unix(int64(instant.Value), 0).UTC().Format(time.RFC3339)
					}
					at := time.Unix(instant.At, 0)
					reading.Stamp = at.UTC().Format(time.RFC3339)
					reading.StampText = at.UTC().Format("2006-01-02 15:04:05Z")
					reading.Age = formatAge(now.Sub(at))
					switch {
					case shared == 0:
						shared = instant.At
					case shared != instant.At:
						uniform = false
					}
				}
				tile.Readings = append(tile.Readings, reading)
			}
			if uniform && shared != 0 {
				at := time.Unix(shared, 0)
				tile.Stamp = at.UTC().Format(time.RFC3339)
				tile.StampText = at.UTC().Format("2006-01-02 15:04:05Z")
				tile.Age = formatAge(now.Sub(at))
				for i := range tile.Readings {
					tile.Readings[i].Stamp, tile.Readings[i].StampText, tile.Readings[i].Age = "", "", ""
				}
			}
			view.Tiles = append(view.Tiles, tile)
		}

		for _, def := range catalog.Series {
			if def.Group != group.Key {
				continue
			}
			panel := MetricSeriesView{
				Key: def.Key, Title: def.Title, Unit: def.Unit, Summary: def.Summary,
				Note:     noteView(tab.ID, def.Key, def.Note),
				Scope:    scopeNote(def.Scope),
				Instance: instance,
			}
			byInstance := stats[def.Key]
			names := make([]string, 0, len(byInstance))
			for name := range byInstance {
				if inScope[name] {
					names = append(names, name)
				}
			}
			sort.Strings(names)
			for _, name := range names {
				st := byInstance[name]
				panel.Rows = append(panel.Rows, MetricStatRow{
					Instance: name,
					Latest:   formatMetric(st.Latest, def.Unit),
					Min:      formatMetric(st.Min, def.Unit),
					Max:      formatMetric(st.Max, def.Unit),
					Avg:      formatMetric(st.Avg, def.Unit),
				})
			}
			view.Panels = append(view.Panels, panel)
		}

		tab.Groups = append(tab.Groups, view)
	}
	return tab
}

// formatInstant renders one point-in-time claim per its definition. The
// distinction the Render kinds exist for is that every value arriving
// here is a float64: a unix timestamp, a page count and a "1 if fenced"
// flag are indistinguishable without the definition that named them.
func formatInstant(def metrics.InstantDef, value float64) string {
	switch def.Render {
	case metrics.RenderWords:
		if value == 0 {
			return def.Words[0]
		}
		return def.Words[1]
	case metrics.RenderBytes:
		return formatBytes(value)
	case metrics.RenderSeconds:
		return formatSeconds(value)
	case metrics.RenderTimestamp:
		if value <= 0 {
			if def.ZeroMeansNever {
				return "never reported"
			}
			return unknown
		}
		return time.Unix(int64(value), 0).UTC().Format("2006-01-02 15:04:05Z")
	case metrics.RenderVersion:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return fmt.Sprintf("%.4g", value)
	}
}

// formatSeconds renders a duration reported in seconds in the largest
// unit that keeps it readable.
func formatSeconds(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return unknown
	}
	switch {
	case v < 1:
		return fmt.Sprintf("%.0f ms", v*1000)
	case v < 90:
		return fmt.Sprintf("%.1f s", v)
	case v < 5400:
		return fmt.Sprintf("%.0fm %02ds", math.Floor(v/60), int(v)%60)
	default:
		return fmt.Sprintf("%.0fh %02dm", math.Floor(v/3600), int(v)%3600/60)
	}
}

// formatMetric renders one summary value; nil is "unknown", bytes are
// humanised, everything else keeps four significant digits.
func formatMetric(v *float64, unit string) string {
	if v == nil {
		return unknown
	}
	if unit == "bytes" {
		return formatBytes(*v)
	}
	if unit == "bytes/s" {
		return formatBytes(*v) + "/s"
	}
	return fmt.Sprintf("%.4g", *v)
}

// formatBytes humanises a byte count in binary units.
func formatBytes(v float64) string {
	const unit = 1024.0
	for _, suffix := range []string{"B", "KiB", "MiB", "GiB", "TiB"} {
		if v < unit || suffix == "TiB" {
			if suffix == "B" {
				return fmt.Sprintf("%.0f %s", v, suffix)
			}
			return fmt.Sprintf("%.1f %s", v, suffix)
		}
		v /= unit
	}
	return ""
}

// formatSpan renders a configuration duration in the largest whole
// unit that fits, so "168h0m0s" reads as "7d".
func formatSpan(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return fmt.Sprintf("%ds", d/time.Second)
	}
}

// metricsTabID sanitises an instance name into an element id fragment.
// Instance names are Kubernetes object names, so they are already
// lowercase alphanumerics and dashes; this is belt and braces against a
// name that is not, which would otherwise produce a broken id.
func metricsTabID(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		default:
			return '-'
		}
	}, strings.ToLower(name))
}
