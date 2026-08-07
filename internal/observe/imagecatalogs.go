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

package observe

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Image catalog bounds. A catalog carries at most eight images by the
// CRD's own validation; the ceilings here bound the namespace's catalogs
// and guard against a resource that predates that validation.
const (
	// MaxImageCatalogs bounds retained and rendered catalogs.
	MaxImageCatalogs = 32
	// MaxCatalogImages bounds the images rendered from one catalog.
	MaxCatalogImages = 16
)

// ClusterCatalogState is the outcome of the optional cluster-scoped
// catalog lookup. A ClusterImageCatalog lives outside the namespace this
// console is otherwise confined to, so every way the lookup can decline
// to produce content is named rather than collapsed into an absence.
type ClusterCatalogState string

const (
	// ClusterCatalogDisabled means the deployment did not opt in. The
	// reference is shown and its content is not claimed.
	ClusterCatalogDisabled ClusterCatalogState = "disabled"
	// ClusterCatalogNotReferenced means the cluster draws its image from
	// a namespaced catalog, or names it directly.
	ClusterCatalogNotReferenced ClusterCatalogState = "not-referenced"
	// ClusterCatalogPresent means the referenced catalog was read.
	ClusterCatalogPresent ClusterCatalogState = "present"
	// ClusterCatalogAbsent means the API server confirmed the referenced
	// catalog does not exist.
	ClusterCatalogAbsent ClusterCatalogState = "absent"
	// ClusterCatalogUnknown means the lookup could not be made. A denied
	// ClusterRole binding and a missing CRD both land here: the console
	// says it could not look, never that the catalog is not there.
	ClusterCatalogUnknown ClusterCatalogState = "unknown"
)

// CatalogImageFacts is one image a catalog offers.
type CatalogImageFacts struct {
	// Major is the PostgreSQL major version the image provides.
	Major int
	// Image is the image reference.
	Image string
}

// ImageCatalogFacts is the operator-reported content of one
// ImageCatalog.
//
// A catalog is namespace-shared infrastructure: it is not owned by a
// cluster, clusters reference it. So unlike every other collection this
// package retains, the set is not filtered to the target cluster at the
// source — the console renders only the catalog the cluster's
// spec.imageCatalogRef names, and that resolution happens at render,
// where the cluster snapshot is available and a changed reference is
// picked up without restarting a watch.
type ImageCatalogFacts struct {
	// Name is the resource name.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// Images is the bounded list the catalog offers, major-ascending.
	Images []CatalogImageFacts
	// ImagesTruncated reports that the catalog carried more images than
	// the display bound.
	ImagesTruncated bool
	// CreatedAt is the resource creation time.
	CreatedAt time.Time
}

// ImageCatalogDeletion identifies a removed catalog incarnation.
type ImageCatalogDeletion struct {
	// Name is the removed resource name.
	Name string
	// UID is the removed incarnation.
	UID string
}

// ImageCatalogChange is one change from the catalog watch. Exactly one
// field is set.
type ImageCatalogChange struct {
	// Put upserts one catalog.
	Put *ImageCatalogFacts
	// Delete removes one catalog incarnation.
	Delete *ImageCatalogDeletion
}

// ImageCatalogWatch is a running watch on the namespace's catalogs.
type ImageCatalogWatch interface {
	// Changes streams changes until the watch ends.
	Changes() <-chan ImageCatalogChange
	// Stop releases the watch.
	Stop()
}

// ImageCatalogsState is one complete seed: the namespace's catalogs,
// the optional cluster-scoped one, and the version the watch resumes
// from.
type ImageCatalogsState struct {
	// Catalogs is the bounded namespaced catalog seed.
	Catalogs []ImageCatalogFacts
	// ResourceVersion starts the namespaced watch.
	ResourceVersion string
	// Truncated reports a source safety ceiling.
	Truncated bool
	// ClusterCatalog is the referenced cluster-scoped catalog, read only
	// when the deployment opted in. It has no watch: a cluster-scoped
	// watch cannot be pinned by name and would need list authority over
	// every catalog in the cluster, so it refreshes on re-seed.
	ClusterCatalog ImageCatalogFacts
	// ClusterCatalogState is the outcome of that optional lookup.
	ClusterCatalogState ClusterCatalogState
}

// ImageCatalogSource produces the namespace's ImageCatalog resources.
type ImageCatalogSource interface {
	// FetchImageCatalogs returns a complete bounded seed.
	FetchImageCatalogs(ctx context.Context) (ImageCatalogsState, error)
	// WatchImageCatalogs streams changes from the given version.
	WatchImageCatalogs(ctx context.Context, fromResourceVersion string) (ImageCatalogWatch, error)
}

// ImageCatalogsSnapshot is the rendered catalog set, immutable and
// carrying its own staleness.
type ImageCatalogsSnapshot struct {
	// Generation increases by one on every publication.
	Generation uint64
	// ObservedAt is the time of the last successful contact.
	ObservedAt time.Time
	// Stale reports lost contact.
	Stale bool
	// Truncated reports that more catalogs existed than the bound.
	Truncated bool
	// Catalogs is sorted by name and bounded by MaxImageCatalogs. These
	// are the namespace's catalogs.
	Catalogs []ImageCatalogFacts
	// ClusterCatalog is the cluster-scoped catalog the Cluster
	// references, read only when the deployment opted in. It is kept
	// apart from Catalogs because a namespaced and a cluster-scoped
	// catalog may share a name and mean different objects.
	ClusterCatalog ImageCatalogFacts
	// ClusterCatalogState is the outcome of that optional lookup.
	ClusterCatalogState ClusterCatalogState
}

// Catalog returns the named catalog and whether it was observed.
func (s ImageCatalogsSnapshot) Catalog(name string) (ImageCatalogFacts, bool) {
	for _, c := range s.Catalogs {
		if c.Name == name {
			return c, true
		}
	}
	return ImageCatalogFacts{}, false
}

// ImageCatalogStore holds the current catalog snapshot for concurrent
// readers.
type ImageCatalogStore struct {
	mu   sync.RWMutex
	snap ImageCatalogsSnapshot
	has  bool
}

// NewImageCatalogStore returns an empty store.
func NewImageCatalogStore() *ImageCatalogStore { return &ImageCatalogStore{} }

// CurrentImageCatalogs returns the snapshot and whether one exists.
func (s *ImageCatalogStore) CurrentImageCatalogs() (ImageCatalogsSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.has
}

// publish replaces the snapshot, advancing the generation and clearing
// staleness.
func (s *ImageCatalogStore) publish(catalogs []ImageCatalogFacts, clusterCatalog ImageCatalogFacts, clusterState ClusterCatalogState, observedAt time.Time, sourceTruncated bool) {
	sorted, cut := bounded(catalogs, lessCatalogName, MaxImageCatalogs)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = ImageCatalogsSnapshot{
		Generation:          s.snap.Generation + 1,
		ObservedAt:          observedAt,
		Truncated:           sourceTruncated || cut,
		Catalogs:            sorted,
		ClusterCatalog:      clusterCatalog,
		ClusterCatalogState: clusterState,
	}
	s.has = true
}

// markStale marks the retained snapshot stale, if one exists.
func (s *ImageCatalogStore) markStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has {
		return
	}
	s.snap.Stale = true
}

// lessCatalogName orders catalogs by name; a catalog is standing
// configuration with no meaningful recency.
func lessCatalogName(a, b ImageCatalogFacts) bool { return a.Name < b.Name }

// catalogRetention identifies retained catalogs. The lexically largest
// name loses, matching lessCatalogName so an evicted entry is one the
// page would have cut anyway.
var catalogRetention = retention[ImageCatalogFacts]{
	Name:      func(c ImageCatalogFacts) string { return c.Name },
	UID:       func(c ImageCatalogFacts) string { return c.UID },
	Limit:     MaxImageCatalogs + 1,
	Evictable: func(a, b ImageCatalogFacts) bool { return a.Name > b.Name },
}

// ImageCatalogCollector maintains the catalog store on the shared loop.
type ImageCatalogCollector struct {
	source         ImageCatalogSource
	store          *ImageCatalogStore
	clock          Clock
	logger         *slog.Logger
	state          keyed[ImageCatalogFacts]
	truncated      bool
	clusterCatalog ImageCatalogFacts
	clusterState   ClusterCatalogState
}

// NewImageCatalogCollector wires a catalog collector onto a store.
func NewImageCatalogCollector(source ImageCatalogSource, store *ImageCatalogStore, clock Clock, logger *slog.Logger) *ImageCatalogCollector {
	return &ImageCatalogCollector{source: source, store: store, clock: clock, logger: logger}
}

// Run blocks until ctx is done, maintaining the store.
func (c *ImageCatalogCollector) Run(ctx context.Context) error {
	return newLoop[string, ImageCatalogChange](c, c.clock, c.logger).Run(ctx)
}

// op names this resource in contact-loss logs.
func (c *ImageCatalogCollector) op() string { return "image catalogs" }

// seed replaces the retained set and returns the resource version the
// watch resumes from.
func (c *ImageCatalogCollector) seed(ctx context.Context) (string, error) {
	state, err := c.source.FetchImageCatalogs(ctx)
	if err != nil {
		return "", err
	}
	c.truncated = state.Truncated
	c.clusterCatalog = state.ClusterCatalog
	c.clusterState = state.ClusterCatalogState
	c.state = make(keyed[ImageCatalogFacts], len(state.Catalogs))
	for _, cat := range state.Catalogs {
		if c.state.put(cat, catalogRetention) {
			c.truncated = true
		}
	}
	return state.ResourceVersion, nil
}

// follow starts the catalog watch from the seed's resource version.
func (c *ImageCatalogCollector) follow(ctx context.Context, from string) (<-chan ImageCatalogChange, func(), error) {
	w, err := c.source.WatchImageCatalogs(ctx, from)
	if err != nil {
		return nil, nil, err
	}
	return w.Changes(), w.Stop, nil
}

// apply folds one change into the retained set. It reports whether the
// change was recognized; a change carrying nothing is not.
func (c *ImageCatalogCollector) apply(change ImageCatalogChange) bool {
	switch {
	case change.Put != nil:
		if c.state.put(*change.Put, catalogRetention) {
			c.truncated = true
		}
	case change.Delete != nil:
		c.state.remove(change.Delete.Name, change.Delete.UID, catalogRetention)
	default:
		return false
	}
	return true
}

// publish snapshots the retained set into the store.
func (c *ImageCatalogCollector) publish(observedAt time.Time) {
	c.store.publish(c.state.list(), c.clusterCatalog, c.clusterState, observedAt, c.truncated)
}

// markStale marks the retained snapshot stale, if one exists.
func (c *ImageCatalogCollector) markStale() { c.store.markStale() }
