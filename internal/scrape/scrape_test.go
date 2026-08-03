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

package scrape

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/metrics"
	"github.com/fyannk/pgConsole/internal/observe"
)

const exporterPayload = `# HELP cnpg_backends_total Number of backends
# TYPE cnpg_backends_total gauge
cnpg_backends_total{datname="app"} 5
cnpg_backends_total{datname="postgres"} 2
# TYPE cnpg_pg_replication_lag gauge
cnpg_pg_replication_lag{stream="a"} 0.5
cnpg_pg_replication_lag{stream="b"} 2.5
cnpg_pg_stat_database_xact_commit{datname="app"} 1000
not a metric line
cnpg_pg_stat_database_blks_hit 42 1700000000000
irrelevant_metric{x="y"} 9
`

func TestParseFoldsTheCatalog(t *testing.T) {
	t.Parallel()
	values, err := Parse(strings.NewReader(exporterPayload))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := values["connections"]; got != 7 {
		t.Errorf("connections = %v, want the label sets summed to 7", got)
	}
	if got := values["replication-lag"]; got != 2.5 {
		t.Errorf("replication-lag = %v, want the worst stream 2.5", got)
	}
	if got := values["xact-commit"]; got != 1000 {
		t.Errorf("xact-commit = %v, want 1000 via the bare candidate name", got)
	}
	if got := values["blocks-hit"]; got != 42 {
		t.Errorf("blocks-hit = %v, want 42 with the timestamp ignored", got)
	}
	if _, ok := values["database-size"]; ok {
		t.Error("an absent metric produced a value")
	}
	if len(values) != 4 {
		t.Errorf("values = %v, want exactly the four served series", values)
	}
}

func TestParseSkipsHostileValues(t *testing.T) {
	t.Parallel()
	values, err := Parse(strings.NewReader("cnpg_backends_total NaN\ncnpg_pg_replication_lag +Inf\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("values = %v, want NaN and Inf refused", values)
	}
}

// staticPods serves a fixed roster.
type staticPods struct{ snap observe.PodsSnapshot }

func (s staticPods) CurrentPods() (observe.PodsSnapshot, bool) { return s.snap, true }

// sweepClock fires the wait immediately a bounded number of times.
type sweepClock struct {
	mu     sync.Mutex
	sweeps int
	now    time.Time
}

func (c *sweepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(10 * time.Second)
	return c.now
}

func (c *sweepClock) Wait(ctx context.Context, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sweeps == 0 {
		return context.Canceled
	}
	c.sweeps--
	return ctx.Err()
}

func testLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

func exporterServer(t *testing.T, payload string) (ip, port string, close func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	host, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	return host, port, srv.Close
}

func TestCollectorSweepsRunningPodsAndLogsOncePerStateChange(t *testing.T) {
	ip, port, closeSrv := exporterServer(t, exporterPayload)
	defer closeSrv()

	source := staticPods{snap: observe.PodsSnapshot{Pods: []observe.PodFacts{
		{Name: "orders-1", IP: ip},
		{Name: "orders-2", IP: "203.0.113.1"}, // unreachable, TEST-NET
		{Name: "orders-3"},                    // no IP yet: skipped silently
	}}}
	store := metrics.NewStore(metrics.Limits{Interval: 10 * time.Second})
	logger, logs := testLogger()
	c := New(source, store, 10*time.Second, &sweepClock{sweeps: 2, now: time.Unix(1700000000, 0)}, logger)
	c.port = port
	c.client.Timeout = 500 * time.Millisecond

	if err := c.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}

	if got := store.Instances(); len(got) != 1 || got[0] != "orders-1" {
		t.Fatalf("instances = %v, want only the reachable orders-1", got)
	}
	times, byInstance := store.Range("connections", metrics.TierRaw)
	if len(times) != 2 || *byInstance["orders-1"][0] != 7 {
		t.Fatalf("stored range = %v %v", times, byInstance)
	}
	// Two failing sweeps for orders-2 log one line, not two.
	if got := strings.Count(logs.String(), "metrics scrape failed"); got != 1 {
		t.Fatalf("failure logged %d times across two sweeps, want once\n%s", got, logs.String())
	}
	if strings.Contains(logs.String(), "orders-3") {
		t.Fatal("a pod without an IP was treated as a failure")
	}
}
