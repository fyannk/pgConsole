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

package application

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/config"
	"github.com/fyannk/pgConsole/internal/kube"
)

// panicWriter fails the test if any mutation is ever attempted. It
// stands in for the writer to prove read-only mode never reaches one.
type panicWriter struct{ t *testing.T }

func (p panicWriter) CreateBackup(context.Context, string) error {
	p.t.Fatal("read-only assembly reached the writer")
	return nil
}
func (p panicWriter) ReloadCluster(context.Context, time.Time) error {
	p.t.Fatal("read-only assembly reached the writer")
	return nil
}
func (p panicWriter) RestartCluster(context.Context, time.Time) error {
	p.t.Fatal("read-only assembly reached the writer")
	return nil
}
func (p panicWriter) PromoteInstance(context.Context, string, time.Time) error {
	p.t.Fatal("read-only assembly reached the writer")
	return nil
}

// opsConfig loads a config with the given ALLOW_OPERATIONS value.
func opsConfig(t *testing.T, allow string) config.Config {
	t.Helper()
	cfg, err := config.Load(func(name string) (string, bool) {
		switch name {
		case config.EnvClusterName:
			return "orders", true
		case config.EnvNamespace:
			return "payments", true
		case config.EnvListenAddr:
			return "127.0.0.1:0", true
		case config.EnvAllowOperations:
			return allow, true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// serveTest assembles, listens, and serves; returns the base URL and a
// stop function.
func serveTest(t *testing.T, cfg config.Config, deps Deps) (string, func()) {
	t.Helper()
	app, err := New(cfg, deps, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx) }()
	return "http://" + app.Addr(), func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}
}

// TestReadOnlyAssemblyHasNoOperationRoute proves that with operations
// disabled, no operation route is registered even though a writer is
// available — the executor is never constructed, so the route does not
// exist and the writer is never reached.
func TestReadOnlyAssemblyHasNoOperationRoute(t *testing.T) {
	t.Parallel()
	base, stop := serveTest(t, opsConfig(t, "false"), Deps{
		Prober: kube.UnavailableProber{},
		Writer: panicWriter{t: t},
	})
	defer stop()
	for _, path := range []string{"/operations", "/operations/restart"} {
		status, _, err := httpGet(t, base+path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if status != http.StatusNotFound {
			t.Errorf("read-only %s = %d, want 404", path, status)
		}
	}
}

// TestOperationsModeRegistersRoutes proves the routes exist once
// operations are enabled with a writer.
func TestOperationsModeRegistersRoutes(t *testing.T) {
	t.Parallel()
	base, stop := serveTest(t, opsConfig(t, "true"), Deps{
		Prober: kube.UnavailableProber{},
		Writer: panicWriter{t: t},
	})
	defer stop()
	// The operation routes require the poweruser level; the default
	// header names are in force, so an authorized request reaches the
	// registered route rather than the level-gate denial.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/operations", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Forwarded-User", "operator")
	req.Header.Set("X-PgToolBox-Level", "dba")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /operations: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("operations index = %d, want 200", resp.StatusCode)
	}
}
