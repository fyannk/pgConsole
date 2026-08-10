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
)

// FollowLogs opens a live stream for one container of a member pod.
//
// Membership is re-verified immediately before opening, through the same
// check the on-demand tail uses: the roster driving the follower may be
// a few seconds old, and a pod that stopped being a member must not keep
// streaming into the console. It uses the existing pods/log grant and
// needs no new RBAC.
//
// The stream starts from now, with no backfill. Re-reading history on
// every reconnect would re-count lines the matcher has already seen and
// re-retain lines the buffer already holds — which for a container that
// restarts in a loop would compound with every attempt.
func (c *Client) FollowLogs(ctx context.Context, pod, container string) (io.ReadCloser, error) {
	verified, err := c.verifyMemberContainer(ctx, pod, container)
	if err != nil {
		return nil, err
	}
	follow := true
	return c.typed.CoreV1().Pods(c.opts.Namespace).
		GetLogs(pod, &corev1.PodLogOptions{Container: verified, Follow: follow}).
		Stream(ctx)
}
