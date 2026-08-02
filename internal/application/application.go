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

// Package application assembles the process: it wires configuration,
// the collector, the web handler, and the hardened HTTP server, and
// owns the listen, serve, and graceful-shutdown lifecycle. Assembly is
// where mode decisions happen; in read-only mode no writer exists in
// the graph.
package application

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/fyannk/pgConsole/internal/config"
	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/ops"
	"github.com/fyannk/pgConsole/internal/redact"
	"github.com/fyannk/pgConsole/internal/review"
	"github.com/fyannk/pgConsole/internal/web"
)

// HTTP server hardening values, shared with the family so both consoles
// present the same edge behavior.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 15 * time.Second
	maxHeaderBytes    = 16 * 1024
)

// Deps are the injected capabilities of the process. Production wiring
// builds them from the in-cluster client; tests inject fakes.
type Deps struct {
	// Source observes the target cluster. Nil disables the collector,
	// leaving the section an explicit unknown.
	Source observe.Source
	// LogTailer performs the bounded on-demand log fetch. Nil renders
	// the log routes unavailable.
	LogTailer web.LogTailer
	// Writer is the mutation transport. It is consumed only in
	// operations mode; in read-only mode it is never referenced, so no
	// mutation path exists in the assembly graph.
	Writer ops.Writer
	// AccessReviewSource observes the namespace's access requests and
	// role names. Nil disables the review collector.
	AccessReviewSource observe.AccessReviewSource
	// AccessReviewWriter records access-request decisions. It is consumed
	// only in access-review mode; otherwise it is never referenced, so no
	// decision-writing path exists in the assembly graph.
	AccessReviewWriter review.Writer
	// PodSource observes the cluster's instance pods. Nil disables the
	// pod collector.
	PodSource observe.PodSource
	// EventSource observes the cluster's candidate events. Nil disables
	// the event collector.
	EventSource observe.EventSource
	// BackupSource observes the target cluster's Backup and
	// ScheduledBackup resources plus its optional ObjectStore reference.
	BackupSource observe.BackupSource
	// PoolerSource observes the target cluster's Pooler resources.
	PoolerSource observe.PoolerSource
	// PoolerPodSource observes the pods run by the cluster's poolers.
	PoolerPodSource observe.PoolerPodSource
	// FailoverQuorumSource observes the cluster's FailoverQuorum.
	FailoverQuorumSource observe.FailoverQuorumSource
	// ImageCatalogSource observes the namespace's ImageCatalog set.
	ImageCatalogSource observe.ImageCatalogSource
	// DatabaseObjectsSource observes the cluster's declared databases,
	// roles, publications and subscriptions.
	DatabaseObjectsSource observe.DatabaseObjectsSource
	// EvidenceFetcher polls the repository-evidence sidecar. Nil means
	// the consumer is disabled: no poller runs, no section renders,
	// and readiness never involves the sidecar.
	EvidenceFetcher evidence.Fetcher
	// HistoryRunner is the durable history journal's background flush
	// loop, owning the journal file's lifecycle. Nil means history is
	// in-memory or disabled and no loop runs.
	HistoryRunner func(ctx context.Context) error
	// HistorySource reads the same bounded store that the Kubernetes watch
	// recorder writes. Nil means history is disabled and no UI route exists.
	HistorySource web.HistorySource
	// Prober answers the readiness endpoint.
	Prober web.ReadinessProber
	// Clock supplies time to the collectors and the page ages.
	Clock observe.Clock
}

// runner is one background loop owned by Serve.
type runner func(ctx context.Context) error

// App is an assembled, not-yet-listening application.
type App struct {
	logger   *slog.Logger
	server   *http.Server
	listener net.Listener
	runners  []runner
	addr     string
}

// New assembles the application from a validated configuration. It does
// not open the listener; Listen and Serve complete the lifecycle, so
// every configuration failure happens strictly before the port opens.
func New(cfg config.Config, deps Deps, logger *slog.Logger) (*App, error) {
	if deps.Clock == nil {
		deps.Clock = observe.RealClock{}
	}

	var runners []runner
	sources := web.Sources{
		Cluster:         web.EmptySnapshots{},
		Pods:            web.EmptySnapshots{},
		Events:          web.EmptySnapshots{},
		Backups:         web.EmptySnapshots{},
		Poolers:         web.EmptySnapshots{},
		PoolerPods:      web.EmptySnapshots{},
		FailoverQuorum:  web.EmptySnapshots{},
		ImageCatalogs:   web.EmptySnapshots{},
		DatabaseObjects: web.EmptySnapshots{},
		History:         deps.HistorySource,
	}
	if deps.Source != nil {
		store := observe.NewStore()
		sources.Cluster = store
		runners = append(runners, observe.NewCollector(deps.Source, store, deps.Clock, logger).Run)
	}
	if deps.PodSource != nil {
		podStore := observe.NewPodStore()
		sources.Pods = podStore
		runners = append(runners, observe.NewPodCollector(deps.PodSource, podStore, deps.Clock, logger).Run)
	}
	if deps.EventSource != nil {
		eventStore := observe.NewEventStore()
		sources.Events = eventStore
		runners = append(runners, observe.NewEventCollector(deps.EventSource, eventStore, cfg.EventsMaxAge, deps.Clock, logger).Run)
	}
	if deps.BackupSource != nil {
		backupStore := observe.NewBackupStore()
		sources.Backups = backupStore
		runners = append(runners, observe.NewBackupCollector(deps.BackupSource, backupStore, deps.Clock, logger).Run)
	}
	if deps.PoolerSource != nil {
		poolerStore := observe.NewPoolerStore()
		sources.Poolers = poolerStore
		runners = append(runners, observe.NewPoolerCollector(deps.PoolerSource, poolerStore, deps.Clock, logger).Run)
	}
	if deps.PoolerPodSource != nil {
		poolerPodStore := observe.NewPoolerPodStore()
		sources.PoolerPods = poolerPodStore
		runners = append(runners, observe.NewPoolerPodCollector(deps.PoolerPodSource, poolerPodStore, deps.Clock, logger).Run)
	}
	if deps.FailoverQuorumSource != nil {
		quorumStore := observe.NewFailoverQuorumStore()
		sources.FailoverQuorum = quorumStore
		runners = append(runners, observe.NewFailoverQuorumCollector(deps.FailoverQuorumSource, quorumStore, deps.Clock, logger).Run)
	}
	if deps.ImageCatalogSource != nil {
		catalogStore := observe.NewImageCatalogStore()
		sources.ImageCatalogs = catalogStore
		runners = append(runners, observe.NewImageCatalogCollector(deps.ImageCatalogSource, catalogStore, deps.Clock, logger).Run)
	}
	if deps.DatabaseObjectsSource != nil {
		declaredStore := observe.NewDatabaseObjectsStore()
		sources.DatabaseObjects = declaredStore
		runners = append(runners, observe.NewDatabaseObjectsCollector(deps.DatabaseObjectsSource, declaredStore, deps.Clock, logger).Run)
	}
	if deps.EvidenceFetcher != nil {
		evidenceStore := evidence.NewStore()
		sources.Evidence = evidenceStore
		runners = append(runners, evidence.NewPoller(deps.EvidenceFetcher, evidenceStore, deps.Clock, logger).Run)
	}
	if deps.HistoryRunner != nil {
		runners = append(runners, deps.HistoryRunner)
	}
	// The access-review collector runs only in review mode with a wired
	// source: disabled mode observes nothing and renders no panel.
	if cfg.AllowAccessReview && deps.AccessReviewSource != nil {
		reviewStore := observe.NewAccessReviewStore()
		sources.AccessReview = reviewStore
		runners = append(runners, observe.NewAccessReviewCollector(deps.AccessReviewSource, reviewStore, deps.Clock, logger).Run)
	}

	// The identity extractor exists whenever a user header is configured —
	// display and audit attribution need it. The authorization level is
	// not decided here: the handler reads it from the trusted level header
	// per request. The console probes no capability.
	auth := web.Auth{}
	if cfg.TrustedUserHeader != "" {
		auth.Extractor = identity.NewExtractor(cfg.TrustedUserHeader)
	}

	// The operations executor — and therefore the mutation writer — is
	// constructed only in operations mode with a wired writer. In
	// read-only mode nothing here references the writer, so the
	// assembly graph contains no mutation path.
	var executor web.OpsExecutor
	if cfg.AllowOperations && deps.Writer != nil {
		csrf, err := ops.NewCSRF(deps.Clock)
		if err != nil {
			return nil, redact.NewError("csrf init", redact.CategoryInternal, err)
		}
		executor = ops.NewExecutor(deps.Writer, csrf, deps.Clock, logger)
	}

	// The review executor — and therefore the decision writer — is
	// constructed only in access-review mode with a wired writer. In
	// every other mode nothing references the writer, so the assembly
	// graph contains no decision-writing path.
	var reviewer web.ReviewExecutor
	if cfg.AllowAccessReview && deps.AccessReviewWriter != nil {
		csrf, err := ops.NewCSRF(deps.Clock)
		if err != nil {
			return nil, redact.NewError("csrf init", redact.CategoryInternal, err)
		}
		reviewer = review.NewExecutor(deps.AccessReviewWriter, csrf, deps.Clock, logger)
	}

	handler, err := web.New(web.Config{
		ClusterName:       cfg.ClusterName,
		Namespace:         cfg.Namespace,
		EventsWindow:      cfg.EventsMaxAge,
		AllowLogs:         cfg.AllowLogs,
		LevelHeader:       cfg.TrustedLevelHeader,
		AllowOperations:   cfg.AllowOperations,
		AllowAccessReview: cfg.AllowAccessReview,
		Links: web.Links{
			ObjectStoreViewer: cfg.ObjectStoreViewerURL,
			PgAdmin:           cfg.PgAdminURL,
			Monitoring:        cfg.MonitoringURL,
		},
	}, sources, deps.Prober, deps.LogTailer, auth, executor, reviewer, deps.Clock.Now, logger)
	if err != nil {
		return nil, err
	}
	return &App{
		logger:  logger,
		runners: runners,
		addr:    cfg.ListenAddr,
		server: &http.Server{
			Handler:           handler.Routes(),
			ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout:       readTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			MaxHeaderBytes:    maxHeaderBytes,
		},
	}, nil
}

// Listen opens the configured address. It is separate from Serve so
// callers observe listen failures synchronously and tests can read the
// bound address before serving.
func (a *App) Listen() error {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", a.addr)
	if err != nil {
		return redact.NewError("listen", redact.CategoryUnavailable, err)
	}
	a.listener = listener
	return nil
}

// Addr returns the bound listen address. It is valid after Listen.
func (a *App) Addr() string {
	if a.listener == nil {
		return ""
	}
	return a.listener.Addr().String()
}

// Serve blocks until the listener fails or ctx asks for shutdown, then
// drains connections and waits for the collector to stop. A clean
// shutdown returns nil.
func (a *App) Serve(ctx context.Context) error {
	if a.listener == nil {
		return redact.NewError("serve", redact.CategoryInternal, errors.New("serve before listen"))
	}
	a.logger.Info("listening", slog.String("addr", a.Addr()))

	runnerCtx, stopRunners := context.WithCancel(ctx)
	defer stopRunners()
	var runnersDone sync.WaitGroup
	for _, run := range a.runners {
		runnersDone.Add(1)
		go func() {
			defer runnersDone.Done()
			_ = run(runnerCtx)
		}()
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- a.server.Serve(a.listener)
	}()

	var result error
	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			result = redact.NewError("serve", redact.CategoryInternal, err)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := a.server.Shutdown(shutdownCtx); err != nil { //nolint:contextcheck // draining must outlive the canceled serve context.
			result = redact.NewError("shutdown", redact.CategoryTimeout, err)
		}
	}

	stopRunners()
	runnersDone.Wait()
	if result == nil {
		a.logger.Info("stopped")
	}
	return result
}
