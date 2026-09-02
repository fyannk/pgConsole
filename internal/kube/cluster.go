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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/fyannk/pgConsole/internal/history"
	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

// clusterGVR is the CloudNativePG Cluster resource.
var clusterGVR = schema.GroupVersionResource{
	Group:    "postgresql.cnpg.io",
	Version:  "v1",
	Resource: "clusters",
}

// Options configure the client for the one target cluster.
type Options struct {
	// Namespace is the target namespace.
	Namespace string
	// ClusterName is the one target cluster.
	ClusterName string
	// RequestTimeout bounds each individual API request.
	RequestTimeout time.Duration
	// LogTailLines bounds the lines of one log tail.
	LogTailLines int
	// LogTailMaxBytes bounds the bytes of one log tail.
	LogTailMaxBytes int64
	// AllowClusterCatalogs permits the one cluster-scoped read this
	// console can be granted: the ClusterImageCatalog its Cluster
	// references. False means the lookup is never attempted.
	AllowClusterCatalogs bool
	// Recorder receives the history capture of every accepted
	// observation. Nil disables capture entirely: no tap wraps any pump
	// and no seed is collected, so the disabled path holds no capture
	// code at all.
	Recorder history.Recorder
}

// Client accesses the one target cluster through the Kubernetes API.
// For the cluster itself it implements observe.Source with the access
// model's shape: the get is pinned to the configured name, the watch is
// scoped by a metadata.name field selector, and no cluster list is ever
// issued. Pods are label-selected and membership-verified.
type Client struct {
	dyn    dynamic.Interface
	typed  kubernetes.Interface
	opts   Options
	logger *slog.Logger
}

// New builds a Client from a REST configuration. Production wiring uses
// InClusterClient; tests inject the configuration of a test API server.
func New(cfg *rest.Config, opts Options, logger *slog.Logger) (*Client, error) {
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, redact.NewError("client build", redact.CategoryInternal, err)
	}
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, redact.NewError("client build", redact.CategoryInternal, err)
	}
	return &Client{dyn: dyn, typed: typed, opts: opts, logger: logger}, nil
}

// InClusterClient builds a Client from the pod's ServiceAccount. It is
// the only production path: kubeconfig files are not read.
func InClusterClient(opts Options, logger *slog.Logger) (*Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, redact.NewError("in-cluster config", redact.CategoryUnavailable, err)
	}
	return New(cfg, opts, logger)
}

// Fetch performs the pinned get. An absent cluster is a successful
// observation with Present false, never an error: absence is a fact the
// console renders.
func (c *Client) Fetch(ctx context.Context) (observe.ClusterState, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()
	obj, err := c.dyn.Resource(clusterGVR).Namespace(c.opts.Namespace).Get(ctx, c.opts.ClusterName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// An empty seed is itself a complete observation: a cluster known
		// to history and absent here was deleted while unobserved.
		c.seedRecord(scopeCluster).commit(true)
		return observe.ClusterState{Facts: observe.ClusterFacts{Present: false}}, nil
	}
	if err != nil {
		return observe.ClusterState{}, categorize("cluster get", err)
	}
	facts, err := convertCluster(obj.Object)
	if err != nil {
		return observe.ClusterState{}, err
	}
	seed := c.seedRecord(scopeCluster)
	seed.add(obj.Object)
	seed.commit(true)
	return observe.ClusterState{Facts: facts}, nil
}

// Watch starts a watch scoped to the configured name by field selector.
// The watch context is not bounded by the request timeout: a watch is
// long-lived by design and ends with the connection or the caller.
//
// It deliberately sends no resource version. The seed is a pinned get,
// and a single object's version is its last modification: on a cluster
// idle longer than the server's watch window it is already expired, and
// a watch resumed from it dies instantly with 410 Expired on every
// retry, freezing the console on a permanently stale snapshot. An unset
// version means "current state": the server re-delivers the object as
// one synthetic Added, which the collector folds harmlessly, then
// streams changes. The cost is a bounded blind spot — a deletion landing
// between the get and the watch start is unseen until the next re-seed —
// which is the same window every contact loss already owns.
func (c *Client) Watch(ctx context.Context) (observe.Watch, error) {
	w, err := c.dyn.Resource(clusterGVR).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector:       "metadata.name=" + c.opts.ClusterName,
		AllowWatchBookmarks: false,
	})
	if err != nil {
		return nil, categorize("cluster watch", err)
	}
	items, stop := fanIn(ctx,
		[]watch.Interface{w},
		[]pump[observe.ClusterState]{tap(c, scopeCluster, pumpCluster)})
	return resultStream[observe.ClusterState]{stream[observe.ClusterState]{items: items, stop: stop}}, nil
}

// pumpCluster converts one Cluster watch event. A malformed or error
// event ends the watch: the collector then re-seeds with a fresh get,
// which is the safe interpretation of an undecodable stream. A deletion
// is a complete observation in its own right — absence is a fact the
// console renders, not an error.
func pumpCluster(event watch.Event) (observe.ClusterState, bool, bool) {
	switch event.Type {
	case watch.Added, watch.Modified:
		obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
		if !ok {
			return observe.ClusterState{}, false, true
		}
		facts, err := convertCluster(obj.UnstructuredContent())
		if err != nil {
			return observe.ClusterState{}, false, true
		}
		return observe.ClusterState{Facts: facts}, true, false
	case watch.Deleted:
		return observe.ClusterState{Facts: observe.ClusterFacts{Present: false}}, true, false
	case watch.Bookmark:
		return observe.ClusterState{}, false, false
	default:
		return observe.ClusterState{}, false, true
	}
}

// categorize translates a Kubernetes client error into a categorized,
// output-safe error at this boundary. The raw error, which may embed
// the request URL, survives only as an unwrappable cause.
func categorize(op string, err error) error {
	switch {
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return redact.NewError(op, redact.CategoryForbidden, err)
	case apierrors.IsNotFound(err):
		return redact.NewError(op, redact.CategoryNotFound, err)
	case apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err):
		return redact.NewError(op, redact.CategoryTimeout, err)
	case redact.Categorize(err) != redact.CategoryInternal:
		return redact.NewError(op, redact.Categorize(err), err)
	default:
		return redact.NewError(op, redact.CategoryInternal, err)
	}
}
