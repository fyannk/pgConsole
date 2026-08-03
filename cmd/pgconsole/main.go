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

// Command pgconsole serves the per-cluster operational console for one
// CloudNativePG Cluster. It validates its environment completely before
// opening the listener and shuts down gracefully on SIGTERM.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/fyannk/pgConsole/internal/application"
	"github.com/fyannk/pgConsole/internal/config"
	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/history"
	"github.com/fyannk/pgConsole/internal/history/bolt"
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/metrics"
	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run performs the whole lifecycle: validate, assemble, listen, serve.
// Configuration errors are printed as variable names and constraints;
// values never reach any output.
func run() error {
	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Without in-cluster credentials the console still serves: the page
	// stays the explicit unknown shell and readiness reports 503, which
	// is the honest degraded state rather than a crash loop.
	deps := application.Deps{Prober: kube.UnavailableProber{}, Clock: observe.RealClock{}}
	opts := kube.Options{
		Namespace:            cfg.Namespace,
		ClusterName:          cfg.ClusterName,
		RequestTimeout:       cfg.APIRequestTimeout,
		LogTailLines:         cfg.LogTailLines,
		LogTailMaxBytes:      cfg.LogTailMaxBytes,
		AllowClusterCatalogs: cfg.AllowClusterCatalogs,
	}
	// The history store rides the collectors' own watches through the
	// client's capture taps; disabled means no recorder exists and no
	// tap wraps any pump. The Recorder field is set only inside this
	// branch so a disabled history is interface-nil, not a typed nil.
	//
	// With a journal path configured, the store is primed from the file
	// and mirrors into it, and the journal's flush loop joins the
	// background runners. An unusable journal fails before listen: a
	// deployment that mounted one expects history to survive restarts,
	// and half that contract must not half-work.
	if cfg.HistoryEnabled {
		limits := history.Limits{
			PerObject:      cfg.HistoryPerObjectRevisions,
			MaxRevisions:   cfg.HistoryMaxRevisions,
			MaxBytes:       cfg.HistoryMaxBytes,
			CoalesceWindow: cfg.HistoryCoalesceWindow,
		}
		if cfg.HistoryPath != "" {
			journal, err := bolt.Open(cfg.HistoryPath, observe.RealClock{}, logger)
			if err != nil {
				return err
			}
			store, err := history.NewPersistedStore(limits, observe.RealClock{}, journal)
			if err != nil {
				return err
			}
			opts.Recorder = store
			deps.HistorySource = store
			deps.HistoryRunner = journal.Run
		} else {
			store := history.NewStore(limits, observe.RealClock{})
			opts.Recorder = store
			deps.HistorySource = store
		}
	}
	// The metrics window follows the same shape: in-memory by default,
	// and with a snapshot path configured it is primed from the file and
	// rewritten periodically. An unusable snapshot path fails before
	// listen; an unreadable snapshot merely starts the window empty.
	if cfg.MetricsEnabled {
		store := metrics.NewStore(metrics.Limits{
			Interval:  cfg.MetricsInterval,
			Retention: cfg.MetricsRetention,
		})
		deps.Metrics = store
		if cfg.MetricsPath != "" {
			persister, err := metrics.OpenPersister(cfg.MetricsPath, store, observe.RealClock{}, logger)
			if err != nil {
				return err
			}
			deps.MetricsRunner = persister.Run
		}
	}
	client, err := kube.InClusterClient(opts, logger)
	if err != nil {
		logger.Warn("kubernetes access unavailable",
			slog.String("category", redact.Safe(err)))
	} else {
		deps.Source = client
		deps.PodSource = client
		deps.EventSource = client
		deps.BackupSource = client
		deps.PoolerSource = client
		deps.PoolerPodSource = client
		deps.FailoverQuorumSource = client
		deps.ImageCatalogSource = client
		deps.DatabaseObjectsSource = client
		deps.InfrastructureSource = client
		deps.LogTailer = client
		deps.Prober = client.NewProber()
		// The writer is passed only when operations are enabled: in
		// read-only mode the client's mutation methods are never handed
		// to any consumer.
		if cfg.AllowOperations {
			deps.Writer = client
		}
		// The access-review source and decision writer are passed only
		// when the review panel is enabled: otherwise neither the client's
		// request reads nor its status write reach any consumer.
		if cfg.AllowAccessReview {
			deps.AccessReviewSource = client
			deps.AccessReviewWriter = client
		}
	}

	// The evidence consumer follows fail-before-listen: with repository
	// evidence configured, an unreadable or malformed pod-local token is
	// a startup refusal, not a degraded panel — the operator mounted the
	// contract wrong, and half a contract must not half-work.
	if cfg.RepositoryEvidenceEnabled() {
		evidenceClient, err := evidence.NewClient(
			evidence.SocketPathFromURL(cfg.RepositoryEvidenceURL),
			cfg.RepositoryEvidenceTokenFile,
			evidence.Expectation{
				Fingerprint:  cfg.RepositoryExpectedFingerprint,
				BarmanServer: cfg.RepositoryBarmanServer,
				Namespace:    cfg.Namespace,
			})
		if err != nil {
			return err
		}
		deps.EvidenceFetcher = evidenceClient
	}

	app, err := application.New(cfg, deps, logger)
	if err != nil {
		return err
	}
	if err := app.Listen(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return app.Serve(ctx)
}
