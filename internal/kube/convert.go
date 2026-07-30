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
	apiv1 "github.com/cloudnative-pg/api/pkg/api/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/fyannk/pgconsole/internal/observe"
	"github.com/fyannk/pgconsole/internal/redact"
)

// Conversion bounds. Operator-reported free text is bounded here, at
// the boundary, so no later layer can accidentally render or retain an
// unbounded message.
const (
	maxConditions       = 32
	maxConditionMessage = 1024
)

// convertCluster converts a raw cluster object into source-neutral
// facts. Fields absent from the object — older CloudNativePG versions
// report fewer fields — stay empty or nil and render as unknown; they
// are never invented.
func convertCluster(content map[string]any) (observe.ClusterFacts, error) {
	var cluster apiv1.Cluster
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &cluster); err != nil {
		return observe.ClusterFacts{}, redact.NewError("cluster convert", redact.CategoryInternal, err)
	}

	facts := observe.ClusterFacts{
		Present:        true,
		UID:            string(cluster.UID),
		Phase:          cluster.Status.Phase,
		PhaseReason:    cluster.Status.PhaseReason,
		CurrentPrimary: cluster.Status.CurrentPrimary,
		TargetPrimary:  cluster.Status.TargetPrimary,
		Image:          cluster.Status.Image,
	}

	if cluster.Spec.Instances > 0 {
		v := cluster.Spec.Instances
		facts.DesiredInstances = &v
	}
	if cluster.Status.ReadyInstances > 0 || cluster.Status.Instances > 0 {
		v := cluster.Status.ReadyInstances
		facts.ReadyInstances = &v
	}
	if cluster.Status.TimelineID > 0 {
		v := cluster.Status.TimelineID
		facts.TimelineID = &v
	}
	if info := cluster.Status.PGDataImageInfo; info != nil && info.MajorVersion > 0 {
		v := info.MajorVersion
		facts.PostgresMajorVersion = &v
	}

	for i, cond := range cluster.Status.Conditions {
		if i == maxConditions {
			break
		}
		msg := cond.Message
		if len(msg) > maxConditionMessage {
			msg = msg[:maxConditionMessage]
		}
		facts.Conditions = append(facts.Conditions, observe.Condition{
			Type:    cond.Type,
			Status:  string(cond.Status),
			Reason:  cond.Reason,
			Message: msg,
		})
	}

	return facts, nil
}
