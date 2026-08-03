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

// Package metrics retains a bounded window of instance-reported metric
// samples so the console can show what the exporters claimed over time.
//
// Every value here is a claim by the instance's metrics endpoint, never
// a measurement the console made itself (AGENTS.md rule 8); the store
// records those claims verbatim and derives nothing beyond per-interval
// rates for counters and per-bucket aggregates for the rollup tier. A
// scrape that did not happen leaves a hole in the timestamps, and reads
// surface that hole as a gap rather than interpolating across it.
//
// Bounds are structural, not policed: the series catalog is fixed at
// compile time, instances are capped, and both tiers are ring buffers
// whose capacity follows from the configured interval and retention.
// Memory is therefore bounded by construction — there is nothing to
// evict and no cleanup loop to fall behind.
package metrics

import "time"

// Kind says how a series' samples behave over time.
type Kind int

const (
	// Gauge samples are point-in-time values, stored as reported.
	Gauge Kind = iota
	// Counter samples are cumulative; the store converts them to
	// per-second rates at ingest, so a counter reset produces a gap
	// rather than a negative spike.
	Counter
)

// Aggregate says how label sets of one metric fold into one value.
type Aggregate int

const (
	// Sum adds all label sets, e.g. connections across databases.
	Sum Aggregate = iota
	// Max keeps the largest label set, e.g. the worst replication lag.
	Max
)

// SeriesDef is one curated series. The catalog is the console's entire
// metrics surface: nothing outside it is scraped or stored, which is
// what bounds cardinality.
type SeriesDef struct {
	// Key is the stable identifier used in URLs and storage.
	Key string
	// Title is the plain-language name.
	Title string
	// Unit labels the axis; rates are already per-second.
	Unit string
	// Kind selects gauge or counter semantics.
	Kind Kind
	// Aggregate folds a metric's label sets into one value.
	Aggregate Aggregate
	// Names are the candidate metric names, first match wins; a metric
	// absent from an exporter simply yields no sample.
	Names []string
}

// Catalog is the fixed series allowlist, chosen from the metrics the
// CloudNativePG instance exporter serves by default.
var Catalog = []SeriesDef{
	{Key: "connections", Title: "Connections", Unit: "connections", Kind: Gauge, Aggregate: Sum,
		Names: []string{"cnpg_backends_total"}},
	{Key: "replication-lag", Title: "Replication lag", Unit: "s", Kind: Gauge, Aggregate: Max,
		Names: []string{"cnpg_pg_replication_lag"}},
	{Key: "xact-commit", Title: "Transactions committed", Unit: "tx/s", Kind: Counter, Aggregate: Sum,
		Names: []string{"cnpg_pg_stat_database_xact_commit_total", "cnpg_pg_stat_database_xact_commit"}},
	{Key: "xact-rollback", Title: "Transactions rolled back", Unit: "tx/s", Kind: Counter, Aggregate: Sum,
		Names: []string{"cnpg_pg_stat_database_xact_rollback_total", "cnpg_pg_stat_database_xact_rollback"}},
	{Key: "blocks-hit", Title: "Buffer cache hits", Unit: "blocks/s", Kind: Counter, Aggregate: Sum,
		Names: []string{"cnpg_pg_stat_database_blks_hit_total", "cnpg_pg_stat_database_blks_hit"}},
	{Key: "blocks-read", Title: "Blocks read from disk", Unit: "blocks/s", Kind: Counter, Aggregate: Sum,
		Names: []string{"cnpg_pg_stat_database_blks_read_total", "cnpg_pg_stat_database_blks_read"}},
	{Key: "database-size", Title: "Database size", Unit: "bytes", Kind: Gauge, Aggregate: Sum,
		Names: []string{"cnpg_pg_database_size_bytes"}},
	{Key: "wal-archived", Title: "WAL segments archived", Unit: "segments/s", Kind: Counter, Aggregate: Sum,
		Names: []string{"cnpg_pg_stat_archiver_archived_count_total", "cnpg_pg_stat_archiver_archived_count"}},
}

// SeriesByKey resolves a catalog entry; ok is false for unknown keys.
func SeriesByKey(key string) (SeriesDef, bool) {
	for _, def := range Catalog {
		if def.Key == key {
			return def, true
		}
	}
	return SeriesDef{}, false
}

// Limits bound the store. Zero values take the defaults.
type Limits struct {
	// Interval is the scrape cadence; it sizes the raw tier.
	Interval time.Duration
	// RawWindow is how long raw samples are kept.
	RawWindow time.Duration
	// Retention is how long rollups are kept.
	Retention time.Duration
	// RollupEvery is the rollup bucket width.
	RollupEvery time.Duration
	// MaxInstances caps how many instances are tracked at once.
	MaxInstances int
}

// Default bounds: 10s scrapes, 6h of raw samples, 7d of 5-minute
// rollups, at most 8 instances.
const (
	DefaultInterval    = 10 * time.Second
	DefaultRawWindow   = 6 * time.Hour
	DefaultRetention   = 7 * 24 * time.Hour
	DefaultRollupEvery = 5 * time.Minute
	DefaultInstances   = 8
)

// withDefaults fills unset limits.
func (l Limits) withDefaults() Limits {
	if l.Interval <= 0 {
		l.Interval = DefaultInterval
	}
	if l.RawWindow <= 0 {
		l.RawWindow = DefaultRawWindow
	}
	if l.Retention <= 0 {
		l.Retention = DefaultRetention
	}
	if l.RollupEvery <= 0 {
		l.RollupEvery = DefaultRollupEvery
	}
	if l.MaxInstances <= 0 {
		l.MaxInstances = DefaultInstances
	}
	return l
}

// Point is one read-side sample. A nil Value is a gap: the console did
// not observe a claim there and says so.
type Point struct {
	// At is the sample time, Unix seconds.
	At int64
	// Value is the reported value or derived rate; nil is a gap.
	Value *float64
}

// Stats summarise one instrument over a window; nil fields mean no
// samples were observed.
type Stats struct {
	Latest *float64
	Min    *float64
	Max    *float64
	Avg    *float64
}
