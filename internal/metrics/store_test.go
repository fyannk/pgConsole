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
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

func tick(n int) time.Time { return t0.Add(time.Duration(n) * 10 * time.Second) }

func testStore() *Store {
	return NewStore(Limits{Interval: 10 * time.Second, RawWindow: time.Hour,
		Retention: 24 * time.Hour, RollupEvery: time.Minute, MaxInstances: 3})
}

func TestGaugeRoundTripAndStats(t *testing.T) {
	t.Parallel()
	s := testStore()
	for i, v := range []float64{5, 7, 3} {
		s.Observe("orders-1", tick(i), map[string]float64{"connections": v})
	}
	times, byInstance := s.Range("connections", TierRaw)
	if len(times) != 3 || len(byInstance["orders-1"]) != 3 {
		t.Fatalf("raw range = %d times, %d values", len(times), len(byInstance["orders-1"]))
	}
	if v := byInstance["orders-1"][1]; v == nil || *v != 7 {
		t.Fatalf("middle sample = %v, want 7", v)
	}
	st := s.SeriesStats("connections")["orders-1"]
	if st.Latest == nil || *st.Latest != 3 || *st.Min != 3 || *st.Max != 7 || *st.Avg != 5 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestCounterBecomesRateAndResetIsAGap(t *testing.T) {
	t.Parallel()
	s := testStore()
	// 100 -> 200 over 10s is 10/s; the reset to 50 must not claim a
	// negative rate, and 50 -> 150 resumes at 10/s.
	for i, v := range []float64{100, 200, 50, 150} {
		s.Observe("orders-1", tick(i), map[string]float64{"xact-commit": v})
	}
	times, byInstance := s.Range("xact-commit", TierRaw)
	column := byInstance["orders-1"]
	if len(column) != len(times) {
		t.Fatalf("column misaligned: %d values on %d times", len(column), len(times))
	}
	var rates []float64
	for _, v := range column {
		if v != nil {
			rates = append(rates, *v)
		}
	}
	// First sight and the reset sweep both yield no rate.
	if len(rates) != 2 || rates[0] != 10 || rates[1] != 10 {
		t.Fatalf("rates = %v, want [10 10]", rates)
	}
	for _, v := range rates {
		if v < 0 {
			t.Fatalf("a counter reset produced a negative rate: %v", v)
		}
	}
}

func TestUnsweptSpanReadsAsGap(t *testing.T) {
	t.Parallel()
	s := testStore()
	s.Observe("orders-1", tick(0), map[string]float64{"connections": 5})
	// The console was down for 50 ticks.
	s.Observe("orders-1", tick(50), map[string]float64{"connections": 6})
	times, byInstance := s.Range("connections", TierRaw)
	if len(times) != 3 {
		t.Fatalf("times = %v, want a synthetic break inside the outage", times)
	}
	if byInstance["orders-1"][1] != nil {
		t.Fatal("the outage span carries a value; it must be a gap")
	}
}

func TestRollupAggregatesAndInstanceAlignment(t *testing.T) {
	t.Parallel()
	s := testStore()
	// Twelve sweeps: two 1-minute buckets for orders-1; orders-2 joins
	// for the second bucket only.
	for i := 0; i < 12; i++ {
		values := map[string]float64{"connections": float64(i)}
		s.Observe("orders-1", tick(i), values)
		if i >= 6 {
			s.Observe("orders-2", tick(i), map[string]float64{"connections": 100})
		}
	}
	times, byInstance := s.Range("connections", TierRollup)
	if len(times) != 2 {
		t.Fatalf("rollup times = %v, want 2 buckets", times)
	}
	first := byInstance["orders-1"][0]
	if first == nil || *first != 2.5 {
		t.Fatalf("first bucket avg = %v, want 2.5", first)
	}
	if byInstance["orders-2"][0] != nil {
		t.Fatal("orders-2 has a value in a bucket before it existed")
	}
	if v := byInstance["orders-2"][1]; v == nil || *v != 100 {
		t.Fatalf("orders-2 second bucket = %v, want 100", v)
	}
}

func TestInstanceCapEvictsTheLeastRecentlyObserved(t *testing.T) {
	t.Parallel()
	s := testStore()
	s.Observe("orders-1", tick(0), map[string]float64{"connections": 1})
	s.Observe("orders-2", tick(1), map[string]float64{"connections": 2})
	s.Observe("orders-3", tick(2), map[string]float64{"connections": 3})
	s.Observe("orders-4", tick(3), map[string]float64{"connections": 4})
	got := s.Instances()
	if len(got) != 3 || got[0] != "orders-2" {
		t.Fatalf("instances = %v, want orders-2..4 after evicting orders-1", got)
	}
}

func TestRawRingStaysBounded(t *testing.T) {
	t.Parallel()
	s := NewStore(Limits{Interval: 10 * time.Second, RawWindow: time.Minute,
		Retention: time.Hour, RollupEvery: time.Minute, MaxInstances: 2})
	for i := 0; i < 100; i++ {
		s.Observe("orders-1", tick(i), map[string]float64{"connections": float64(i)})
	}
	times, _ := s.Range("connections", TierRaw)
	if len(times) != 6 {
		t.Fatalf("raw window holds %d samples, want the 6 the capacity allows", len(times))
	}
}

func TestUnknownSeriesKeyIsRefused(t *testing.T) {
	t.Parallel()
	if _, ok := SeriesByKey("nope"); ok {
		t.Fatal("unknown key resolved")
	}
	s := testStore()
	s.Observe("orders-1", tick(0), map[string]float64{"nope": 1})
	if times, _ := s.Range("nope", TierRaw); times != nil {
		t.Fatal("a value outside the catalog was stored")
	}
}
