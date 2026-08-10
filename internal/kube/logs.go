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
	"io"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

// TailLogs fetches a bounded tail of one container in a member pod. An
// empty container name means the PostgreSQL container.
//
// Membership is re-verified live, immediately before the log request:
// the pod is fetched and its controller ownership checked, so a pod that
// stopped being a member between page render and click is refused. A
// non-member and a nonexistent pod are indistinguishable to the caller —
// both are not-found — while the exclusion is logged with its stable
// reason. The fetch runs on the request's context: a client disconnect
// cancels it. Nothing is cached or persisted.
//
// The boundary is the pod, not the container. Once controller ownership
// proves the pod is this cluster's, every container in it is in scope:
// the PostgreSQL container's log is the most sensitive stream in the pod
// — it can carry query text — so a caller already allowed that is not
// escalated by reading a plugin sidecar beside it. Restricting to
// postgres would instead hide the sidecars CNPG-I moves backup and WAL
// archiving into, which is where those failures are now reported.
//
// The name must still be one the pod declares. That is not a privilege
// check but an honesty one: an arbitrary string would let the API server
// decide what "not found" means, and the console states its own refusals.
func (c *Client) TailLogs(ctx context.Context, pod, container string) (observe.LogTail, error) {
	container, err := c.verifyMemberContainer(ctx, pod, container)
	if err != nil {
		return observe.LogTail{}, err
	}

	lines := int64(c.opts.LogTailLines)
	limit := c.opts.LogTailMaxBytes
	req := c.typed.CoreV1().Pods(c.opts.Namespace).GetLogs(pod, &corev1.PodLogOptions{
		Container:  container,
		TailLines:  &lines,
		LimitBytes: &limit,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return observe.LogTail{}, categorize("log tail", err)
	}
	defer func() { _ = stream.Close() }()

	// The server enforces LimitBytes; the LimitReader is the client-side
	// guard so a misbehaving server still cannot exceed the bound.
	data, err := io.ReadAll(io.LimitReader(stream, limit))
	if err != nil {
		return observe.LogTail{}, categorize("log tail read", err)
	}
	return observe.LogTail{
		Content:          string(data),
		TruncatedByBytes: int64(len(data)) >= limit,
		LineLimit:        c.opts.LogTailLines,
		ByteLimit:        limit,
	}, nil
}

// declaresContainer reports whether the pod declares the named
// container. Init containers count: an init container that failed is
// often the only place the reason a pod never started is written down.
func declaresContainer(containers []observe.ContainerFacts, name string) bool {
	for _, container := range containers {
		if container.Name == name {
			return true
		}
	}
	return false
}

// verifyMemberContainer re-checks live that the pod belongs to this
// cluster and declares the container, returning the container to read.
// An empty name means the PostgreSQL container.
//
// Both the on-demand tail and the continuous follower go through here,
// deliberately: two copies of a membership check are two chances for one
// of them to drift, and this is the check the whole log surface rests on.
func (c *Client) verifyMemberContainer(ctx context.Context, pod, container string) (string, error) {
	getCtx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	obj, err := c.dyn.Resource(podGVR).Namespace(c.opts.Namespace).Get(getCtx, pod, metav1.GetOptions{})
	cancel()
	if apierrors.IsNotFound(err) {
		return "", redact.NewError("log tail", redact.CategoryNotFound, err)
	}
	if err != nil {
		return "", categorize("log tail pod get", err)
	}
	facts, member, err := c.convertPod(obj.Object)
	if err != nil {
		return "", err
	}
	if !member {
		c.logExcludedPod(facts.Name)
		return "", redact.NewError("log tail", redact.CategoryNotFound, nil)
	}
	if container == "" {
		container = postgresContainer
	}
	if !declaresContainer(facts.Containers, container) {
		c.logExcludedContainer(facts.Name, container)
		return "", redact.NewError("log tail", redact.CategoryNotFound, nil)
	}
	return container, nil
}
