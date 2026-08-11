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
	"io"
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

// version is the release identity, stamped at link time with
// -X main.version. An unstamped build reports "dev", which is what a
// local `go build` actually is rather than a placeholder for a version
// it does not have.
var version = "dev"

func main() {
	os.Exit(cli(os.Args[1:], os.Stdout, os.Stderr, run))
}

// cli turns the command line and the outcome of runFn into an exit code.
// It is separate from main so the argument contract can be exercised
// without spawning a process: main hands it the real arguments, streams,
// and run, and does nothing else.
func cli(args []string, stdout, stderr io.Writer, runFn func() error) int {
	// The console is configured entirely by environment, so the only
	// argument it accepts is the one asking what it is. Anything else is
	// refused rather than ignored: a misspelled flag that changes nothing
	// silently is how an operator concludes the setting had no effect.
	// Write errors on the process streams are discarded throughout: the
	// exit code is the real result, and there is nowhere left to report a
	// failure to report.
	if len(args) > 0 {
		switch args[0] {
		case "--version", "-version":
			_, _ = fmt.Fprintln(stdout, version)
			return 0
		default:
			_, _ = fmt.Fprintf(stderr,
				"unknown argument %q: pgconsole is configured by environment variables\n",
				args[0])
			return 2
		}
	}
	if err := runFn(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// run performs the whole lifecycle: validate, assemble, listen, serve.
// Configuration errors are printed as variable names and constraints;
// values never reach any output.
func run() error {
	app, err := build(os.LookupEnv, os.Stdout)
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

// build validates the environment and assembles the application, stopping
// deliberately short of opening the listener. Every fail-before-listen
// refusal lives in here — bad configuration, an unusable history journal,
// an unusable metrics snapshot path, a malformed evidence token — so the
// boundary those comments keep referring to is a real seam a test can
// drive without binding a port.
func build(lookup config.Lookup, logOut io.Writer) (*application.App, error) {
	cfg, err := config.Load(lookup)
	if err != nil {
		return nil, err
	}

	logger := slog.New(slog.NewJSONHandler(logOut, nil))
	// The identity is stated once, before anything can fail. The released
	// image is distroless, so there is no shell to run --version in:
	// `kubectl logs` is the operator's only route to the version a bug
	// report asks for.
	logger.Info("pgconsole starting", slog.String("version", version))

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
				return nil, err
			}
			store, err := history.NewPersistedStore(limits, observe.RealClock{}, journal)
			if err != nil {
				return nil, err
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
		limits := metrics.Limits{
			Interval:  cfg.MetricsInterval,
			Retention: cfg.MetricsRetention,
		}
		store := metrics.NewStore(metrics.Instance, limits)
		deps.Metrics = store
		// The poolers run PgBouncer, not PostgreSQL, and their exporter
		// answers on a different port with a different surface. A second
		// window over the pooler catalog keeps the two from sharing
		// keys, a store, or a screen.
		deps.PoolerMetrics = metrics.NewStore(metrics.Pooler, limits)
		if cfg.MetricsPath != "" {
			persister, err := metrics.OpenPersister(cfg.MetricsPath, store, observe.RealClock{}, logger)
			if err != nil {
				return nil, err
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
		deps.KubeVersionSource = client
		deps.QuotaSource = client
		deps.BackupSource = client
		deps.PoolerSource = client
		deps.PoolerPodSource = client
		deps.FailoverQuorumSource = client
		deps.ImageCatalogSource = client
		deps.DatabaseObjectsSource = client
		deps.InfrastructureSource = client
		deps.LogTailer = client
		// Continuous following reuses the same pods/log grant and the
		// same membership proof as the on-demand tail. Only the opener
		// comes from here; the roster it follows is assembled where the
		// pod store lives.
		deps.LogStreamOpener = client
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
			return nil, err
		}
		deps.EvidenceFetcher = evidenceClient
	}

	return application.New(cfg, deps, logger)
}
