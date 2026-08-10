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

	"github.com/fyannk/pgConsole/internal/logstream"
	"github.com/fyannk/pgConsole/internal/observe"
)

// PodRoster reads the current instance pods. The observe store
// implements it; the follower needs only this much.
type PodRoster interface {
	CurrentPods() (observe.PodsSnapshot, bool)
}

// LogFollower is the Kubernetes side of continuous log following: it
// says which containers to follow and opens the streams.
//
// Membership is decided the same way the on-demand tail decides it — by
// the roster the collectors already proved — so following can never
// reach a pod the console would refuse to tail on request. It uses the
// same pods/log grant and needs no new RBAC.
type LogFollower struct {
	client *Client
	roster PodRoster
}

// NewLogFollower wires a follower onto the client and the pod roster.
func (c *Client) NewLogFollower(roster PodRoster) *LogFollower {
	return &LogFollower{client: c, roster: roster}
}

// Members lists every container of every currently observed instance
// pod, including init containers: an init container that failed is often
// the only place the reason a pod never started is written down.
//
// A pod that has left the roster simply stops being returned, which is
// what makes the runner drop its follower.
func (f *LogFollower) Members() []logstream.Member {
	snap, ok := f.roster.CurrentPods()
	if !ok {
		return nil
	}
	var members []logstream.Member
	for _, pod := range snap.Pods {
		for _, container := range pod.Containers {
			members = append(members, logstream.Member{Pod: pod.Name, Container: container.Name})
		}
	}
	return members
}

// Follow opens a live stream for one container.
//
// It re-verifies membership immediately before opening, exactly as the
// on-demand tail does: the roster may be a few seconds old, and a pod
// that stopped being a member must not keep streaming into the console.
func (f *LogFollower) Follow(ctx context.Context, pod, container string) (io.ReadCloser, error) {
	verified, err := f.client.verifyMemberContainer(ctx, pod, container)
	if err != nil {
		return nil, err
	}
	container = verified
	// No TailLines: following starts from now. Backfilling on every
	// reconnect would re-observe lines the matcher has already counted
	// and re-retain lines the buffer already holds.
	follow := true
	return f.client.typed.CoreV1().Pods(f.client.opts.Namespace).
		GetLogs(pod, &corev1.PodLogOptions{Container: container, Follow: follow}).
		Stream(ctx)
}
