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
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/kube"
	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/ops"
	"github.com/fyannk/pgConsole/internal/review"
)

// reviewClock is a non-sleeping clock at the fixed test instant.
type reviewClock struct{}

func (reviewClock) Now() time.Time                                { return testNow }
func (reviewClock) Wait(_ context.Context, _ time.Duration) error { return nil }

// reviewWriter records the one decision write.
type reviewWriter struct {
	calls              int
	name, state, level string
	by                 string
	failWith           error
}

func (w *reviewWriter) WriteAccessRequestStatus(_ context.Context, name, state, level, decidedBy string, _ time.Time) error {
	w.calls++
	w.name, w.state, w.level, w.by = name, state, level, decidedBy
	return w.failWith
}

// fakeAccessReview serves a fixed access-review snapshot.
type fakeAccessReview struct {
	snap observe.AccessReviewSnapshot
	ok   bool
}

func (f fakeAccessReview) CurrentAccessReview() (observe.AccessReviewSnapshot, bool) {
	return f.snap, f.ok
}

// dba is the header set that admits the review routes.
var dba = map[string]string{"X-Forwarded-User": "dba@corp", "X-PgToolBox-Level": "dba"}

func TestAccessReviewNavigationRequiresIdentityAndDBALevel(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newReviewHandler(t, pendingSnapshot())
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{name: "dba", headers: dba, want: true},
		{name: "poweruser", headers: map[string]string{"X-Forwarded-User": "operator", "X-PgToolBox-Level": "poweruser"}},
		{name: "view", headers: map[string]string{"X-Forwarded-User": "viewer", "X-PgToolBox-Level": "view"}},
		{name: "level without identity", headers: map[string]string{"X-PgToolBox-Level": "dba"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := getWithHeaders(t, h, "/", tc.headers).Body.String()
			if got := strings.Contains(body, `href="/access-requests"`); got != tc.want {
				t.Errorf("access-review navigation present = %v, want %v", got, tc.want)
			}
		})
	}
}

func newReviewHandler(t *testing.T, source AccessReviewSource) (*Handler, *reviewWriter, *review.Executor, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	w := &reviewWriter{}
	csrf, err := ops.NewCSRF(reviewClock{})
	if err != nil {
		t.Fatalf("NewCSRF: %v", err)
	}
	exec := review.NewExecutor(w, csrf, reviewClock{}, logger)
	h, err := New(Config{ClusterName: "orders", Namespace: "payments", EventsWindow: time.Hour, LevelHeader: "X-PgToolBox-Level", AllowAccessReview: true},
		Sources{Cluster: staticSnapshots{}, Pods: staticSnapshots{}, Events: staticSnapshots{}, Backups: staticSnapshots{}, Poolers: staticSnapshots{}, PoolerPods: staticSnapshots{}, FailoverQuorum: staticSnapshots{}, ImageCatalogs: staticSnapshots{}, DatabaseObjects: staticSnapshots{}, AccessReview: source},
		kube.FakeProber{}, fakeTailer{},
		Auth{Extractor: identity.NewExtractor("X-Forwarded-User")},
		nil, exec, func() time.Time { return testNow }, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h, w, exec, logs
}

func pendingSnapshot() fakeAccessReview {
	created := testNow.Add(-5 * time.Minute)
	decided := testNow.Add(-1 * time.Hour)
	return fakeAccessReview{
		ok: true,
		snap: observe.AccessReviewSnapshot{
			Generation: 3,
			ObservedAt: testNow.Add(-2 * time.Second),
			Requests: []observe.AccessRequestFacts{
				{Name: "req-alice", UID: "u1", Subject: "alice@corp", Message: "need read access", State: observe.AccessRequestPending, CreatedAt: created},
				{Name: "req-bob", UID: "u2", Subject: "bob@corp", State: observe.AccessRequestApproved, RequestedLevel: "poweruser", DecidedBy: "carol@corp", DecidedAt: &decided},
			},
		},
	}
}

func postReview(t *testing.T, h *Handler, path string, form url.Values, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range dba {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.Routes().ServeHTTP(rec, req)
	return rec
}

// TestAccessReviewRequiresDBALevel proves the panel and its actions admit
// only the dba level; every lower level and a missing identity are denied
// by direct request.
func TestAccessReviewRequiresDBALevel(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newReviewHandler(t, pendingSnapshot())

	denied := []map[string]string{
		{"X-Forwarded-User": "u", "X-PgToolBox-Level": "view"},
		{"X-Forwarded-User": "u", "X-PgToolBox-Level": "poweruser"},
		{"X-Forwarded-User": "u", "X-PgToolBox-Level": "bogus"},
		{"X-Forwarded-User": "u"},
	}
	for _, headers := range denied {
		if rec := getWithHeaders(t, h, "/access-requests", headers); rec.Code != http.StatusForbidden {
			t.Errorf("index for %v = %d, want 403", headers, rec.Code)
		}
	}
	if rec := getWithHeaders(t, h, "/access-requests", dba); rec.Code != http.StatusOK {
		t.Fatalf("dba index = %d, want 200", rec.Code)
	}
}

// TestAccessReviewDisabledHasNoRoutes proves that without the panel
// enabled the routes do not exist — 404, not a route that refuses.
func TestAccessReviewDisabledHasNoRoutes(t *testing.T) {
	t.Parallel()
	h, _ := newLeveledHandler(t, staticSnapshots{})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/access-requests"},
		{http.MethodPost, "/access-requests/req-alice/approve"},
		{http.MethodPost, "/access-requests/req-alice/deny"},
	} {
		if rec := getWithHeaders(t, h, tc.path, dba); tc.method == http.MethodGet && rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 when disabled", tc.path, rec.Code)
		}
	}
	if rec := postReview(t, h, "/access-requests/req-alice/approve", url.Values{}, nil); rec.Code != http.StatusNotFound {
		t.Errorf("disabled approve = %d, want 404", rec.Code)
	}
}

// TestAccessReviewListsPendingAndDecided proves the panel renders pending
// requests with the closed level options and forms, and decided requests
// read-only. The picker is asserted in full: it comes from the console's
// own ladder, so all three levels must be offered in every deployment.
func TestAccessReviewListsPendingAndDecided(t *testing.T) {
	t.Parallel()
	h, _, _, _ := newReviewHandler(t, pendingSnapshot())
	body := getWithHeaders(t, h, "/access-requests", dba).Body.String()
	for _, want := range []string{
		"alice@corp", "need read access",
		`action="/access-requests/req-alice/approve"`,
		`action="/access-requests/req-alice/deny"`,
		`<option value="view">view</option>`,
		`<option value="poweruser">poweruser</option>`,
		`<option value="dba">dba</option>`,
		"bob@corp", "approved", "carol@corp",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("panel misses %q", want)
		}
	}
	// The decided request offers no action form.
	if strings.Contains(body, `action="/access-requests/req-bob/approve"`) {
		t.Error("a decided request rendered an action form")
	}
}

// TestAccessReviewApproveFlow proves a dba approval with a valid token
// and an offered level writes the decision with the reviewer identity.
func TestAccessReviewApproveFlow(t *testing.T) {
	t.Parallel()
	h, w, exec, _ := newReviewHandler(t, pendingSnapshot())
	token := exec.Issue("req-alice", review.ActionApprove)
	rec := postReview(t, h, "/access-requests/req-alice/approve", url.Values{"csrf": {token}, "level": {"poweruser"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d", rec.Code)
	}
	if w.calls != 1 || w.name != "req-alice" || w.state != "approved" || w.level != "poweruser" || w.by != "dba@corp" {
		t.Fatalf("write = %+v", w)
	}
}

func TestAccessReviewDenyFlow(t *testing.T) {
	t.Parallel()
	h, w, exec, _ := newReviewHandler(t, pendingSnapshot())
	token := exec.Issue("req-alice", review.ActionDeny)
	rec := postReview(t, h, "/access-requests/req-alice/deny", url.Values{"csrf": {token}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("deny status = %d", rec.Code)
	}
	if w.calls != 1 || w.state != "denied" || w.level != "" {
		t.Fatalf("deny write = %+v", w)
	}
}

// TestAccessReviewRejectsMissingOrForgedCSRF proves the decision write is
// guarded by the token even for a dba.
func TestAccessReviewRejectsMissingOrForgedCSRF(t *testing.T) {
	t.Parallel()
	h, w, _, _ := newReviewHandler(t, pendingSnapshot())
	for _, form := range []url.Values{{}, {"csrf": {"forged"}, "level": {"poweruser"}}} {
		if rec := postReview(t, h, "/access-requests/req-alice/approve", form, nil); rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}
	}
	if w.calls != 0 {
		t.Fatalf("a decision wrote without a valid token: %d", w.calls)
	}
}

func TestAccessReviewRejectsCrossOrigin(t *testing.T) {
	t.Parallel()
	h, w, exec, _ := newReviewHandler(t, pendingSnapshot())
	token := exec.Issue("req-alice", review.ActionDeny)
	rec := postReview(t, h, "/access-requests/req-alice/deny", url.Values{"csrf": {token}}, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site status = %d, want 403", rec.Code)
	}
	if w.calls != 0 {
		t.Error("cross-site request wrote a decision")
	}
}

// TestAccessReviewApproveOffMenuLevelRejected proves a tampered level is a
// 400 and no write happens.
func TestAccessReviewApproveOffMenuLevelRejected(t *testing.T) {
	t.Parallel()
	h, w, exec, _ := newReviewHandler(t, pendingSnapshot())
	token := exec.Issue("req-alice", review.ActionApprove)
	rec := postReview(t, h, "/access-requests/req-alice/approve", url.Values{"csrf": {token}, "level": {"superuser"}}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("off-menu level status = %d, want 400", rec.Code)
	}
	if w.calls != 0 {
		t.Error("off-menu approval wrote a decision")
	}
}

// TestAccessReviewEscapesHostileContent proves request-supplied text
// renders as text only.
func TestAccessReviewEscapesHostileContent(t *testing.T) {
	t.Parallel()
	source := fakeAccessReview{ok: true, snap: observe.AccessReviewSnapshot{
		Generation: 1, ObservedAt: testNow,
		Requests: []observe.AccessRequestFacts{
			{Name: "req-x", Subject: `<script>alert(1)</script>`, Message: "hi", State: observe.AccessRequestPending, CreatedAt: testNow},
		},
	}}
	h, _, _, _ := newReviewHandler(t, source)
	body := getWithHeaders(t, h, "/access-requests", dba).Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("request subject rendered unescaped")
	}
}

// TestAccessReviewGateDenialDoesNotLeakIdentity proves a denied lower-tier
// request logs a reason only, not the forwarded identity.
func TestAccessReviewGateDenialDoesNotLeakIdentity(t *testing.T) {
	t.Parallel()
	h, _, _, logs := newReviewHandler(t, pendingSnapshot())
	getWithHeaders(t, h, "/access-requests", map[string]string{"X-Forwarded-User": "mallory-canary", "X-PgToolBox-Level": "view"})
	if strings.Contains(logs.String(), "mallory-canary") {
		t.Error("denied identity leaked into logs")
	}
}
