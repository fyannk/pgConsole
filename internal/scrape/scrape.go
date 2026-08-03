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

// Package scrape polls the instance pods' Prometheus metrics endpoints
// and feeds the curated samples into the metrics store.
//
// Targets come from the pod watch the console already runs — the same
// membership-verified roster every other screen trusts — so scraping
// adds no RBAC and invents no discovery. Each sweep GETs every running
// instance's metrics endpoint, parses only the catalog's allowlisted
// names, and records what the exporter claimed. A pod that cannot be
// reached simply contributes nothing that sweep: the hole reads back as
// a gap, and the failure is logged once per state change rather than
// once per sweep (AGENTS.md rule 6: logs are bounded).
package scrape

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fyannk/pgConsole/internal/metrics"
	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

const (
	// metricsPort is the CloudNativePG instance metrics port.
	metricsPort = "9187"
	// maxBody bounds one scrape response; the default exporter payload
	// is well under this, and anything larger is cut, not buffered.
	maxBody = 2 << 20
	// requestTimeout bounds one scrape request.
	requestTimeout = 5 * time.Second
)

// PodsSource supplies the scrape targets.
type PodsSource interface {
	CurrentPods() (observe.PodsSnapshot, bool)
}

// Collector sweeps the instance metrics endpoints on a fixed cadence.
type Collector struct {
	source   PodsSource
	store    *metrics.Store
	clock    observe.Clock
	logger   *slog.Logger
	interval time.Duration
	client   *http.Client
	// port is the metrics port dialed on every target; fixed to the
	// CloudNativePG default, overridden only by tests.
	port string
	// failing tracks which instances the previous sweeps could not
	// reach, so reachability changes log once instead of every sweep.
	failing map[string]bool
}

// New builds a collector polling at the given interval.
func New(source PodsSource, store *metrics.Store, interval time.Duration, clock observe.Clock, logger *slog.Logger) *Collector {
	return &Collector{
		source:   source,
		store:    store,
		clock:    clock,
		logger:   logger,
		interval: interval,
		client:   &http.Client{Timeout: requestTimeout},
		port:     metricsPort,
		failing:  map[string]bool{},
	}
}

// Run sweeps until the context ends.
func (c *Collector) Run(ctx context.Context) error {
	for {
		if err := c.clock.Wait(ctx, c.interval); err != nil {
			return err
		}
		c.sweep(ctx)
	}
}

// sweep scrapes every running instance once. All samples of one sweep
// share one timestamp, which is what lets the read side align the
// instances on a single time axis.
func (c *Collector) sweep(ctx context.Context) {
	snap, ok := c.source.CurrentPods()
	if !ok {
		return
	}
	at := c.clock.Now()
	for _, pod := range snap.Pods {
		if pod.IP == "" || pod.Deleting {
			continue
		}
		values, err := c.scrapeOne(ctx, pod.IP)
		if err != nil {
			if !c.failing[pod.Name] {
				c.failing[pod.Name] = true
				c.logger.Info("metrics scrape failed",
					slog.String("instance", pod.Name),
					slog.String("category", redact.Safe(err)))
			}
			continue
		}
		if c.failing[pod.Name] {
			delete(c.failing, pod.Name)
			c.logger.Info("metrics scrape recovered", slog.String("instance", pod.Name))
		}
		c.store.Observe(pod.Name, at, values)
	}
}

// scrapeOne fetches and parses one instance's endpoint.
func (c *Collector) scrapeOne(ctx context.Context, ip string) (map[string]float64, error) {
	url := "http://" + net.JoinHostPort(ip, c.port) + "/metrics"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, redact.NewError("metrics scrape", redact.CategoryInternal, err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, redact.NewError("metrics scrape", redact.CategoryUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, redact.NewError("metrics scrape", redact.CategoryUnavailable,
			fmt.Errorf("status %d", resp.StatusCode))
	}
	return Parse(io.LimitReader(resp.Body, maxBody))
}

// Parse reads Prometheus text format and folds it into the catalog: for
// each series the first candidate name that appears wins, and its label
// sets aggregate per the series' rule. Anything outside the catalog is
// skipped unread; malformed lines are skipped rather than failing the
// sweep, so one odd line cannot blind every chart.
func Parse(r io.Reader) (map[string]float64, error) {
	type agg struct {
		value float64
		seen  bool
	}
	// byName folds label sets per metric name as lines stream past.
	byName := map[string]*agg{}
	wanted := map[string]metrics.SeriesDef{}
	for _, def := range Catalog() {
		for _, name := range def.Names {
			wanted[name] = def
		}
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := parseLine(line)
		if !ok {
			continue
		}
		def, want := wanted[name]
		if !want {
			continue
		}
		a := byName[name]
		if a == nil {
			a = &agg{}
			byName[name] = a
		}
		switch {
		case !a.seen:
			a.value, a.seen = value, true
		case def.Aggregate == metrics.Max:
			if value > a.value {
				a.value = value
			}
		default:
			a.value += value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, redact.NewError("metrics parse", redact.CategoryInternal, err)
	}

	out := map[string]float64{}
	for _, def := range Catalog() {
		for _, name := range def.Names {
			if a := byName[name]; a != nil && a.seen {
				out[def.Key] = a.value
				break
			}
		}
	}
	return out, nil
}

// Catalog exposes the metrics catalog to the parser tests without a
// second import path for callers.
func Catalog() []metrics.SeriesDef { return metrics.Catalog }

// parseLine splits one sample line into its metric name and value. The
// optional trailing timestamp is ignored: the sweep time is the
// console's own observation time, which is the claim the store makes.
func parseLine(line string) (name string, value float64, ok bool) {
	rest := line
	if brace := strings.IndexByte(rest, '{'); brace >= 0 {
		name = rest[:brace]
		end := strings.LastIndexByte(rest, '}')
		if end < brace {
			return "", 0, false
		}
		rest = strings.TrimSpace(rest[end+1:])
	} else {
		fields := strings.Fields(rest)
		if len(fields) < 2 {
			return "", 0, false
		}
		name = fields[0]
		rest = strings.Join(fields[1:], " ")
	}
	fields := strings.Fields(rest)
	if len(fields) < 1 {
		return "", 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return "", 0, false
	}
	return name, v, true
}
