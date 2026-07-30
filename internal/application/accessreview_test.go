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
	"net/http"
	"testing"
	"time"

	"github.com/fyannk/pgconsole/internal/config"
	"github.com/fyannk/pgconsole/internal/kube"
	"github.com/fyannk/pgconsole/internal/observe"
)

// panicReviewWriter fails the test if the read-only assembly ever reaches
// the decision writer.
type panicReviewWriter struct{ t *testing.T }

func (p panicReviewWriter) WriteAccessRequestStatus(context.Context, string, string, string, string, time.Time) error {
	p.t.Fatal("disabled assembly reached the access-review writer")
	return nil
}

// staticAccessSource is a never-consulted source for the disabled-mode
// assembly: the collector is not wired when the panel is off.
type staticAccessSource struct{}

func (staticAccessSource) FetchAccessReview(context.Context) (observe.AccessReviewState, error) {
	return observe.AccessReviewState{}, nil
}

func (staticAccessSource) WatchAccessReview(context.Context, observe.AccessReviewState) (observe.AccessReviewWatch, error) {
	return nil, context.Canceled
}

// reviewConfig loads a config with the given ALLOW_ACCESS_REVIEW value.
func reviewConfig(t *testing.T, allow string) config.Config {
	t.Helper()
	cfg, err := config.Load(func(name string) (string, bool) {
		switch name {
		case config.EnvClusterName:
			return "orders", true
		case config.EnvNamespace:
			return "payments", true
		case config.EnvListenAddr:
			return "127.0.0.1:0", true
		case config.EnvAllowAccessReview:
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

// TestDisabledAssemblyHasNoAccessReviewRoute proves that with the review
// panel off, no review route is registered even when a source and writer
// are supplied — the writer is never referenced.
func TestDisabledAssemblyHasNoAccessReviewRoute(t *testing.T) {
	t.Parallel()
	base, stop := serveTest(t, reviewConfig(t, "false"), Deps{
		Prober:             kube.UnavailableProber{},
		AccessReviewSource: staticAccessSource{},
		AccessReviewWriter: panicReviewWriter{t: t},
	})
	defer stop()
	for _, path := range []string{"/access-requests", "/access-requests/req-1/approve"} {
		status, _, err := httpGet(t, base+path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if status != http.StatusNotFound {
			t.Errorf("disabled %s = %d, want 404", path, status)
		}
	}
}
