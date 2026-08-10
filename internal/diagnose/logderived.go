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

package diagnose

import (
	"fmt"

	"github.com/fyannk/pgConsole/internal/logstream"
)

// LogObservations is the read side of the continuous matcher. It is an
// interface so diagnose depends on the shape rather than the matcher,
// and so a test needs no stream.
//
// A nil value means log following is off, which the detector reports as
// "could not run" rather than as nothing found: a console that is not
// reading logs has not ruled out anything in them.
type LogObservations interface {
	Observations() []logstream.Observation
}

// logDetector reports what the continuous matcher found in the logs.
//
// It is the one detector whose evidence comes from a stream rather than
// a snapshot, and that changes what it may claim. A stream is best
// effort: it breaks on every container restart and Kubernetes cannot say
// what was missed. So this reports what was seen and never how much
// there was — the count is stated as a floor, and the absence of an
// observation is never evidence that nothing happened.
type logDetector struct{}

func (logDetector) Name() string { return "log-messages" }

func (logDetector) Describes() string {
	return "a message in a container's log that names a failure no object status reports"
}

func (d logDetector) Detect(in Input) ([]Finding, string) {
	if in.Logs == nil {
		return nil, "log following is off, so nothing in the logs has been read"
	}
	observations := in.Logs.Observations()
	if len(observations) == 0 {
		return nil, ""
	}

	findings := make([]Finding, 0, len(observations))
	for _, observation := range observations {
		findings = append(findings, Finding{
			ID:       "log/" + observation.RuleID + "/" + observation.Pod + "/" + observation.Container,
			Severity: SeverityCritical,
			Summary:  observation.Summary,
			Detail: "Read from the container's log while following it. Following is " +
				"best effort: a stream breaks on every container restart and " +
				"Kubernetes cannot report what was emitted in between, so the " +
				"count below is a floor and an absence here rules nothing out.",
			Evidence: []Evidence{
				{
					Origin: "container log (best effort)",
					Object: fmt.Sprintf("Pod/%s container %s", observation.Pod, observation.Container),
					Detail: observation.Line,
				},
				{
					Origin: "console-observed",
					Object: "matching window",
					Detail: fmt.Sprintf("first seen %s, most recently %s, at least %d matching lines",
						observation.FirstSeen.UTC().Format("15:04:05Z"),
						observation.LastSeen.UTC().Format("15:04:05Z"),
						observation.Count),
				},
			},
			Link:      "/logs/" + observation.Pod + "/" + observation.Container,
			LinkLabel: "Log tail",
		})
	}
	return findings, ""
}
