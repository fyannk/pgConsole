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
	"net/http"
	"sort"
	"time"

	"github.com/fyannk/pgConsole/internal/metrics"
)

// MetricsView is the charts screen. The text beside every chart is the
// complete no-script content: latest, min, max and average per instance
// over the retained window, so the page states its facts before any
// canvas draws.
type MetricsView struct {
	// Shell is the shared chrome.
	Shell ShellView
	// Interval and Retention state the window's shape in words.
	Interval  string
	Retention string
	RawWindow string
	// Series are the catalog panels, in catalog order.
	Series []MetricSeriesView
	// HasSamples is false until the first sweep lands.
	HasSamples bool
}

// MetricSeriesView is one chart panel.
type MetricSeriesView struct {
	Key   string
	Title string
	Unit  string
	// Rows are the per-instance window summaries, sorted by instance.
	Rows []MetricStatRow
	// Data is the uPlot-ready payload for the served raw window, placed
	// in an attribute so contextual escaping stays in charge of it.
	Data string
}

// MetricStatRow is one instance's summary in text.
type MetricStatRow struct {
	Instance string
	Latest   string
	Min      string
	Max      string
	Avg      string
}

// seriesPayload is the JSON shape the chart script consumes, both
// inline and from the poll endpoint. Instances are an ordered list, so
// chart series keep a stable order and colour across refreshes.
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

// handleClusterMetrics renders the charts screen.
func (h *Handler) handleClusterMetrics(w http.ResponseWriter, r *http.Request) {
	view, err := h.buildMetricsView()
	if err != nil {
		h.renderDenied(w, r, http.StatusInternalServerError, "metrics assembly failed")
		return
	}
	view.Shell = h.shell(r, "cluster-metrics")
	h.renderPage(w, "cluster-metrics", "cluster-metrics.html.tmpl", view)
}

// handleMetricsSeries serves one series as JSON for the chart poll. It
// answers only for catalog keys and known windows, so the endpoint can
// never be steered outside the curated surface.
func (h *Handler) handleMetricsSeries(w http.ResponseWriter, r *http.Request) {
	def, ok := metrics.SeriesByKey(r.URL.Query().Get("key"))
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
	payload := h.seriesPayload(def, tier)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		h.logger.Warn("metrics series encode failed")
	}
}

// seriesPayload reads one series from the store into the wire shape.
func (h *Handler) seriesPayload(def metrics.SeriesDef, tier metrics.Tier) seriesPayload {
	times, byInstance := h.sources.Metrics.Range(def.Key, tier)
	payload := seriesPayload{Unit: def.Unit, Times: times}
	names := make([]string, 0, len(byInstance))
	for name := range byInstance {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		payload.Instances = append(payload.Instances, instanceColumn{Name: name, Values: byInstance[name]})
	}
	return payload
}

// buildMetricsView assembles the screen from the store.
func (h *Handler) buildMetricsView() (MetricsView, error) {
	src := h.sources.Metrics
	view := MetricsView{
		Interval:  formatSpan(src.Interval()),
		Retention: formatSpan(src.Retention()),
		RawWindow: formatSpan(metrics.DefaultRawWindow),
	}
	for _, def := range metrics.Catalog {
		panel := MetricSeriesView{Key: def.Key, Title: def.Title, Unit: def.Unit}
		stats := src.SeriesStats(def.Key)
		names := make([]string, 0, len(stats))
		for name := range stats {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			st := stats[name]
			panel.Rows = append(panel.Rows, MetricStatRow{
				Instance: name,
				Latest:   formatMetric(st.Latest, def.Unit),
				Min:      formatMetric(st.Min, def.Unit),
				Max:      formatMetric(st.Max, def.Unit),
				Avg:      formatMetric(st.Avg, def.Unit),
			})
		}
		if len(panel.Rows) > 0 {
			view.HasSamples = true
		}
		raw, err := json.Marshal(h.seriesPayload(def, metrics.TierRaw))
		if err != nil {
			return MetricsView{}, err
		}
		panel.Data = string(raw)
		view.Series = append(view.Series, panel)
	}
	return view, nil
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
