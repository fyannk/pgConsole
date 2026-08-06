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
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/fyannk/pgConsole/internal/config"
	"github.com/fyannk/pgConsole/internal/kube"
)

// testConfig returns a valid configuration bound to an ephemeral port.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(func(name string) (string, bool) {
		switch name {
		case config.EnvClusterName:
			return "orders", true
		case config.EnvNamespace:
			return "payments", true
		case config.EnvListenAddr:
			return "127.0.0.1:0", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// start assembles, listens, and serves the application, returning its
// base URL and a stop function that asserts a clean shutdown. The
// dependency set has no source: the page stays an unknown shell and the
// prober reports unavailable.
func start(t *testing.T, logs *bytes.Buffer) (string, func()) {
	t.Helper()
	app, err := New(testConfig(t), Deps{Prober: kube.UnavailableProber{}}, slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx) }()
	stop := func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}
	return "http://" + app.Addr(), stop
}

// httpGet performs a context-carrying GET and returns the status and
// body, closing the response.
// httpGet requests as a dba. Every screen is decided by the forwarded
// level, so an assembly test asking what a screen renders has to send
// one; the admission ladder itself is tested in internal/web.
func httpGet(t *testing.T, url string) (int, string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("X-PgToolBox-Level", "dba")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(body), nil
}

func TestHandlerServesOverRealListener(t *testing.T) {
	t.Parallel()
	logs := &bytes.Buffer{}
	base, stop := start(t, logs)
	defer stop()

	status, body, err := httpGet(t, base+"/healthz")
	if err != nil {
		t.Fatalf("GET healthz: %v", err)
	}
	if status != http.StatusOK || body != "ok\n" {
		t.Fatalf("healthz = %d %q", status, body)
	}

	status, _, err = httpGet(t, base+"/readyz")
	if err != nil {
		t.Fatalf("GET readyz: %v", err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503 without an API probe", status)
	}
}

// TestGracefulShutdown proves context cancellation drains the server and
// Serve returns nil, which is the SIGTERM path of the process.
func TestGracefulShutdown(t *testing.T) {
	t.Parallel()
	logs := &bytes.Buffer{}
	base, stop := start(t, logs)

	if _, _, err := httpGet(t, base+"/"); err != nil {
		t.Fatalf("GET /: %v", err)
	}

	stop()

	if _, _, err := httpGet(t, base+"/healthz"); err == nil {
		t.Error("server still accepting connections after shutdown")
	}
	if !strings.Contains(logs.String(), "stopped") {
		t.Error("shutdown was not logged")
	}
}

func TestServeBeforeListenFails(t *testing.T) {
	t.Parallel()
	app, err := New(testConfig(t), Deps{Prober: kube.UnavailableProber{}}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Serve(context.Background()); err == nil {
		t.Fatal("Serve succeeded without a listener")
	}
}

// TestApplicationNeverLogsConfiguredValues proves normal startup and
// shutdown logs carry no configured value beyond the listen address.
func TestApplicationNeverLogsConfiguredValues(t *testing.T) {
	t.Parallel()
	logs := &bytes.Buffer{}
	_, stop := start(t, logs)
	stop()
	for _, secretish := range []string{"orders", "payments"} {
		if strings.Contains(logs.String(), secretish) {
			t.Errorf("lifecycle log contains configured value %q", secretish)
		}
	}
}
