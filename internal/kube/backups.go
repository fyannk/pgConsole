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
	"log/slog"
	"sync"
	"time"

	apiv1 "github.com/cloudnative-pg/api/pkg/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgconsole/internal/observe"
	"github.com/fyannk/pgconsole/internal/redact"
)

var (
	backupGVR          = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "backups"}
	scheduledBackupGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "scheduledbackups"}
	objectStoreGVR     = schema.GroupVersionResource{Group: "barmancloud.cnpg.io", Version: "v1", Resource: "objectstores"}
)

const (
	backupListPageSize     = 200
	maxBackupCandidates    = 2000
	maxScheduledCandidates = 1000
	barmanPluginName       = "barman-cloud.cloudnative-pg.io"
	barmanObjectName       = "barmanObjectName"
)

// FetchBackupCatalog lists both namespaced resource kinds, selecting the
// configured Cluster by spec.cluster.name. The optional ObjectStore get is
// metadata-only and non-fatal: its failure becomes one unknown claim.
func (c *Client) FetchBackupCatalog(ctx context.Context) (observe.BackupCatalogState, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	backups, backupRV, backupsTruncated, err := c.listBackups(ctx)
	if err != nil {
		return observe.BackupCatalogState{}, err
	}
	schedules, scheduleRV, schedulesTruncated, err := c.listScheduledBackups(ctx)
	if err != nil {
		return observe.BackupCatalogState{}, err
	}

	return observe.BackupCatalogState{
		Backups:                  backups,
		ScheduledBackups:         schedules,
		ObjectStore:              c.fetchObjectStoreReference(ctx),
		BackupsTruncated:         backupsTruncated,
		SchedulesTruncated:       schedulesTruncated,
		BackupResourceVersion:    backupRV,
		ScheduledResourceVersion: scheduleRV,
	}, nil
}

func (c *Client) listBackups(ctx context.Context) ([]observe.BackupFacts, string, bool, error) {
	var backups []observe.BackupFacts
	examined := 0
	opts := metav1.ListOptions{Limit: backupListPageSize}
	rv := ""
	for {
		list, err := c.dyn.Resource(backupGVR).Namespace(c.opts.Namespace).List(ctx, opts)
		if err != nil {
			return nil, "", false, categorize("backups list", err)
		}
		rv = list.GetResourceVersion()
		for i := range list.Items {
			facts, member, err := c.convertBackup(list.Items[i].Object)
			if err != nil {
				return nil, "", false, err
			}
			if member {
				backups = append(backups, facts)
			}
		}
		examined += len(list.Items)
		if list.GetContinue() == "" {
			break
		}
		if len(backups) > observe.MaxBackups || examined >= maxBackupCandidates {
			return backups, rv, true, nil
		}
		opts.Continue = list.GetContinue()
	}
	return backups, rv, len(backups) > observe.MaxBackups, nil
}

func (c *Client) listScheduledBackups(ctx context.Context) ([]observe.ScheduledBackupFacts, string, bool, error) {
	var schedules []observe.ScheduledBackupFacts
	examined := 0
	opts := metav1.ListOptions{Limit: backupListPageSize}
	rv := ""
	for {
		list, err := c.dyn.Resource(scheduledBackupGVR).Namespace(c.opts.Namespace).List(ctx, opts)
		if err != nil {
			return nil, "", false, categorize("scheduled backups list", err)
		}
		rv = list.GetResourceVersion()
		for i := range list.Items {
			facts, member, err := c.convertScheduledBackup(list.Items[i].Object)
			if err != nil {
				return nil, "", false, err
			}
			if member {
				schedules = append(schedules, facts)
			}
		}
		examined += len(list.Items)
		if list.GetContinue() == "" {
			break
		}
		if len(schedules) > observe.MaxScheduledBackups || examined >= maxScheduledCandidates {
			return schedules, rv, true, nil
		}
		opts.Continue = list.GetContinue()
	}
	return schedules, rv, len(schedules) > observe.MaxScheduledBackups, nil
}

func (c *Client) fetchObjectStoreReference(ctx context.Context) observe.ObjectStoreReference {
	unknown := observe.ObjectStoreReference{State: observe.ObjectStoreUnknown}
	cluster, err := c.dyn.Resource(clusterGVR).Namespace(c.opts.Namespace).Get(ctx, c.opts.ClusterName, metav1.GetOptions{})
	if err != nil {
		c.logObjectStoreUnavailable(err)
		return unknown
	}
	name, err := objectStoreName(cluster.Object)
	if err != nil {
		c.logObjectStoreUnavailable(err)
		return unknown
	}
	if name == "" {
		return observe.ObjectStoreReference{State: observe.ObjectStoreNotReferenced}
	}
	ref := observe.ObjectStoreReference{Name: name, State: observe.ObjectStoreUnknown}
	if _, err := c.dyn.Resource(objectStoreGVR).Namespace(c.opts.Namespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
		c.logObjectStoreUnavailable(err)
		return ref
	}
	ref.State = observe.ObjectStorePresent
	return ref
}

func (c *Client) logObjectStoreUnavailable(err error) {
	category := redact.Safe(categorize("object store get", err))
	c.logger.Info("object store reference unavailable", slog.String("category", category))
}

func objectStoreName(content map[string]any) (string, error) {
	var cluster apiv1.Cluster
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &cluster); err != nil {
		return "", redact.NewError("cluster plugin convert", redact.CategoryInternal, err)
	}
	for _, plugin := range cluster.Spec.Plugins {
		if plugin.Name != barmanPluginName || (plugin.Enabled != nil && !*plugin.Enabled) {
			continue
		}
		return plugin.Parameters[barmanObjectName], nil
	}
	return "", nil
}

func (c *Client) convertBackup(content map[string]any) (observe.BackupFacts, bool, error) {
	var backup apiv1.Backup
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &backup); err != nil {
		return observe.BackupFacts{}, false, redact.NewError("backup convert", redact.CategoryInternal, err)
	}
	method := backup.Status.Method
	if method == "" {
		method = backup.Spec.Method
	}
	facts := observe.BackupFacts{
		Name: backup.Name, UID: string(backup.UID), Phase: string(backup.Status.Phase),
		Method: string(method), BackupID: backup.Status.BackupID,
		CreatedAt: backup.CreationTimestamp.Time.UTC(),
		StartedAt: metaTime(backup.Status.StartedAt), StoppedAt: metaTime(backup.Status.StoppedAt),
	}
	if backup.Spec.PluginConfiguration != nil {
		facts.PluginName = backup.Spec.PluginConfiguration.Name
	}
	return facts, backup.Namespace == c.opts.Namespace && backup.Spec.Cluster.Name == c.opts.ClusterName, nil
}

func (c *Client) convertScheduledBackup(content map[string]any) (observe.ScheduledBackupFacts, bool, error) {
	var schedule apiv1.ScheduledBackup
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, &schedule); err != nil {
		return observe.ScheduledBackupFacts{}, false, redact.NewError("scheduled backup convert", redact.CategoryInternal, err)
	}
	facts := observe.ScheduledBackupFacts{
		Name: schedule.Name, UID: string(schedule.UID), Method: string(schedule.Spec.Method),
		Schedule: schedule.Spec.Schedule, Suspended: schedule.Spec.Suspend,
		CreatedAt:        schedule.CreationTimestamp.Time.UTC(),
		LastScheduleTime: metaTime(schedule.Status.LastScheduleTime),
		NextScheduleTime: metaTime(schedule.Status.NextScheduleTime),
	}
	return facts, schedule.Namespace == c.opts.Namespace && schedule.Spec.Cluster.Name == c.opts.ClusterName, nil
}

func metaTime(value *metav1.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	t := value.Time.UTC()
	return &t
}

// WatchBackupCatalog merges one watch for each resource kind. Either stream
// ending terminates the merged watch so the collector re-lists both kinds and
// republishes only a complete catalog generation.
func (c *Client) WatchBackupCatalog(ctx context.Context, state observe.BackupCatalogState) (observe.BackupWatch, error) {
	backups, err := c.dyn.Resource(backupGVR).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{ResourceVersion: state.BackupResourceVersion})
	if err != nil {
		return nil, categorize("backups watch", err)
	}
	schedules, err := c.dyn.Resource(scheduledBackupGVR).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{ResourceVersion: state.ScheduledResourceVersion})
	if err != nil {
		backups.Stop()
		return nil, categorize("scheduled backups watch", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	w := &backupWatch{
		client: c, ctx: watchCtx, cancel: cancel,
		backups: backups, schedules: schedules, changes: make(chan observe.BackupChange),
	}
	w.wg.Add(2)
	go w.pumpBackups()
	go w.pumpSchedules()
	go func() {
		w.wg.Wait()
		close(w.changes)
	}()
	return w, nil
}

type backupWatch struct {
	client    *Client
	ctx       context.Context
	cancel    context.CancelFunc
	backups   watch.Interface
	schedules watch.Interface
	changes   chan observe.BackupChange
	once      sync.Once
	wg        sync.WaitGroup
}

func (w *backupWatch) Changes() <-chan observe.BackupChange { return w.changes }

func (w *backupWatch) Stop() {
	w.once.Do(func() {
		w.cancel()
		w.backups.Stop()
		w.schedules.Stop()
	})
}

func (w *backupWatch) pumpBackups() {
	defer w.wg.Done()
	defer w.Stop()
	for event := range w.backups.ResultChan() {
		obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
		if !ok {
			return
		}
		facts, member, err := w.client.convertBackup(obj.UnstructuredContent())
		if err != nil {
			return
		}
		if !member {
			continue
		}
		var change observe.BackupChange
		switch event.Type {
		case watch.Added, watch.Modified:
			change.PutBackup = &facts
		case watch.Deleted:
			change.DeleteBackup = &observe.BackupDeletion{Name: facts.Name, UID: facts.UID}
		case watch.Bookmark:
			continue
		default:
			return
		}
		if !w.send(change) {
			return
		}
	}
}

func (w *backupWatch) pumpSchedules() {
	defer w.wg.Done()
	defer w.Stop()
	for event := range w.schedules.ResultChan() {
		obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
		if !ok {
			return
		}
		facts, member, err := w.client.convertScheduledBackup(obj.UnstructuredContent())
		if err != nil {
			return
		}
		if !member {
			continue
		}
		var change observe.BackupChange
		switch event.Type {
		case watch.Added, watch.Modified:
			change.PutScheduledBackup = &facts
		case watch.Deleted:
			change.DeleteScheduledBackup = &observe.ScheduledBackupDeletion{Name: facts.Name, UID: facts.UID}
		case watch.Bookmark:
			continue
		default:
			return
		}
		if !w.send(change) {
			return
		}
	}
}

func (w *backupWatch) send(change observe.BackupChange) bool {
	select {
	case <-w.ctx.Done():
		return false
	case w.changes <- change:
		return true
	}
}
