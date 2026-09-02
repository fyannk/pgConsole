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
// The stream starts from now, with no backfill: see followLogOptions for
// why that has to be asked for rather than assumed. What was written
// while no stream was open is not recovered, and is not meant to be --
// the follower records the window as a gap and the matcher reports the
// container as unread, which is the honest account of it. History is the
// on-demand tail's job, and it asks for a tail length of its own.
func (c *Client) FollowLogs(ctx context.Context, pod, container string) (io.ReadCloser, error) {
	verified, err := c.verifyMemberContainer(ctx, pod, container)
	if err != nil {
		return nil, err
	}
	return c.typed.CoreV1().Pods(c.opts.Namespace).
		GetLogs(pod, followLogOptions(verified)).
		Stream(ctx)
}

// followFromNow is the tail length the follower asks for: none, so the
// stream carries only what is written after it opens.
//
// It has to be said explicitly. With TailLines unset and no SinceTime,
// the API server serves the log from the container's creation and only
// then follows -- so every reconnect would re-read the whole log. The
// matcher would count those lines again, inflating "at least N matching
// lines" into a count of re-reads; it would refresh their last-seen
// instant, so an observation would stay current because the connection
// blinked rather than because the fault recurred, and would never expire
// while reconnects continued; and the buffer would re-retain lines it
// already holds. Every one of those is the console reporting the past as
// the present, out of a stream break it had already recorded as a gap.
const followFromNow = int64(0)

// followLogOptions is what the follower asks the API server for. It is
// built here rather than inline so the tail length is something a test
// can hold the follower to.
func followLogOptions(container string) *corev1.PodLogOptions {
	tail := followFromNow
	return &corev1.PodLogOptions{Container: container, Follow: true, TailLines: &tail}
}
