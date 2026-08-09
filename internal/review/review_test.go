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

package review

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/ops"
	"github.com/fyannk/pgConsole/internal/redact"
)

// fixedClock is a non-sleeping clock at a fixed instant.
type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time                                { return c.t }
func (c fixedClock) Wait(_ context.Context, _ time.Duration) error { return nil }

// recordingWriter captures the one status write.
type recordingWriter struct {
	calls    int
	name     string
	state    string
	level    string
	by       string
	at       time.Time
	failWith error
}

func (w *recordingWriter) WriteAccessRequestStatus(_ context.Context, name, state, level, decidedBy string, decidedAt time.Time) error {
	w.calls++
	w.name, w.state, w.level, w.by, w.at = name, state, level, decidedBy, decidedAt
	return w.failWith
}

func newExecutor(t *testing.T, writer Writer) (*Executor, *bytes.Buffer) {
	t.Helper()
	clock := fixedClock{t: time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)}
	csrf, err := ops.NewCSRF(clock)
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}
	logs := &bytes.Buffer{}
	return NewExecutor(writer, csrf, clock, slog.New(slog.NewJSONHandler(logs, nil))), logs
}

func TestDecideApproveWritesChosenLevel(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	e, _ := newExecutor(t, w)
	outcome, err := e.Decide(context.Background(), "req-1", ActionApprove, "poweruser", "alice@corp")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if outcome != "approved" {
		t.Errorf("outcome = %q", outcome)
	}
	if w.calls != 1 || w.name != "req-1" || w.state != "approved" || w.level != "poweruser" || w.by != "alice@corp" {
		t.Errorf("write = %+v", w)
	}
	if !w.at.Equal(time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("decidedAt = %v, want the clock instant", w.at)
	}
}

func TestDecideDenyWritesNoLevel(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	e, _ := newExecutor(t, w)
	outcome, err := e.Decide(context.Background(), "req-2", ActionDeny, "", "bob@corp")
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if outcome != "denied" || w.state != "denied" || w.level != "" || w.by != "bob@corp" {
		t.Errorf("deny write = %+v, outcome %q", w, outcome)
	}
}

// TestDecideApproveRejectsOffMenuLevel proves a tampered form naming a
// level outside the closed grantable set is refused, and no write
// happens. The whitespace and case cases matter: the operator's enum is
// exact, so a level that only looks right must not reach the API server
// and be refused there instead.
func TestDecideApproveRejectsOffMenuLevel(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	e, _ := newExecutor(t, w)
	for _, level := range []string{"", "superuser", "reader", "poweruser ", "DBA", "admin"} {
		_, err := e.Decide(context.Background(), "req-3", ActionApprove, level, "mallory")
		if !errors.Is(err, ErrUnknownLevel) {
			t.Errorf("level %q: err = %v, want ErrUnknownLevel", level, err)
		}
	}
	if w.calls != 0 {
		t.Fatalf("rejected approvals still wrote %d times", w.calls)
	}
}

func TestDecideRejectsUnknownAction(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	e, _ := newExecutor(t, w)
	if _, err := e.Decide(context.Background(), "req-4", "escalate", "view", "eve"); !errors.Is(err, ErrInvalidAction) {
		t.Errorf("err = %v, want ErrInvalidAction", err)
	}
	if w.calls != 0 {
		t.Error("unknown action wrote a decision")
	}
}

// TestDecideWriteFailurePropagatesRedacted proves a transport failure is
// returned and audited by category, never leaking hostile detail.
func TestDecideWriteFailurePropagatesRedacted(t *testing.T) {
	t.Parallel()
	const canary = "sekret-canary"
	w := &recordingWriter{failWith: redact.NewError("access request decide "+canary, redact.CategoryForbidden, nil)}
	e, logs := newExecutor(t, w)
	if _, err := e.Decide(context.Background(), "req-5", ActionDeny, "", "carol"); err == nil {
		t.Fatal("write failure not returned")
	}
	if strings.Contains(logs.String(), canary) {
		t.Error("write failure leaked hostile detail into the audit log")
	}
	if !strings.Contains(logs.String(), string(redact.CategoryForbidden)) {
		t.Error("audit log missing the failure category")
	}
}

// TestTokenBindsActionAndName proves a confirmation token is valid only
// for the exact action and request it was issued for.
func TestTokenBindsActionAndName(t *testing.T) {
	t.Parallel()
	e, _ := newExecutor(t, &recordingWriter{})
	token := e.Issue("req-6", ActionApprove)
	if !e.Verify("req-6", ActionApprove, token) {
		t.Fatal("token rejected for its own action and request")
	}
	if e.Verify("req-6", ActionDeny, token) {
		t.Error("approve token accepted for deny")
	}
	if e.Verify("req-7", ActionApprove, token) {
		t.Error("token accepted for a different request")
	}
}

// TestDecideAuditsReviewerAndOutcome proves the audit line records the
// reviewer identity and a stable outcome.
func TestDecideAuditsReviewerAndOutcome(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	e, logs := newExecutor(t, w)
	if _, err := e.Decide(context.Background(), "req-8", ActionApprove, "dba", "dana@corp"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	line := logs.String()
	for _, want := range []string{`"action":"approve"`, `"request":"req-8"`, `"reviewer":"dana@corp"`, `"reviewer_verification":"proxy-asserted"`, `"outcome":"recorded"`} {
		if !strings.Contains(line, want) {
			t.Errorf("audit line missing %q; got %s", want, line)
		}
	}
}
