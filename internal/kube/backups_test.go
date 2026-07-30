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
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/fyannk/pgconsole/internal/observe"
)

func rawBackup(name, cluster, method, phase string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Backup",
		"metadata": map[string]any{"name": name, "namespace": "payments", "uid": "u-" + name, "creationTimestamp": "2026-07-28T10:00:00Z"},
		"spec":     map[string]any{"cluster": map[string]any{"name": cluster}, "method": method},
		"status":   map[string]any{"phase": phase, "method": method, "startedAt": "2026-07-28T10:01:00Z", "stoppedAt": "2026-07-28T10:02:00Z"},
	}}
}

func rawSchedule(name, cluster string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "ScheduledBackup",
		"metadata": map[string]any{"name": name, "namespace": "payments", "uid": "u-" + name, "creationTimestamp": "2026-07-28T09:00:00Z"},
		"spec":     map[string]any{"cluster": map[string]any{"name": cluster}, "method": "plugin", "schedule": "0 0 2 * * *", "suspend": false},
		"status":   map[string]any{"lastScheduleTime": "2026-07-28T10:00:00Z", "nextScheduleTime": "2026-07-29T10:00:00Z"},
	}}
}

func rawClusterWithObjectStore(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]any{"name": "orders", "namespace": "payments"},
		"spec": map[string]any{"instances": int64(3), "plugins": []any{map[string]any{
			"name": barmanPluginName, "parameters": map[string]any{barmanObjectName: name},
		}}},
	}}
}

func TestConvertBackupAndScheduleRequireExactClusterReference(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)
	facts, member, err := c.convertBackup(rawBackup("b1", "orders", "volumeSnapshot", "completed").Object)
	if err != nil || !member {
		t.Fatalf("target Backup conversion: member=%v err=%v", member, err)
	}
	if facts.Method != "volumeSnapshot" || facts.Phase != "completed" || facts.StartedAt == nil || facts.StoppedAt == nil {
		t.Fatalf("Backup facts incomplete: %+v", facts)
	}
	_, member, err = c.convertBackup(rawBackup("foreign", "other", "plugin", "completed").Object)
	if err != nil || member {
		t.Fatalf("foreign Backup membership: member=%v err=%v", member, err)
	}
	schedule, member, err := c.convertScheduledBackup(rawSchedule("daily", "orders").Object)
	if err != nil || !member || schedule.Method != "plugin" || schedule.Suspended == nil || *schedule.Suspended {
		t.Fatalf("target ScheduledBackup conversion: member=%v facts=%+v err=%v", member, schedule, err)
	}
	_, member, err = c.convertScheduledBackup(rawSchedule("foreign-daily", "other").Object)
	if err != nil || member {
		t.Fatalf("foreign ScheduledBackup membership: member=%v err=%v", member, err)
	}
}

func TestConvertBackupCarriesCorrelationEvidence(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t)

	plugin := rawBackup("b-plugin", "orders", "plugin", "completed")
	plugin.Object["spec"].(map[string]any)["pluginConfiguration"] = map[string]any{"name": barmanPluginName}
	plugin.Object["status"].(map[string]any)["backupId"] = "20260729T060000"
	facts, member, err := c.convertBackup(plugin.Object)
	if err != nil || !member {
		t.Fatalf("plugin Backup conversion: member=%v err=%v", member, err)
	}
	if facts.BackupID != "20260729T060000" || facts.PluginName != barmanPluginName {
		t.Errorf("correlation facts = %q %q", facts.BackupID, facts.PluginName)
	}

	snapshot, member, err := c.convertBackup(rawBackup("b-vs", "orders", "volumeSnapshot", "completed").Object)
	if err != nil || !member {
		t.Fatalf("volumeSnapshot Backup conversion: member=%v err=%v", member, err)
	}
	if snapshot.BackupID != "" || snapshot.PluginName != "" {
		t.Errorf("unreported correlation facts must stay empty: %q %q", snapshot.BackupID, snapshot.PluginName)
	}
}

func TestFetchBackupCatalogObjectStoreForbiddenDegradesOnlyReference(t *testing.T) {
	t.Parallel()
	const canary = "credential-bearing-canary"
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		backupGVR: "BackupList", scheduledBackupGVR: "ScheduledBackupList",
	}
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds,
		rawBackup("orders-good", "orders", "plugin", "completed"),
		rawBackup("foreign", "other", "plugin", "completed"),
		rawSchedule("daily", "orders"), rawSchedule("foreign-daily", "other"),
		rawClusterWithObjectStore("orders-store"),
	)
	dyn.PrependReactor("get", "objectstores", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: objectStoreGVR.Group, Resource: objectStoreGVR.Resource}, "orders-store", errors.New(canary))
	})
	logs := &bytes.Buffer{}
	c := &Client{dyn: dyn, opts: Options{Namespace: "payments", ClusterName: "orders", RequestTimeout: time.Second}, logger: slog.New(slog.NewJSONHandler(logs, nil))}
	state, err := c.FetchBackupCatalog(context.Background())
	if err != nil {
		t.Fatalf("FetchBackupCatalog: %v", err)
	}
	if len(state.Backups) != 1 || state.Backups[0].Name != "orders-good" || len(state.ScheduledBackups) != 1 {
		t.Fatalf("cluster selection failed: %+v", state)
	}
	if state.ObjectStore.Name != "orders-store" || state.ObjectStore.State != observe.ObjectStoreUnknown {
		t.Fatalf("forbidden lookup did not become isolated unknown: %+v", state.ObjectStore)
	}
	if strings.Contains(logs.String(), canary) || !strings.Contains(logs.String(), "forbidden") {
		t.Fatalf("ObjectStore log not safely categorized: %s", logs.String())
	}
}

func TestObjectStoreNameHonorsEnabledPlugin(t *testing.T) {
	t.Parallel()
	cluster := rawClusterWithObjectStore("orders-store")
	name, err := objectStoreName(cluster.Object)
	if err != nil || name != "orders-store" {
		t.Fatalf("objectStoreName = %q, %v", name, err)
	}
	plugins := cluster.Object["spec"].(map[string]any)["plugins"].([]any)
	plugins[0].(map[string]any)["enabled"] = false
	name, err = objectStoreName(cluster.Object)
	if err != nil || name != "" {
		t.Fatalf("disabled plugin reference = %q, %v", name, err)
	}
}

func TestBackupCatalogCandidateTraversalBounded(t *testing.T) {
	t.Parallel()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		backupGVR: "BackupList", scheduledBackupGVR: "ScheduledBackupList",
	})
	counts := map[string]int{}
	dyn.PrependReactor("list", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		resource := action.GetResource().Resource
		counts[resource]++
		kind := "Backup"
		if resource == scheduledBackupGVR.Resource {
			kind = "ScheduledBackup"
		}
		items := make([]unstructured.Unstructured, backupListPageSize)
		for i := range items {
			items[i] = unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "postgresql.cnpg.io/v1", "kind": kind,
				"metadata": map[string]any{"name": "foreign", "namespace": "payments"},
				"spec":     map[string]any{"cluster": map[string]any{"name": "other"}, "schedule": "0 0 2 * * *"},
			}}
		}
		list := &unstructured.UnstructuredList{Items: items}
		list.SetContinue("more")
		list.SetResourceVersion("10")
		return true, list, nil
	})
	c := &Client{dyn: dyn, opts: Options{Namespace: "payments", ClusterName: "orders", RequestTimeout: time.Second}, logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))}
	backups, _, truncated, err := c.listBackups(context.Background())
	if err != nil || len(backups) != 0 || !truncated || counts[backupGVR.Resource] != maxBackupCandidates/backupListPageSize {
		t.Fatalf("Backup ceiling: rows=%d truncated=%v calls=%d err=%v", len(backups), truncated, counts[backupGVR.Resource], err)
	}
	schedules, _, truncated, err := c.listScheduledBackups(context.Background())
	if err != nil || len(schedules) != 0 || !truncated || counts[scheduledBackupGVR.Resource] != maxScheduledCandidates/backupListPageSize {
		t.Fatalf("ScheduledBackup ceiling: rows=%d truncated=%v calls=%d err=%v", len(schedules), truncated, counts[scheduledBackupGVR.Resource], err)
	}
}

func TestWatchBackupCatalogFiltersAndMergesKinds(t *testing.T) {
	t.Parallel()
	dyn := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		backupGVR: "BackupList", scheduledBackupGVR: "ScheduledBackupList",
	})
	c := &Client{dyn: dyn, opts: Options{Namespace: "payments", ClusterName: "orders", RequestTimeout: time.Second}, logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w, err := c.WatchBackupCatalog(ctx, observe.BackupCatalogState{})
	if err != nil {
		t.Fatalf("WatchBackupCatalog: %v", err)
	}
	defer w.Stop()

	for _, obj := range []*unstructured.Unstructured{
		rawBackup("foreign", "other", "plugin", "completed"),
		rawBackup("target", "orders", "plugin", "running"),
	} {
		if _, err := dyn.Resource(backupGVR).Namespace("payments").Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create Backup: %v", err)
		}
	}
	change := <-w.Changes()
	if change.PutBackup == nil || change.PutBackup.Name != "target" {
		t.Fatalf("merged watch delivered wrong Backup change: %+v", change)
	}
	if _, err := dyn.Resource(scheduledBackupGVR).Namespace("payments").Create(ctx, rawSchedule("daily", "orders"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create ScheduledBackup: %v", err)
	}
	change = <-w.Changes()
	if change.PutScheduledBackup == nil || change.PutScheduledBackup.Name != "daily" {
		t.Fatalf("merged watch delivered wrong ScheduledBackup change: %+v", change)
	}
}
