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
	"math"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/history"
	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/metrics"
	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/ops"
	"github.com/fyannk/pgConsole/internal/review"
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

type uiHistoryClock struct{ now time.Time }

func (c *uiHistoryClock) Now() time.Time { return c.now }

func uiHistorySource() *history.Store {
	clock := &uiHistoryClock{now: testNow.Add(-2 * time.Minute)}
	store := history.NewStore(history.Limits{PerObject: 20, MaxRevisions: 100, MaxBytes: 1024 * 1024}, clock)
	base := history.Observation{
		Scope: "cluster", Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster",
		Namespace: "payments", Name: "orders", UID: "cluster-uid", Generation: 4,
		Manifest: []byte(`{"apiVersion":"postgresql.cnpg.io/v1","kind":"Cluster","metadata":{"name":"orders","namespace":"payments"},"spec":{"instances":2}}`),
		SpecHash: "instances-2", StatusHash: "ready", Actor: history.Actor{Manager: "cloudnative-pg", Operation: "Update"},
	}
	store.Observe(base)
	clock.now = testNow.Add(-time.Minute)
	base.Generation = 5
	base.Manifest = []byte(`{"apiVersion":"postgresql.cnpg.io/v1","kind":"Cluster","metadata":{"name":"orders","namespace":"payments"},"spec":{"instances":3}}`)
	base.SpecHash = "instances-3"
	base.Actor = history.Actor{Manager: "gitops", Operation: "Apply"}
	store.Observe(base)
	clock.now = testNow.Add(-30 * time.Second)
	store.Observe(history.Observation{
		Scope: "pods", Group: "", Version: "v1", Kind: "Pod",
		Namespace: "payments", Name: "orders-1", UID: "pod-uid-1",
		Manifest: []byte(`{"apiVersion":"v1","kind":"Pod","metadata":{"name":"orders-1","namespace":"payments","labels":{"cnpg.io/cluster":"orders"}},"spec":{"nodeName":"node-a","containers":[{"name":"postgres","image":"ghcr.io/cloudnative-pg/postgresql:16.4"}]},"status":{"phase":"Running"}}`),
		SpecHash: "pod-spec-1", StatusHash: "running",
		Actor: history.Actor{Manager: "kubelet", Operation: "Update"},
	})
	return store
}

// uiMetricsSource is a deterministic metrics window: an hour of sweeps
// for three instances, with a visible outage gap and a replica whose
// lag moves, so the charts and summaries have a real shape.
func uiMetricsSource() *metrics.Store {
	store := metrics.NewStore(metrics.Instance, metrics.Limits{Interval: 10 * time.Second})
	base := testNow.Add(-time.Hour)
	for i := 0; i < 360; i++ {
		if i >= 200 && i < 230 {
			continue // a console outage: the lines must break here
		}
		at := base.Add(time.Duration(i) * 10 * time.Second)
		phase := float64(i) / 30
		// The primary carries the primary-only families too, so the
		// harness exercises a panel that is populated on one tab and
		// honestly empty on the others.
		store.Observe("orders-1", at, map[string]float64{
			"connections":        12 + 4*math.Sin(phase),
			"xact-commit":        float64(100000 + i*220),
			"xact-rollback":      float64(400 + i*3),
			"blocks-hit":         float64(900000 + i*5100),
			"blocks-read":        float64(12000 + i*40),
			"backends-waiting":   math.Max(0, 3*math.Sin(phase/4)),
			"max-tx-duration":    4 + 2*math.Sin(phase/5),
			"database-size":      2<<30 + float64(i)*4096,
			"wal-bytes":          float64(50 << 20 * i),
			"wal-disk-size":      float64(208 << 20),
			"wal-archive-ready":  math.Max(0, 6*math.Sin(phase/6)),
			"wal-archived":       float64(1200 + i*2),
			"streaming-replicas": 2,
			"xid-age":            float64(120000 + i*40),
		}, map[string]float64{
			"postgres-version":       18.4,
			"instance-up":            1,
			"in-recovery":            0,
			"wal-receiver-up":        0,
			"postmaster-start":       float64(base.Add(-72 * time.Hour).Unix()),
			"nodes-used":             3,
			"sync-replicas-expected": 1,
			"sync-replicas-observed": 1,
			"last-archived-time":     float64(at.Add(-30 * time.Second).Unix()),
			"last-backup":            float64(base.Add(-5 * time.Hour).Unix()),
			"last-failed-backup":     0,
			"wal-segments":           13,
			"slots-active":           2,
		})
		store.Observe("orders-2", at, map[string]float64{
			"connections":     5 + 2*math.Sin(phase/2),
			"replication-lag": 0.2 + 0.15*math.Sin(phase/3),
		}, map[string]float64{
			"postgres-version": 18.4,
			"instance-up":      1,
			"in-recovery":      1,
			"wal-receiver-up":  1,
			"postmaster-start": float64(base.Add(-71 * time.Hour).Unix()),
		})
		store.Observe("orders-3", at, map[string]float64{
			"connections":     4,
			"replication-lag": 0.4 + 0.3*math.Sin(phase/4),
		}, map[string]float64{
			"postgres-version": 18.4,
			"instance-up":      1,
			"in-recovery":      1,
			"wal-receiver-up":  1,
			"postmaster-start": float64(base.Add(-70 * time.Hour).Unix()),
		})
	}
	return store
}

// uiPopulated is the everything-reported source set, current or stale.
func uiPopulated(stale bool) staticSnapshots {
	facts := healthyFacts()
	facts.UID = "uid-1234"
	// The reference lives on the Cluster and the catalog is a separate
	// object, so the fixture must set both for the panel to resolve.
	facts.ImageCatalogRef = &observe.ImageCatalogRef{Kind: "ImageCatalog", Name: "postgres", Major: 16}
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
		poolers: observe.PoolersSnapshot{
			Generation: 3, ObservedAt: testNow.Add(-5 * time.Second), Stale: stale,
			Poolers: []observe.PoolerFacts{
				{
					Name: "orders-rw", UID: "uid-pooler-rw", Type: "rw", PoolMode: "transaction",
					DesiredInstances: int32p(2), ReadyInstances: 2, Phase: "active",
					Image: "ghcr.io/cloudnative-pg/pgbouncer:1.24",
				},
				{
					Name: "orders-ro", UID: "uid-pooler-ro", Type: "ro", PoolMode: "session",
					DesiredInstances: int32p(2), ReadyInstances: 1, Phase: "paused",
					PhaseReason: "holding new client connections while the replicas catch up",
					Image:       "ghcr.io/cloudnative-pg/pgbouncer:1.24",
				},
			},
		},
		poolersOK: true,
		poolerPods: observe.PodsSnapshot{
			Generation: 5, ObservedAt: testNow.Add(-2 * time.Second), Stale: stale,
			Pods: []observe.PodFacts{
				{Name: "orders-rw-abc-1", UID: "pp1", Role: "orders-rw", Phase: "Running",
					Ready: boolp(true), Restarts: intp(0), Node: "node-a", Image: "pgbouncer:1.24"},
				{Name: "orders-rw-abc-2", UID: "pp2", Role: "orders-rw", Phase: "Running",
					Ready: boolp(true), Restarts: intp(0), Node: "node-b", Image: "pgbouncer:1.24"},
				{Name: "orders-ro-def-1", UID: "pp3", Role: "orders-ro", Phase: "Running",
					Ready: boolp(false), Restarts: intp(1), Node: "node-c", Image: "pgbouncer:1.24"},
			},
		},
		poolerPodsOK: true,
		infra: observe.InfrastructureSnapshot{
			Generation: 4, ObservedAt: testNow.Add(-4 * time.Second), Stale: stale,
			Services: []observe.ServiceFacts{
				{Name: "orders-rw", UID: "svc-rw", Role: "read-write", Type: "ClusterIP",
					ClusterIP: "10.96.10.1", Port: int32p(5432),
					TargetSelector: []string{"cnpg.io/cluster=orders", "cnpg.io/instanceRole=primary"}},
				{Name: "orders-ro", UID: "svc-ro", Role: "read-only", Type: "ClusterIP",
					ClusterIP: "10.96.10.2", Port: int32p(5432),
					TargetSelector: []string{"cnpg.io/cluster=orders", "cnpg.io/instanceRole=replica"}},
			},
			Volumes: []observe.VolumeFacts{
				{Name: "orders-1", UID: "vol-1", Instance: "orders-1", Role: "PG_DATA", Phase: "Bound", Capacity: "1Gi", StorageClass: "standard"},
				{Name: "orders-2", UID: "vol-2", Instance: "orders-2", Role: "PG_DATA", Phase: "Bound", Capacity: "1Gi", StorageClass: "standard"},
				{Name: "orders-3", UID: "vol-3", Instance: "orders-3", Role: "PG_DATA", Phase: "Bound", Capacity: "1Gi", StorageClass: "standard"},
			},
			Children: []observe.ChildFacts{
				{Kind: "Secret", Name: "orders-app", UID: "sec-app", SecretType: "kubernetes.io/basic-auth", Keys: intp(2)},
				{Kind: "Secret", Name: "orders-ca", UID: "sec-ca", SecretType: "Opaque", Keys: intp(2)},
				{Kind: "Secret", Name: "orders-server", UID: "sec-server", SecretType: "kubernetes.io/tls", Keys: intp(3)},
				{Kind: "PodDisruptionBudget", Name: "orders", UID: "pdb-1", MinAvailable: "1", DisruptionsAllowed: int32p(1)},
				{Kind: "PodDisruptionBudget", Name: "orders-primary", UID: "pdb-2", MinAvailable: "1", DisruptionsAllowed: int32p(0)},
				{Kind: "ServiceAccount", Name: "orders", UID: "sa-1"},
				{Kind: "Role", Name: "orders", UID: "role-1", Rules: intp(3)},
				{Kind: "RoleBinding", Name: "orders", UID: "rb-1", RoleRef: "orders", Subjects: intp(1)},
			},
		},
		infraOK: true,
		quorum: observe.FailoverQuorumSnapshot{
			Generation: 2, ObservedAt: testNow.Add(-4 * time.Second), Stale: stale,
			Quorum: observe.FailoverQuorumFacts{
				Present: true, Method: "any", Primary: "orders-1",
				StandbyNumber: 1, Standbys: []string{"orders-2", "orders-3"},
			},
		},
		quorumOK: true,
		catalogs: observe.ImageCatalogsSnapshot{
			Generation: 2, ObservedAt: testNow.Add(-6 * time.Second), Stale: stale,
			Catalogs: []observe.ImageCatalogFacts{{
				Name: "postgres", UID: "uid-catalog",
				Images: []observe.CatalogImageFacts{
					{Major: 16, Image: "ghcr.io/cloudnative-pg/postgresql:16.4"},
					{Major: 17, Image: "ghcr.io/cloudnative-pg/postgresql:17.2"},
				},
			}},
		},
		catalogsOK: true,
		declared:   declaredFixture(),
		declaredOK: true,
	}
}

// int32p is the pointer form of an optional reported count.
func int32p(v int32) *int32 { return &v }

// uiAbsent is the observed-but-deleted cluster: the API server answered
// and the Cluster is not there. It is distinct from the cold start —
// absence is a reported fact, not a missing observation — and the
// console must render it as such rather than as an error.
func uiAbsent() staticSnapshots {
	return staticSnapshots{
		snap: observe.Snapshot{
			Generation: 9, ObservedAt: testNow.Add(-3 * time.Second),
			Cluster: observe.ClusterFacts{Present: false},
		},
		ok:        true,
		podsOK:    true,
		eventsOK:  true,
		backupsOK: true,
	}
}

// uiQuiet is the observed-but-empty cluster: every source answered, and
// what it reported is nothing.
//
// It is a different claim from the cold start, and the console words it
// differently — "the operator reports no Poolers" versus "nothing has
// been observed" — so a design that only ever sees one of the two cannot
// tell whether it has covered the other. The cluster itself is healthy,
// because the point of this state is the empty sections around it.
func uiQuiet() staticSnapshots {
	facts := healthyFacts()
	facts.UID = "uid-1234"
	// No catalog reference, so the image-catalog panel shows the
	// cluster naming its image directly.
	facts.ImageCatalogRef = nil
	return staticSnapshots{
		snap:         observe.Snapshot{Generation: 3, ObservedAt: testNow.Add(-3 * time.Second), Cluster: facts},
		ok:           true,
		pods:         observe.PodsSnapshot{Generation: 3, ObservedAt: testNow.Add(-3 * time.Second)},
		podsOK:       true,
		events:       observe.EventsSnapshot{Generation: 3, ObservedAt: testNow.Add(-3 * time.Second)},
		eventsOK:     true,
		backups:      observe.BackupsSnapshot{Generation: 3, ObservedAt: testNow.Add(-3 * time.Second), ObjectStore: observe.ObjectStoreReference{State: observe.ObjectStoreNotReferenced}},
		backupsOK:    true,
		poolers:      observe.PoolersSnapshot{Generation: 3, ObservedAt: testNow.Add(-3 * time.Second)},
		poolersOK:    true,
		poolerPods:   observe.PodsSnapshot{Generation: 3, ObservedAt: testNow.Add(-3 * time.Second)},
		poolerPodsOK: true,
		quorum: observe.FailoverQuorumSnapshot{
			Generation: 3, ObservedAt: testNow.Add(-3 * time.Second),
			Quorum: observe.FailoverQuorumFacts{Present: false},
		},
		quorumOK: true,
		catalogs: observe.ImageCatalogsSnapshot{
			Generation: 3, ObservedAt: testNow.Add(-3 * time.Second),
			ClusterCatalogState: observe.ClusterCatalogNotReferenced,
		},
		catalogsOK: true,
		declared:   observe.DatabaseObjectsSnapshot{Generation: 3, ObservedAt: testNow.Add(-3 * time.Second)},
		declaredOK: true,
	}
}

// uiClusterCatalog is a cluster drawing its image from a cluster-scoped
// ClusterImageCatalog that this deployment did not opt in to read.
//
// It exists because the image-catalog panel has four outcomes and only
// one of them — the resolved namespaced catalog — appears in any other
// fixture. This is the branch that must never read as "the catalog is
// missing" when the truth is "I was not permitted to look", so it is the
// one worth putting in front of a designer.
func uiClusterCatalog() staticSnapshots {
	src := uiPopulated(false)
	facts := src.snap.Cluster
	facts.ImageCatalogRef = &observe.ImageCatalogRef{
		Kind: "ClusterImageCatalog", Name: "shared-postgres", Major: 16,
	}
	src.snap.Cluster = facts
	src.catalogs = observe.ImageCatalogsSnapshot{
		Generation: 2, ObservedAt: testNow.Add(-6 * time.Second),
		ClusterCatalogState: observe.ClusterCatalogDisabled,
	}
	src.catalogsOK = true
	return src
}

// uiMissingCatalog is a cluster naming a namespaced catalog that was not
// observed. It is the fourth and last outcome of the image-catalog
// panel, and the only one where the console does say the catalog is not
// there — because here the API server answered and it was not.
func uiMissingCatalog() staticSnapshots {
	src := uiPopulated(false)
	facts := src.snap.Cluster
	facts.ImageCatalogRef = &observe.ImageCatalogRef{
		Kind: "ImageCatalog", Name: "retired-catalog", Major: 16,
	}
	src.snap.Cluster = facts
	src.catalogs = observe.ImageCatalogsSnapshot{
		Generation: 2, ObservedAt: testNow.Add(-6 * time.Second),
		ClusterCatalogState: observe.ClusterCatalogNotReferenced,
	}
	src.catalogsOK = true
	return src
}

// uiHandler builds the fully wired handler for one fixture state: every
// snapshot source, the repository-evidence consumer, all link-outs, and
// the trusted level header.
//
// authorized selects the capability set. False is the baseline build:
// operations and access review are switched off, so the sidebar carries
// them as inert entries and neither route exists. True wires the
// executor, the reviewer and the access-review source from the ordinary
// unit-test fakes, so every screen the console can serve is reachable.
// Both are real deployments — which is why the harness serves each
// fixture state twice rather than picking one.
func uiHandler(t *testing.T, snapshots allSources, status evidence.Status, authorized bool) *Handler {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	cfg := Config{
		ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour,
		AllowLogs: true, LevelHeader: "X-PgToolBox-Level", Links: uiLinks,
	}
	sources := Sources{Cluster: snapshots, Pods: snapshots, Events: snapshots, Backups: snapshots, Poolers: snapshots, PoolerPods: snapshots, FailoverQuorum: snapshots, ImageCatalogs: snapshots, DatabaseObjects: snapshots, Infrastructure: snapshots,
		Evidence: fakeEvidence{status: status}, History: uiHistorySource(), Metrics: uiMetricsSource()}
	var executor OpsExecutor
	var reviewer ReviewExecutor
	if authorized {
		cfg.AllowOperations = true
		cfg.AllowAccessReview = true
		executor = newRecordingExecutor()
		csrf, err := ops.NewCSRF(reviewClock{})
		if err != nil {
			t.Fatalf("NewCSRF: %v", err)
		}
		reviewer = review.NewExecutor(&reviewWriter{}, csrf, reviewClock{}, logger)
		sources.AccessReview = pendingSnapshot()
	}
	h, err := New(cfg, sources,
		kube.FakeProber{}, fakeTailer{}, Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
		executor, reviewer, func() time.Time { return testNow }, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// TestUIHarness serves the fixture states on consecutive ports until
// signalled. The data states are the ones whose presentation differs: a
// fully reported cluster, the same cluster with every watch broken, the
// same again with the evidence consumer unreachable, and a cold start
// with nothing observed yet.
//
// Each is served twice. The first four ports are the baseline build,
// with operations and access review switched off; hack/uitest/drive.js
// drives those, and its check that an unbuilt destination stays inert
// depends on them staying that way. The next four are the same data
// with every capability wired, which is what hack/design-bundle.sh
// captures — a design bundle should describe the whole console, not the
// subset one deployment enables.
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

	data := []struct {
		name      string
		snapshots allSources
		status    evidence.Status
	}{
		{"healthy", uiPopulated(false), report},
		{"stale", uiPopulated(true), report},
		{"degraded", uiPopulated(true), evidence.Status{Failure: evidence.FailureUnavailable}},
		{"empty", EmptySnapshots{}, evidence.Status{}},
		// Appended, never inserted: drive.js addresses the first four
		// baseline ports by offset.
		{"absent", uiAbsent(), evidence.Status{}},
		// Observed and empty, with a silent evidence sidecar: the
		// wording every "there are none" branch uses, which no other
		// fixture reaches.
		{"quiet", uiQuiet(), evidence.Status{Failure: evidence.FailureUnavailable}},
		{"cluster-catalog", uiClusterCatalog(), report},
		{"missing-catalog", uiMissingCatalog(), report},
	}

	var states []struct {
		name    string
		handler *Handler
	}
	for _, authorized := range []bool{false, true} {
		suffix := ""
		if authorized {
			suffix = "-authorized"
		}
		for _, d := range data {
			states = append(states, struct {
				name    string
				handler *Handler
			}{d.name + suffix, uiHandler(t, d.snapshots, d.status, authorized)})
		}
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
