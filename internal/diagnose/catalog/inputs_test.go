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
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fyannk/pgConsole/internal/diagnose"
	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/history"
	"github.com/fyannk/pgConsole/internal/logstream"
	"github.com/fyannk/pgConsole/internal/metrics"
	"github.com/fyannk/pgConsole/internal/observe"
)

var now = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

// fixtureWindow is a MetricsWindow serving instant flags and a short
// run of series samples per instance. The run is three samples ten
// minutes apart, so a rule asking for a value held across a window
// sees more than one of them inside it.
type fixtureWindow struct {
	instants map[string]map[string]metrics.Instant
	series   map[string]map[string]float64
}

// fixtureSampleTimes are the sample instants, oldest first. They sit
// inside the tightest window any rule asks a value to hold for, so a
// sustained-threshold rule sees a run rather than a lone sample it
// cannot judge.
var fixtureSampleTimes = []int64{
	now.Add(-20 * time.Minute).Unix(), now.Add(-10 * time.Minute).Unix(), now.Unix(),
}

func (w fixtureWindow) Interval() time.Duration { return metrics.DefaultInterval }

func (w fixtureWindow) Instances() []string { return nil }

func (w fixtureWindow) Range(key string, tier metrics.Tier) ([]int64, map[string][]*float64) {
	if tier != metrics.TierRaw {
		return nil, nil
	}
	out := map[string][]*float64{}
	for instance, byKey := range w.series {
		value, ok := byKey[key]
		if !ok {
			continue
		}
		column := make([]*float64, len(fixtureSampleTimes))
		for i := range column {
			held := value
			column[i] = &held
		}
		out[instance] = column
	}
	return fixtureSampleTimes, out
}

func (w fixtureWindow) InstantReadings() map[string]map[string]metrics.Instant { return w.instants }

type fixtureLogs []logstream.Observation

func (l fixtureLogs) Observations() []logstream.Observation { return l }

// everythingObserved is an input in which every source the console
// publishes is present, fresh, and carries something a check reacts
// to. It is the fixture the consumption test withholds from, one source
// at a time.
func everythingObserved() diagnose.Input {
	major, desired := 17, 3
	two := int32(2)
	applied := false
	return diagnose.Input{
		Now:        now,
		HasCluster: true,
		Cluster: observe.Snapshot{Cluster: observe.ClusterFacts{
			Present: true, PostgresMajorVersion: &major, DesiredInstances: &desired,
			Phase: "Not enough disk space", CurrentPrimary: "orders-1", TargetPrimary: "orders-1",
			ImageCatalogRef: &observe.ImageCatalogRef{Kind: "ImageCatalog", Name: "absent"},
		}},
		HasPods: true,
		Pods: observe.PodsSnapshot{Pods: []observe.PodFacts{{
			Name: "orders-1",
			Containers: []observe.ContainerFacts{
				{Name: "bootstrap-controller", Init: true, Image: "ghcr.io/cloudnative-pg/cloudnative-pg:1.30.0"},
				{Name: "postgres", Image: "ghcr.io/cloudnative-pg/postgresql:17.5", State: "waiting", Reason: "CrashLoopBackOff"},
				{Name: "plugin-barman-cloud", Image: "ghcr.io/cloudnative-pg/plugin-barman-cloud-sidecar:v0.5.0"},
			},
		}}},
		HasEvents: true,
		Events: observe.EventsSnapshot{Events: []observe.EventFacts{{
			Kind: "Pod", Object: "orders-2", Type: "Warning", Reason: "Evicted",
			Message: "The node was low on resource: memory", Count: 1, LastSeen: now}}},
		HasBackups: true,
		Backups: observe.BackupsSnapshot{Backups: []observe.BackupFacts{{
			Name: "orders-20260810", Phase: "failed", CreatedAt: now.Add(-time.Hour)}}},
		HasInfrastructure: true,
		Infrastructure: observe.InfrastructureSnapshot{Volumes: []observe.VolumeFacts{{
			Name: "orders-2", Phase: "Pending"}}},
		HasKubeVersion: true,
		KubeVersion:    observe.KubeVersionSnapshot{GitVersion: "v1.33.2"},
		HasQuotas:      true,
		Quotas: observe.QuotasSnapshot{Quotas: []observe.QuotaFacts{{Name: "tight",
			Resources: []observe.QuotaResourceFacts{{Resource: "pods", Hard: "3", Used: "3", Exhausted: true}}}}},
		HasPoolers: true,
		Poolers: observe.PoolersSnapshot{Poolers: []observe.PoolerFacts{{
			Name: "orders-rw", DesiredInstances: &two, ReadyInstances: 1}}},
		HasPoolerPods: true,
		PoolerPods: observe.PodsSnapshot{Pods: []observe.PodFacts{{
			Name:       "orders-rw-1",
			Containers: []observe.ContainerFacts{{Name: "pgbouncer", State: "waiting", Reason: "CrashLoopBackOff"}},
		}}},
		HasFailoverQuorum: true,
		FailoverQuorum: observe.FailoverQuorumSnapshot{Quorum: observe.FailoverQuorumFacts{
			Present: true, StandbyNumber: 2, Standbys: []string{"orders-2"}}},
		HasImageCatalogs:   true,
		ImageCatalogs:      observe.ImageCatalogsSnapshot{},
		HasDatabaseObjects: true,
		DatabaseObjects: observe.DatabaseObjectsSnapshot{Databases: []observe.DatabaseFacts{{
			Name: "app", Declared: observe.Declared{Applied: &applied}}}},
		HasHistory: true,
		// A pod destroyed and remade three times, and a definition five
		// writers deep — the two shapes the timeline checks count.
		History: history.Snapshot{Entries: append(
			replacements("orders-3", 3),
			rewrites("orders", 5)...)},
		HasEvidence: true,
		Evidence: evidence.Status{HasReport: true, Snapshot: evidence.Snapshot{Report: evidence.Report{
			Completeness: "complete", ScopeName: "orders", EvidenceGeneration: 3,
			Barman: &evidence.BarmanFacts{WAL: evidence.StateFact{State: "unhealthy", Code: "wal-gap-confirmed"}},
		}}},
		Metrics: fixtureWindow{
			instants: map[string]map[string]metrics.Instant{
				"orders-1": {
					"fencing-on":             {At: now.Unix(), Value: 1},
					"in-recovery":            {At: now.Unix(), Value: 0},
					"wal-receiver-up":        {At: now.Unix(), Value: 1},
					"sync-replicas-expected": {At: now.Unix(), Value: 2},
					"sync-replicas-observed": {At: now.Unix(), Value: 1},
				},
				// The replica that stopped streaming: in recovery, no
				// receiver, and a lag that holds. All three readings are
				// on this instance, which is what the corroborating rule
				// requires.
				"orders-2": {
					"in-recovery":     {At: now.Unix(), Value: 1},
					"wal-receiver-up": {At: now.Unix(), Value: 0},
				},
			},
			series: map[string]map[string]float64{
				"orders-1": {"slot-retained-bytes": 40 << 30, "max-tx-duration": 7200},
				"orders-2": {"replication-lag": 900},
			},
		},
		PoolerMetrics: fixtureWindow{series: map[string]map[string]float64{"orders-rw-1": {"maxwait": 12}}},
		Logs: fixtureLogs{{RuleID: "cnpg-wal-disk-full", Pod: "orders-1", Container: "postgres",
			Line: "no free disk space for WALs", FirstSeen: now.Add(-time.Minute), LastSeen: now, Count: 4}},
	}
}

// replacements is one pod name observed under several identities.
func replacements(name string, times int) []history.Entry {
	entries := make([]history.Entry, 0, times)
	for i := range times {
		entries = append(entries, history.Entry{
			Kind: "Pod", Name: name, UID: fmt.Sprintf("uid-%d", i),
			Change:     history.ChangeCreated,
			ObservedAt: now.Add(-time.Duration(times-i) * 10 * time.Minute),
		})
	}
	return entries
}

// rewrites is one object's definition written repeatedly, by two
// managers in turn.
func rewrites(name string, times int) []history.Entry {
	managers := [...]string{"argocd-controller", "mutating-webhook"}
	entries := make([]history.Entry, 0, times)
	for i := range times {
		entries = append(entries, history.Entry{
			Kind: "Cluster", Name: name, UID: "cluster-uid",
			Change:     history.ChangeSpec,
			Actor:      history.Actor{Manager: managers[i%len(managers)]},
			ObservedAt: now.Add(-time.Duration(times-i) * 5 * time.Minute),
		})
	}
	return entries
}

// TestEveryInputSourceIsConsumed is the guard against a source that is
// plumbed into the engine and read by nothing. For every source on
// diagnose.Input — each Has* flag, each interface — withholding it from
// an input in which everything is observed must change what the run
// reports: a check that could not run, or a finding that is no longer
// made. A source whose absence changes nothing is dead weight the
// handler gathers for no reader, and this fails on it by name.
func TestEveryInputSourceIsConsumed(t *testing.T) {
	t.Parallel()
	baseline := summarize(diagnose.Run(everythingObserved(), Rules()...))
	if strings.Contains(baseline, "could not run") {
		t.Fatalf("the fixture leaves a check unable to run, so withholding cannot be told apart:\n%s", baseline)
	}
	value := reflect.ValueOf(everythingObserved())
	for i := range value.NumField() {
		field := value.Type().Field(i)
		withhold := reflect.New(value.Type()).Elem()
		withhold.Set(value)
		switch {
		case strings.HasPrefix(field.Name, "Has") && field.Type.Kind() == reflect.Bool:
			withhold.Field(i).SetBool(false)
		case field.Type.Kind() == reflect.Interface:
			withhold.Field(i).Set(reflect.Zero(field.Type))
		default:
			continue
		}
		in, ok := withhold.Interface().(diagnose.Input)
		if !ok {
			t.Fatal("reflected value is not an Input")
		}
		if got := summarize(diagnose.Run(in, Rules()...)); got == baseline {
			t.Errorf("withholding %s changes nothing: no check reads that source", field.Name)
		}
	}
}

// summarize renders a run as one comparable string: every check with
// its outcome, and every finding by ID.
func summarize(result diagnose.Result) string {
	lines := make([]string, 0, len(result.Checks)+len(result.Findings))
	for _, check := range result.Checks {
		lines = append(lines, fmt.Sprintf("check %s: %s", check.Name, check.Outcome))
	}
	for _, finding := range result.Findings {
		lines = append(lines, "finding "+finding.ID)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
