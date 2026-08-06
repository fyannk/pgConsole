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

package metrics

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"time"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

// Metrics persistence is a periodic snapshot, not a journal: the whole
// bounded window is written atomically (temp file, fsync, rename) once
// a minute and once at shutdown, and read back at startup. The trade is
// stated: a hard kill loses at most the last minute of samples — which
// the next read then shows as a gap, like any other unswept span. The
// history journal made the opposite trade (a 2s write-behind of
// deltas) because a revision timeline is a record; this window is a
// bounded cache of exporter claims, and one minute of it is worth less
// than the journal machinery it would take to keep.
//
// The counter baselines are deliberately not persisted: after a
// restart the first sweep of a counter series yields no rate, so the
// downtime reads as a gap instead of a rate averaged across it.

// snapshotVersion gates the gob payload. A mismatched snapshot is
// discarded with a logged warning rather than failing startup: unlike
// the history journal this file is a bounded cache, and starting it
// empty is the same honest state as first boot.
const snapshotVersion = 1

// flushEvery is the snapshot cadence.
const flushEvery = time.Minute

// PersistedState is the serialized window.
type PersistedState struct {
	// Version gates the layout.
	Version int
	// SavedAt is when the snapshot was taken, Unix seconds.
	SavedAt int64
	// Instruments are every (instance, series) window.
	Instruments []PersistedInstrument
}

// PersistedInstrument is one (instance, series) window.
type PersistedInstrument struct {
	Instance string
	Series   string
	LastAt   int64
	Raw      []PersistedSample
	Rollups  []PersistedBucket
	Open     PersistedBucket
}

// PersistedSample is one raw sample.
type PersistedSample struct {
	At  int64
	Val float64
}

// PersistedBucket is one rollup aggregate; Count 0 means none.
type PersistedBucket struct {
	Start         int64
	Min, Max, Sum float64
	Count         int
}

func exportBucket(b bucket) PersistedBucket {
	return PersistedBucket{Start: b.start, Min: b.min, Max: b.max, Sum: b.sum, Count: b.count}
}

func importBucket(b PersistedBucket) bucket {
	return bucket{start: b.Start, min: b.Min, max: b.Max, sum: b.Sum, count: b.Count}
}

// Export copies the retained window out of the store.
func (s *Store) Export(at time.Time) PersistedState {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := PersistedState{Version: snapshotVersion, SavedAt: at.Unix()}
	for instance, inst := range s.instances {
		for key, ins := range inst.byKey {
			p := PersistedInstrument{Instance: instance, Series: key, LastAt: inst.lastAt,
				Open: exportBucket(ins.open)}
			ins.raw.each(func(v sample) {
				p.Raw = append(p.Raw, PersistedSample{At: v.at, Val: v.val})
			})
			ins.roll.each(func(b bucket) {
				p.Rollups = append(p.Rollups, exportBucket(b))
			})
			state.Instruments = append(state.Instruments, p)
		}
	}
	sort.Slice(state.Instruments, func(a, b int) bool {
		if state.Instruments[a].Instance != state.Instruments[b].Instance {
			return state.Instruments[a].Instance < state.Instruments[b].Instance
		}
		return state.Instruments[a].Series < state.Instruments[b].Series
	})
	return state
}

// Import rebuilds the window from a snapshot, clamped to the store's
// current bounds: rings keep their configured capacities, series
// outside the catalog are dropped, and when the snapshot carries more
// instances than the cap the most recently observed win. Counter
// baselines start empty, so the first post-restart sweep of a counter
// series claims nothing.
func (s *Store) Import(state PersistedState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lastByInstance := map[string]int64{}
	for _, p := range state.Instruments {
		if p.LastAt > lastByInstance[p.Instance] {
			lastByInstance[p.Instance] = p.LastAt
		}
	}
	names := make([]string, 0, len(lastByInstance))
	for name := range lastByInstance {
		names = append(names, name)
	}
	sort.Slice(names, func(a, b int) bool { return lastByInstance[names[a]] > lastByInstance[names[b]] })
	if len(names) > s.limits.MaxInstances {
		names = names[:s.limits.MaxInstances]
	}
	keep := map[string]bool{}
	for _, name := range names {
		keep[name] = true
	}

	for _, p := range state.Instruments {
		if !keep[p.Instance] {
			continue
		}
		if _, ok := s.catalog.SeriesByKey(p.Series); !ok {
			continue
		}
		inst := s.instances[p.Instance]
		if inst == nil {
			inst = &instanceSeries{byKey: map[string]*instrument{}}
			s.instances[p.Instance] = inst
		}
		if p.LastAt > inst.lastAt {
			inst.lastAt = p.LastAt
		}
		ins := &instrument{
			raw:  newRing[sample](int(s.limits.RawWindow / s.limits.Interval)),
			roll: newRing[bucket](int(s.limits.Retention / s.limits.RollupEvery)),
			open: importBucket(p.Open),
		}
		for _, v := range p.Raw {
			ins.raw.push(sample{at: v.At, val: v.Val})
		}
		for _, b := range p.Rollups {
			ins.roll.push(importBucket(b))
		}
		inst.byKey[p.Series] = ins
	}
}

// Persister owns the snapshot file: it primes the store at startup and
// rewrites the file on a fixed cadence plus once at shutdown.
type Persister struct {
	store  *Store
	path   string
	clock  observe.Clock
	logger *slog.Logger
}

// OpenPersister loads any existing snapshot into the store and proves
// the path writable by taking one snapshot immediately. An unreadable
// mount or unwritable path fails here, before the listener: the
// deployment mounted a persistence contract and half of it must not
// half-work. A snapshot that exists but does not decode is different —
// it is discarded with a warning, because an empty window is the same
// honest state as first boot.
func OpenPersister(path string, store *Store, clock observe.Clock, logger *slog.Logger) (*Persister, error) {
	p := &Persister{store: store, path: path, clock: clock, logger: logger}

	raw, err := os.ReadFile(path) //nolint:gosec // the path is the validated METRICS_PATH configuration, same trust as the history journal's.
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// First boot on this volume.
	case err != nil:
		return nil, redact.NewError("metrics snapshot read", redact.CategoryInternal, err)
	default:
		var state PersistedState
		if decodeErr := gob.NewDecoder(bytes.NewReader(raw)).Decode(&state); decodeErr != nil || state.Version != snapshotVersion {
			logger.Warn("metrics snapshot unreadable; starting empty",
				slog.Int("version", state.Version))
		} else {
			store.Import(state)
		}
	}

	if err := p.flush(); err != nil {
		return nil, err
	}
	return p, nil
}

// Run snapshots until the context ends, then takes the final snapshot.
func (p *Persister) Run(ctx context.Context) error {
	for {
		if err := p.clock.Wait(ctx, flushEvery); err != nil {
			if flushErr := p.flush(); flushErr != nil {
				p.logger.Warn("metrics snapshot final flush failed",
					slog.String("category", redact.Safe(flushErr)))
			}
			return err
		}
		if err := p.flush(); err != nil {
			p.logger.Warn("metrics snapshot flush failed",
				slog.String("category", redact.Safe(err)))
		}
	}
}

// flush writes one snapshot atomically: temp file in the same
// directory, fsync, rename over the previous snapshot.
func (p *Persister) flush() error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(p.store.Export(p.clock.Now())); err != nil {
		return redact.NewError("metrics snapshot encode", redact.CategoryInternal, err)
	}
	tmp := p.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the path derives from the validated METRICS_PATH configuration.
	if err != nil {
		return redact.NewError("metrics snapshot write", redact.CategoryInternal, err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return redact.NewError("metrics snapshot write", redact.CategoryInternal, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return redact.NewError("metrics snapshot sync", redact.CategoryInternal, err)
	}
	if err := f.Close(); err != nil {
		return redact.NewError("metrics snapshot close", redact.CategoryInternal, err)
	}
	if err := os.Rename(tmp, p.path); err != nil {
		return redact.NewError("metrics snapshot rename", redact.CategoryInternal, err)
	}
	return nil
}
