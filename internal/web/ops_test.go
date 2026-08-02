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

package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/ops"
)

// recordingExecutor records executed operations and issues real tokens.
type recordingExecutor struct {
	tokens   map[string]string
	executed []string
	failWith error
}

func newRecordingExecutor() *recordingExecutor {
	return &recordingExecutor{tokens: map[string]string{}}
}

func (e *recordingExecutor) Catalog() []ops.Descriptor { return ops.Catalog() }

func (e *recordingExecutor) Issue(id ops.ID, target string) string {
	tok := "tok-" + string(id) + "-" + target
	e.tokens[string(id)+"\x00"+target] = tok
	return tok
}

func (e *recordingExecutor) Verify(id ops.ID, target, token string) bool {
	want, ok := e.tokens[string(id)+"\x00"+target]
	return ok && token == want
}

func (e *recordingExecutor) Execute(_ context.Context, id ops.ID, target string, _ ops.Identity) (string, error) {
	if e.failWith != nil {
		return "unavailable", e.failWith
	}
	e.executed = append(e.executed, string(id)+"/"+target)
	return "accepted", nil
}

// powerUser is the minimal authorized header set for the operation
// routes: a forwarded identity and the poweruser level. The operations
// mechanics tests below exercise the confirm/execute flow past the level
// gate, so every request they issue carries it.
var powerUser = map[string]string{"X-Forwarded-User": "operator", "X-PgToolBox-Level": "poweruser"}

func TestOperationsNavigationRequiresIdentityAndPowerUserLevel(t *testing.T) {
	t.Parallel()
	h := newOpsHandler(t, newRecordingExecutor())
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{name: "poweruser", headers: powerUser, want: true},
		{name: "dba", headers: map[string]string{"X-Forwarded-User": "dba", "X-PgToolBox-Level": "dba"}, want: true},
		{name: "view", headers: map[string]string{"X-Forwarded-User": "viewer", "X-PgToolBox-Level": "view"}},
		{name: "level without identity", headers: map[string]string{"X-PgToolBox-Level": "poweruser"}},
		{name: "identity without level", headers: map[string]string{"X-Forwarded-User": "operator"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := getWithHeaders(t, h, "/", tc.headers).Body.String()
			if got := strings.Contains(body, `href="/operations"`); got != tc.want {
				t.Errorf("operations navigation present = %v, want %v", got, tc.want)
			}
		})
	}
}

// newOpsHandler builds an operations-enabled handler over the executor,
// with the trusted level header configured so poweruser requests reach
// the routes.
func newOpsHandler(t *testing.T, exec OpsExecutor) *Handler {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h, err := New(Config{ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour, AllowOperations: true, LevelHeader: "X-PgToolBox-Level"},
		Sources{Cluster: staticSnapshots{}, Pods: staticSnapshots{}, Events: staticSnapshots{}, Backups: staticSnapshots{}, Poolers: staticSnapshots{}, PoolerPods: staticSnapshots{}, FailoverQuorum: staticSnapshots{}, ImageCatalogs: staticSnapshots{}, DatabaseObjects: staticSnapshots{}},
		kube.FakeProber{}, fakeTailer{},
		Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
		exec, nil, func() time.Time { return testNow }, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

// opsGet issues an authorized (poweruser) GET against the operation
// routes.
func opsGet(t *testing.T, h *Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return getWithHeaders(t, h, path, powerUser)
}

// confirmToken fetches a confirmation page and extracts the CSRF token.
func confirmToken(t *testing.T, h *Handler, op, instance string) string {
	t.Helper()
	path := "/operations/" + op
	if instance != "" {
		path += "?instance=" + instance
	}
	body := opsGet(t, h, path).Body.String()
	m := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no CSRF token in confirmation page: %s", body)
	}
	return m[1]
}

// postOp posts an operation with the given form and headers. The
// poweruser authorization headers are applied first, so a caller's
// headers can override them (for the level-gate and cross-origin tests).
func postOp(t *testing.T, h *Handler, op string, form url.Values, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/operations/"+op, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range powerUser {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	return rec
}

func TestOperationsFullFlow(t *testing.T) {
	t.Parallel()
	exec := newRecordingExecutor()
	h := newOpsHandler(t, exec)

	// The index lists the closed catalog.
	if body := opsGet(t, h, "/operations").Body.String(); !strings.Contains(body, "Restart cluster") {
		t.Fatal("operations index misses an operation")
	}

	token := confirmToken(t, h, "restart", "")
	rec := postOp(t, h, "restart", url.Values{"csrf": {token}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("execute status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "accepted") {
		t.Error("result page misses the accepted outcome")
	}
	if len(exec.executed) != 1 || exec.executed[0] != "restart/" {
		t.Fatalf("executed = %v, want one restart", exec.executed)
	}
}

func TestOperationsPromoteCarriesInstance(t *testing.T) {
	t.Parallel()
	exec := newRecordingExecutor()
	h := newOpsHandler(t, exec)
	token := confirmToken(t, h, "promote", "orders-2")
	rec := postOp(t, h, "promote", url.Values{"csrf": {token}, "instance": {"orders-2"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("promote status = %d", rec.Code)
	}
	if len(exec.executed) != 1 || exec.executed[0] != "promote/orders-2" {
		t.Fatalf("executed = %v, want promote for orders-2", exec.executed)
	}
}

// TestOperationsDisabledModeHasNoRoute proves read-only mode registers
// no operation route, GET or POST — not a route that refuses.
func TestOperationsDisabledModeHasNoRoute(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, staticSnapshots{}, kube.FakeProber{}, Links{})
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/operations"},
		{http.MethodGet, "/operations/restart"},
		{http.MethodPost, "/operations/restart"},
	} {
		if rec := get(t, h, tc.method, tc.path); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 in read-only mode", tc.method, tc.path, rec.Code)
		}
	}
	if body := get(t, h, http.MethodGet, "/").Body.String(); strings.Contains(body, "/operations") {
		t.Error("read-only console renders an operations affordance")
	}
}

func TestOperationsRejectMissingOrForgedCSRF(t *testing.T) {
	t.Parallel()
	exec := newRecordingExecutor()
	h := newOpsHandler(t, exec)
	for _, tc := range []struct {
		name string
		form url.Values
	}{
		{"no token", url.Values{}},
		{"forged token", url.Values{"csrf": {"tok-restart-forged"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postOp(t, h, "restart", tc.form, nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rec.Code)
			}
		})
	}
	if len(exec.executed) != 0 {
		t.Fatalf("an operation executed without a valid token: %v", exec.executed)
	}
}

// TestOperationsRejectCrossOrigin proves a cross-site POST is refused
// even with an otherwise valid token.
func TestOperationsRejectCrossOrigin(t *testing.T) {
	t.Parallel()
	exec := newRecordingExecutor()
	h := newOpsHandler(t, exec)
	token := confirmToken(t, h, "restart", "")
	rec := postOp(t, h, "restart", url.Values{"csrf": {token}}, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site status = %d, want 403", rec.Code)
	}
	if len(exec.executed) != 0 {
		t.Fatal("cross-site request executed an operation")
	}
}

func TestOperationsRejectGETExecute(t *testing.T) {
	t.Parallel()
	h := newOpsHandler(t, newRecordingExecutor())
	// A GET to the execute path resolves to the confirmation handler,
	// never execution; the POST-only method mux gives the confirm form.
	body := opsGet(t, h, "/operations/restart").Body.String()
	if !strings.Contains(body, "Confirm and request") {
		t.Fatal("GET on an operation must render confirmation, not execute")
	}
}

func TestOperationsUnknownIs404(t *testing.T) {
	t.Parallel()
	h := newOpsHandler(t, newRecordingExecutor())
	if rec := opsGet(t, h, "/operations/delete-cluster"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown operation confirm = %d, want 404", rec.Code)
	}
}

// TestOperationsRequirePowerUserLevel proves the operations routes admit
// the poweruser and dba levels and deny everything below — view, an
// unknown level, and a missing identity — by the level header alone.
func TestOperationsRequirePowerUserLevel(t *testing.T) {
	t.Parallel()
	exec := newRecordingExecutor()
	logger := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h, err := New(Config{ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour, AllowOperations: true, LevelHeader: "X-PgToolBox-Level"},
		Sources{Cluster: staticSnapshots{}, Pods: staticSnapshots{}, Events: staticSnapshots{}, Backups: staticSnapshots{}, Poolers: staticSnapshots{}, PoolerPods: staticSnapshots{}, FailoverQuorum: staticSnapshots{}, ImageCatalogs: staticSnapshots{}, DatabaseObjects: staticSnapshots{}},
		kube.FakeProber{}, fakeTailer{},
		Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
		exec, nil, func() time.Time { return testNow }, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	denied := []map[string]string{
		{"X-Forwarded-User": "viewer", "X-PgToolBox-Level": "view"},
		{"X-Forwarded-User": "viewer", "X-PgToolBox-Level": "bogus"},
		{"X-Forwarded-User": "viewer"},
		{"X-PgToolBox-Level": "poweruser"}, // no identity to attribute
	}
	for _, headers := range denied {
		if rec := getWithHeaders(t, h, "/operations", headers); rec.Code != http.StatusForbidden {
			t.Errorf("operations index for %v = %d, want 403", headers, rec.Code)
		}
	}

	for _, level := range []string{"poweruser", "dba"} {
		rec := getWithHeaders(t, h, "/operations", map[string]string{"X-Forwarded-User": "operator", "X-PgToolBox-Level": level})
		if rec.Code != http.StatusOK {
			t.Errorf("%s operations index = %d, want 200", level, rec.Code)
		}
	}
}
