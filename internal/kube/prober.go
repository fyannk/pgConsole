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

package kube

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fyannk/pgConsole/internal/redact"
)

// APIProber reports readiness through a lightweight, pinned get of the
// target cluster. Readiness means the API server is reachable: a
// not-found or forbidden answer is an answer, so it is ready — the
// cluster's state and the Role's grants are page content, never
// readiness. Only transport-level failure reports not ready.
type APIProber struct {
	get func(ctx context.Context) error
}

// NewProber builds the readiness prober of this client.
func (c *Client) NewProber() *APIProber {
	return &APIProber{get: func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
		defer cancel()
		_, err := c.dyn.Resource(clusterGVR).Namespace(c.opts.Namespace).Get(ctx, c.opts.ClusterName, metav1.GetOptions{})
		return err
	}}
}

// newProberForTest builds a prober around an injected get outcome.
func newProberForTest(get func(ctx context.Context) error) *APIProber {
	return &APIProber{get: get}
}

// Ready probes the API server. See APIProber for the readiness meaning.
func (p *APIProber) Ready(ctx context.Context) error {
	err := p.get(ctx)
	if err == nil {
		return nil
	}
	switch redact.Categorize(categorize("readiness probe", err)) {
	case redact.CategoryNotFound, redact.CategoryForbidden:
		return nil
	default:
		return categorize("readiness probe", err)
	}
}
