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

package ops

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fyannk/pgconsole/internal/redact"
)

// recordingWriter records every mutation the executor performs.
type recordingWriter struct {
	mu       sync.Mutex
	calls    []string
	backup   string
	instance string
	err      error
}

func (w *recordingWriter) CreateBackup(_ context.Context, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, "backup")
	w.backup = name
	return w.err
}

func (w *recordingWriter) ReloadCluster(_ context.Context, _ time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, "reload")
	return w.err
}

func (w *recordingWriter) RestartCluster(_ context.Context, _ time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, "restart")
	return w.err
}

func (w *recordingWriter) PromoteInstance(_ context.Context, instance string, _ time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, "promote")
	w.instance = instance
	return w.err
}

// fixedClock is a settable, non-sleeping clock.
type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time                                { return c.t }
func (c fixedClock) Wait(_ context.Context, _ time.Duration) error { return nil }

func newExecutor(t *testing.T, w Writer) (*Executor, *bytes.Buffer) {
	t.Helper()
	clock := fixedClock{t: time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)}
	csrf, err := NewCSRF(clock)
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}
	logs := &bytes.Buffer{}
	return NewExecutor(w, csrf, clock, slog.New(slog.NewJSONHandler(logs, nil))), logs
}

func TestExecuteEachOperation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id     ID
		target string
		want   string
	}{
		{Backup, "", "backup"},
		{Reload, "", "reload"},
		{Restart, "", "restart"},
		{Promote, "orders-2", "promote"},
	}
	for _, tc := range cases {
		t.Run(string(tc.id), func(t *testing.T) {
			t.Parallel()
			w := &recordingWriter{}
			e, _ := newExecutor(t, w)
			outcome, err := e.Execute(context.Background(), tc.id, tc.target, Identity{User: "alice", Verified: true})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if outcome != "accepted" {
				t.Errorf("outcome = %q, want accepted", outcome)
			}
			if len(w.calls) != 1 || w.calls[0] != tc.want {
				t.Fatalf("calls = %v, want [%s]", w.calls, tc.want)
			}
		})
	}
}

// TestExecuteUnknownOperationMutatesNothing proves an operation outside
// the catalog does not exist: it is rejected without touching the
// writer.
func TestExecuteUnknownOperationMutatesNothing(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	e, _ := newExecutor(t, w)
	_, err := e.Execute(context.Background(), ID("delete-cluster"), "", Identity{})
	if err == nil {
		t.Fatal("unknown operation accepted")
	}
	if len(w.calls) != 0 {
		t.Fatalf("unknown operation reached the writer: %v", w.calls)
	}
}

func TestExecutePromoteRequiresInstance(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	e, _ := newExecutor(t, w)
	if _, err := e.Execute(context.Background(), Promote, "", Identity{}); err == nil {
		t.Fatal("promote without an instance accepted")
	}
	if len(w.calls) != 0 {
		t.Fatal("promote without an instance reached the writer")
	}
}

// TestExecuteWritesOneAuditLine proves each operation produces exactly
// one structured audit line carrying the actor and outcome, with the
// verification label.
func TestExecuteWritesOneAuditLine(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	e, logs := newExecutor(t, w)
	if _, err := e.Execute(context.Background(), Restart, "", Identity{User: "alice", Verified: false}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	out := logs.String()
	if n := strings.Count(out, `"msg":"operation"`); n != 1 {
		t.Fatalf("audit lines = %d, want exactly 1", n)
	}
	for _, want := range []string{`"operation":"restart"`, `"outcome":"accepted"`, `"actor":"alice"`, `"actor_verification":"unverified"`} {
		if !strings.Contains(out, want) {
			t.Errorf("audit line misses %s", want)
		}
	}
}

// TestExecuteFailureAuditsCategoryOnly proves a failed operation audits
// the outcome category, and the audit is still exactly one line.
func TestExecuteFailureAuditsCategoryOnly(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{err: redact.NewError("cluster restart", redact.CategoryForbidden, errors.New("rbac denied at 10.0.0.1"))}
	e, logs := newExecutor(t, w)
	outcome, err := e.Execute(context.Background(), Restart, "", Identity{User: "alice"})
	if err == nil {
		t.Fatal("failed operation reported success")
	}
	if outcome != "forbidden" {
		t.Errorf("outcome = %q, want forbidden", outcome)
	}
	if strings.Contains(logs.String(), "10.0.0.1") {
		t.Error("audit leaked raw error detail")
	}
}

func TestCatalogIsClosed(t *testing.T) {
	t.Parallel()
	if len(Catalog()) != 4 {
		t.Fatalf("catalog size = %d, want the four enumerated operations", len(Catalog()))
	}
	if _, ok := Describe(ID("apply")); ok {
		t.Error("a non-catalog operation resolved")
	}
}
