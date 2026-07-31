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

//go:build uiharness

// Serves the console over real HTTP so a browser can drive it. This is
// the fixture side of `make test-ui`; hack/test-ui.sh starts it, runs
// hack/uitest/drive.js against it, then signals it to stop.
//
// It exists because the assertions the browser makes — that the
// enhancement layer runs at all under the served Content-Security-Policy,
// that colour contrast clears WCAG AA in both schemes, that a table
// survives a 375px viewport — cannot be made against a rendered string.
// Those are properties of the served page in an engine, so the test has
// to serve the page to an engine. Everything is fixture-driven and
// hermetic: no cluster, no network, the same fixed clock the unit tests
// use.
//
// Build-tagged so it never runs in the normal suite, where an
// indefinitely blocking test would be a hang.
package web

import (
	"bytes"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/observe"
)

// defaultPortBase is the first of the four consecutive ports the
// harness binds. Chosen high to stay clear of anything a developer is
// likely to be running.
const defaultPortBase = 18090

// harnessMaxLifetime bounds the process even if no signal arrives, so a
// wedged driver can never leave a listener behind in CI.
const harnessMaxLifetime = 10 * time.Minute

// uiLinks configures every link-out so the anchors render.
var uiLinks = Links{
	ObjectStoreViewer: "https://viewer.example.com/orders",
	PgAdmin:           "https://pgadmin.example.com",
	Monitoring:        "https://grafana.example.com/d/pg",
}

// uiPopulated is the everything-reported source set, current or stale.
func uiPopulated(stale bool) staticSnapshots {
	facts := healthyFacts()
	facts.UID = "uid-1234"
	started := testNow.Add(-2 * time.Hour)
	stopped := testNow.Add(-90 * time.Minute)
	return staticSnapshots{
		snap: observe.Snapshot{Generation: 7, ObservedAt: testNow.Add(-3 * time.Second), Stale: stale, Cluster: facts},
		ok:   true,
		pods: podsSnapshot(stale,
			memberPod("orders-1", "primary"),
			memberPod("orders-2", "replica"),
			memberPod("orders-3", "replica"),
		),
		podsOK: true,
		events: observe.EventsSnapshot{
			Generation: 4, ObservedAt: testNow.Add(-2 * time.Second), Stale: stale,
			Events: []observe.EventFacts{
				eventFacts("e1", "Pod", "orders-1", "Unhealthy", 4*time.Minute),
				eventFacts("e2", "Pod", "orders-2", "BackOff", 20*time.Minute),
				eventFacts("e3", "Pod", "orders-3", "Killing", 55*time.Minute),
			},
		},
		eventsOK: true,
		backups: observe.BackupsSnapshot{
			Generation: 6, ObservedAt: testNow.Add(-4 * time.Second), Stale: stale,
			Backups: []observe.BackupFacts{
				{Name: "plugin-backup", Phase: "completed", Method: "plugin", CreatedAt: started, StartedAt: &started, StoppedAt: &stopped},
				{Name: "snapshot-backup", Phase: "running", Method: "volumeSnapshot", CreatedAt: testNow.Add(-time.Hour)},
			},
			ScheduledBackups: []observe.ScheduledBackupFacts{
				{Name: "daily", Method: "plugin", Schedule: "0 0 2 * * *", Suspended: boolp(false)},
			},
			ObjectStore: observe.ObjectStoreReference{Name: "orders-store", State: observe.ObjectStorePresent},
		},
		backupsOK: true,
	}
}

// uiHandler builds the fully wired handler for one fixture state: every
// snapshot source, the repository-evidence consumer, all link-outs, and
// the trusted level header.
func uiHandler(t *testing.T, snapshots allSources, status evidence.Status) *Handler {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h, err := New(Config{
		ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour,
		AllowLogs: true, LevelHeader: "X-PgToolBox-Level", Links: uiLinks,
	},
		Sources{Cluster: snapshots, Pods: snapshots, Events: snapshots, Backups: snapshots,
			Evidence: fakeEvidence{status: status}},
		kube.FakeProber{}, fakeTailer{}, Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
		nil, nil, func() time.Time { return testNow }, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// TestUIHarness serves four fixture states on consecutive ports until
// signalled. The states are the ones whose presentation differs: a
// fully reported cluster, the same cluster with every watch broken, the
// same again with the evidence consumer unreachable, and a cold start
// with nothing observed yet.
func TestUIHarness(t *testing.T) {
	base := defaultPortBase
	if v := os.Getenv("PGCONSOLE_UI_PORT_BASE"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("PGCONSOLE_UI_PORT_BASE=%q: %v", v, err)
		}
		base = n
	}

	report := evidence.Status{HasReport: true, Snapshot: evidence.Snapshot{
		Generation: 2, ObservedAt: testNow.Add(-time.Minute), Report: completeReport(),
	}}

	states := []struct {
		name    string
		handler *Handler
	}{
		{"healthy", uiHandler(t, uiPopulated(false), report)},
		{"stale", uiHandler(t, uiPopulated(true), report)},
		{"degraded", uiHandler(t, uiPopulated(true), evidence.Status{Failure: evidence.FailureUnavailable})},
		{"empty", uiHandler(t, EmptySnapshots{}, evidence.Status{})},
	}

	var wg sync.WaitGroup
	servers := make([]*http.Server, 0, len(states))
	for i, state := range states {
		addr := "127.0.0.1:" + strconv.Itoa(base+i)
		srv := &http.Server{
			Addr:              addr,
			Handler:           state.handler.Routes(),
			ReadHeaderTimeout: 5 * time.Second,
		}
		servers = append(servers, srv)
		wg.Add(1)
		go func(s *http.Server, name string) {
			defer wg.Done()
			if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				t.Errorf("serve %s on %s: %v", name, s.Addr, err)
			}
		}(srv, state.name)
		t.Logf("serving %s on http://%s/", state.name, addr)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	select {
	case <-stop:
		t.Log("signalled; shutting down")
	case <-time.After(harnessMaxLifetime):
		t.Errorf("harness reached its %s lifetime without being signalled", harnessMaxLifetime)
	}

	for _, srv := range servers {
		_ = srv.Close()
	}
	wg.Wait()
}
