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

package instancestatus

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

	"github.com/fyannk/pgConsole/internal/observe"
)

const sampleReport = `{
  "currentLsn": "0/6000060", "receivedLsn": "", "replayLsn": "", "systemID": "7411",
  "isPrimary": true, "replayPaused": false, "pendingRestart": true, "pendingRestartForDecrease": false,
  "isWalReceiverActive": false, "isPgRewindRunning": false, "mightBeUnavailable": false,
  "isArchivingWAL": true, "node": "worker-1",
  "lastArchivedWAL": "000000010000000000000005", "lastArchivedWALTime": "2026-09-17T12:00:00.123456Z",
  "lastFailedWAL": "000000010000000000000006", "lastFailedWALTime": "2026-09-17 12:01:00.5+00",
  "currentWAL": "000000010000000000000006", "readyWalFiles": 3, "timeLineID": 1,
  "instanceManagerVersion": "1.30.0", "instanceArch": "amd64",
  "replicationInfo": [{"applicationName": "orders-2", "state": "streaming", "syncState": "async", "syncPriority": "0", "replayLag": "00:00:00.5"}],
  "replicationSlotsInfo": [{"slotName": "_cnpg_orders_2", "slotType": "physical", "active": true, "walStatus": "reserved"},
                           {"slotName": "debezium", "slotType": "logical", "plugin": "pgoutput", "database": "app", "active": false, "walStatus": "reserved"}]
}`

// TestParseReadsTheReportVerbatim proves the subset the console keeps,
// both timestamp forms, and the bounds.
func TestParseReadsTheReportVerbatim(t *testing.T) {
	t.Parallel()
	reading, err := Parse(strings.NewReader(sampleReport))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !reading.IsPrimary || !reading.PendingRestart || reading.ReadyWALFiles != 3 || reading.TimelineID != 1 ||
		reading.InstanceManagerVersion != "1.30.0" || reading.CurrentLSN != "0/6000060" || reading.Node != "worker-1" {
		t.Errorf("scalars wrong: %+v", reading)
	}
	if reading.LastArchivedWALTime == nil || !reading.LastArchivedWALTime.Equal(time.Date(2026, 9, 17, 12, 0, 0, 123456000, time.UTC)) {
		t.Errorf("RFC3339 archive time = %v", reading.LastArchivedWALTime)
	}
	if reading.LastFailedWALTime == nil || !reading.LastFailedWALTime.Equal(time.Date(2026, 9, 17, 12, 1, 0, 500000000, time.UTC)) {
		t.Errorf("PostgreSQL-form failure time = %v", reading.LastFailedWALTime)
	}
	if len(reading.Standbys) != 1 || reading.Standbys[0].ApplicationName != "orders-2" || reading.Standbys[0].ReplayLag != "00:00:00.5" {
		t.Errorf("standbys = %+v", reading.Standbys)
	}
	if len(reading.Slots) != 2 || reading.Slots[1].Name != "debezium" || reading.Slots[1].Active || reading.Slots[1].Plugin != "pgoutput" {
		t.Errorf("slots = %+v", reading.Slots)
	}
	long := strings.Repeat("x", 1000)
	bounded, err := Parse(strings.NewReader(`{"node": "` + long + `", "lastArchivedWALTime": "yesterday"}`))
	if err != nil || len(bounded.Node) != maxText || bounded.LastArchivedWALTime != nil {
		t.Errorf("bounds: node %d, time %v, err %v", len(bounded.Node), bounded.LastArchivedWALTime, err)
	}
	if _, err := Parse(strings.NewReader("not json")); err == nil {
		t.Error("a malformed report parsed")
	}
}

type staticPods struct{ snap observe.PodsSnapshot }

func (s staticPods) CurrentPods() (observe.PodsSnapshot, bool) { return s.snap, true }

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

// TestCollectorKeepsTheLastReportBesideAFailure proves a sweep reads
// every pod with an IP, records a failure per unreachable instance while
// keeping that instance's previous report, forgets an instance that
// left the roster, and logs reachability changes once.
func TestCollectorKeepsTheLastReportBesideAFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(sampleReport))
	}))
	defer srv.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	source := &staticPods{snap: observe.PodsSnapshot{Pods: []observe.PodFacts{
		{Name: "orders-1", IP: host},
		{Name: "orders-2", IP: "203.0.113.1"},
		{Name: "orders-3"},
	}}}
	store := NewStore(10 * time.Second)
	var logs bytes.Buffer
	c := New(source, store, port, &sweepClock{sweeps: 2, now: time.Unix(1700000000, 0)}, slog.New(slog.NewJSONHandler(&logs, nil)))
	c.client.Timeout = 500 * time.Millisecond
	if err := c.Run(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: %v", err)
	}
	snap, ok := store.CurrentInstanceStatus()
	if !ok || snap.Generation != 2 || len(snap.Readings) != 1 || snap.Readings["orders-1"].Instance != "orders-1" {
		t.Fatalf("snapshot = %+v, %v", snap, ok)
	}
	if snap.Failing["orders-2"] == "" {
		t.Errorf("the unreachable instance carries no failure: %+v", snap.Failing)
	}
	if _, listed := snap.Failing["orders-3"]; listed {
		t.Error("a pod without an IP was treated as a failure")
	}
	if got := strings.Count(logs.String(), "instance status read failed"); got != 1 {
		t.Errorf("failure logged %d times across two sweeps, want once", got)
	}

	// The reachable pod stops answering: its last report stays, with
	// the failure beside it; a pod gone from the roster is forgotten.
	srv.Close()
	source.snap.Pods = source.snap.Pods[:1]
	store.publish(time.Unix(1700000100, 0), []string{"orders-1"}, nil, map[string]string{"orders-1": "unavailable"})
	snap, _ = store.CurrentInstanceStatus()
	if snap.Readings["orders-1"].Instance != "orders-1" || snap.Failing["orders-1"] != "unavailable" {
		t.Errorf("last report not kept beside the failure: %+v", snap)
	}
	if _, kept := snap.Readings["orders-2"]; kept {
		t.Error("an instance that left the roster is still listed")
	}
}

// TestCollectorReadsTLSFirstAndFallsBackToPlainHTTP proves the 1.30
// status port, which serves TLS with a certificate the console cannot
// verify, is read, and that a plain-HTTP port is read too.
func TestCollectorReadsTLSFirstAndFallsBackToPlainHTTP(t *testing.T) {
	t.Parallel()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(sampleReport)) })
	for name, srv := range map[string]*httptest.Server{"tls": httptest.NewTLSServer(handler), "plain": httptest.NewServer(handler)} {
		host, port, _ := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(srv.URL, "https://"), "http://"))
		store := NewStore(10 * time.Second)
		c := New(&staticPods{snap: observe.PodsSnapshot{Pods: []observe.PodFacts{{Name: "orders-1", IP: host}}}},
			store, port, &sweepClock{sweeps: 1, now: time.Unix(1700000000, 0)}, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
		_ = c.Run(context.Background())
		snap, _ := store.CurrentInstanceStatus()
		if reading, ok := snap.Readings["orders-1"]; !ok || !reading.IsPrimary {
			t.Errorf("%s: no report read: %+v", name, snap)
		}
		srv.Close()
	}
}
