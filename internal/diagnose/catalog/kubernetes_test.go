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

package catalog

import (
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/diagnose"
	"github.com/fyannk/pgConsole/internal/observe"
)

var testNow = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

// outcomeOf finds one named check in a result.
func outcomeOf(t *testing.T, result diagnose.Result, name string) diagnose.Check {
	t.Helper()
	for _, check := range result.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("check %q did not account for itself", name)
	return diagnose.Check{}
}

// TestKubernetesEOLRidesTheObservedServerVersion walks the pinned rule
// through its three answers: matched on a retired version with the
// server's own report quoted, does-not-apply on a supported one, and
// could-not-run while the version is unobserved.
func TestKubernetesEOLRidesTheObservedServerVersion(t *testing.T) {
	t.Parallel()

	in := diagnose.Input{Now: testNow}
	if got := outcomeOf(t, diagnose.Run(in, Rules()...), "k8s-eol"); got.Outcome != diagnose.CheckUnavailable {
		t.Errorf("outcome = %v with no observed version, want could-not-run", got.Outcome)
	}

	in.HasKubeVersion = true
	in.KubeVersion = observe.KubeVersionSnapshot{GitVersion: "v1.32.4"}
	result := diagnose.Run(in, Rules()...)
	if got := outcomeOf(t, result, "k8s-eol"); got.Outcome != diagnose.CheckMatched {
		t.Fatalf("outcome = %v on 1.32.4, want matched", got.Outcome)
	}
	for _, finding := range result.Findings {
		if finding.ID != "k8s-eol" {
			continue
		}
		if !strings.Contains(finding.Evidence[0].Detail, "v1.32.4") {
			t.Errorf("the server's own report is not quoted: %+v", finding.Evidence)
		}
	}

	in.KubeVersion.GitVersion = "v1.34.0"
	if got := outcomeOf(t, diagnose.Run(in, Rules()...), "k8s-eol"); got.Outcome != diagnose.CheckNotApplicable {
		t.Errorf("outcome = %v on 1.34.0, want does-not-apply", got.Outcome)
	}
}

// TestCrashLoopCatchesTheSidecar proves the gap the container rules
// close: a crash-looping plugin sidecar produces a finding even though
// the instance container is healthy and no CloudNativePG status says
// anything.
func TestCrashLoopCatchesTheSidecar(t *testing.T) {
	t.Parallel()
	restarts := 12
	in := diagnose.Input{
		Now:     testNow,
		HasPods: true,
		Pods: observe.PodsSnapshot{Pods: []observe.PodFacts{{
			Name: "orders-1",
			Containers: []observe.ContainerFacts{
				{Name: "postgres", State: "running"},
				{Name: "plugin-barman-cloud", State: "waiting", Reason: "CrashLoopBackOff",
					Restarts: &restarts},
			},
		}}},
	}
	result := diagnose.Run(in, Rules()...)
	if got := outcomeOf(t, result, "k8s-container-crashloop"); got.Outcome != diagnose.CheckMatched {
		t.Fatalf("outcome = %v, want matched on the crash-looping sidecar", got.Outcome)
	}
	found := false
	for _, finding := range result.Findings {
		if finding.ID == "k8s-container-crashloop/orders-1/plugin-barman-cloud" {
			found = true
		}
	}
	if !found {
		t.Errorf("no finding names the sidecar: %+v", result.Findings)
	}
}

// TestMountFailureQuotesTheKubelet proves the event-backed mount rule
// carries the kubelet's message, which is where the missing Secret is
// named.
func TestMountFailureQuotesTheKubelet(t *testing.T) {
	t.Parallel()
	in := diagnose.Input{
		Now:       testNow,
		HasEvents: true,
		Events: observe.EventsSnapshot{Events: []observe.EventFacts{{
			Type: "Warning", Reason: "FailedMount", Kind: "Pod", Object: "orders-1",
			Message: `MountVolume.SetUp failed for volume "certs" : secret "orders-server" not found`,
		}}},
	}
	result := diagnose.Run(in, Rules()...)
	if got := outcomeOf(t, result, "k8s-volume-mount-failed"); got.Outcome != diagnose.CheckMatched {
		t.Fatalf("outcome = %v, want matched", got.Outcome)
	}
	for _, finding := range result.Findings {
		if finding.ID != "k8s-volume-mount-failed" {
			continue
		}
		if !strings.Contains(finding.Evidence[0].Detail, `secret "orders-server" not found`) {
			t.Errorf("kubelet message not quoted: %+v", finding.Evidence)
		}
	}
}
