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
	"log/slog"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/kube"
)

// TestHistoryRunnerLifecycle proves a wired history runner is started
// by Serve and stopped — and waited for — on shutdown, exactly like the
// collectors it runs beside.
func TestHistoryRunnerLifecycle(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	stopped := make(chan struct{})
	deps := Deps{
		Prober: kube.UnavailableProber{},
		HistoryRunner: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(stopped)
			return nil
		},
	}
	app, err := New(testConfig(t), deps, slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := app.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("history runner never started")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Serve returned before the history runner stopped")
	}
}
