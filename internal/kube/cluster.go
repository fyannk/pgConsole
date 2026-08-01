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
		return observe.ClusterState{Facts: observe.ClusterFacts{Present: false}}, nil
	}
	if err != nil {
		return observe.ClusterState{}, categorize("cluster get", err)
	}
	facts, err := convertCluster(obj.Object)
	if err != nil {
		return observe.ClusterState{}, err
	}
	return observe.ClusterState{Facts: facts, ResourceVersion: obj.GetResourceVersion()}, nil
}

// Watch starts a watch scoped to the configured name by field selector.
// The watch context is not bounded by the request timeout: a watch is
// long-lived by design and ends with the connection or the caller.
func (c *Client) Watch(ctx context.Context, fromResourceVersion string) (observe.Watch, error) {
	w, err := c.dyn.Resource(clusterGVR).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector:       "metadata.name=" + c.opts.ClusterName,
		ResourceVersion:     fromResourceVersion,
		AllowWatchBookmarks: false,
	})
	if err != nil {
		return nil, categorize("cluster watch", err)
	}
	items, stop := fanIn(ctx,
		[]watch.Interface{w},
		[]pump[observe.ClusterState]{pumpCluster})
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
		rv := ""
		if meta, ok := event.Object.(metav1.Object); ok {
			rv = meta.GetResourceVersion()
		}
		return observe.ClusterState{Facts: facts, ResourceVersion: rv}, true, false
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
