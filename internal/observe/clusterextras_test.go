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
	"testing"
	"time"
)

// TestFailoverQuorumStoreRetainsAbsenceAndMarksStale proves the two
// states a reader must never confuse: "the operator reports no quorum"
// is a published observation with Present false, while a stale one says
// the console has lost contact and is showing what it last saw.
func TestFailoverQuorumStoreRetainsAbsenceAndMarksStale(t *testing.T) {
	t.Parallel()
	store := NewFailoverQuorumStore()

	// Nothing published yet: markStale must not invent a snapshot.
	store.markStale()
	if _, ok := store.CurrentFailoverQuorum(); ok {
		t.Fatal("markStale published a snapshot that never existed")
	}

	store.publish(FailoverQuorumFacts{Present: false}, time.Unix(1000, 0))
	snap, ok := store.CurrentFailoverQuorum()
	if !ok || snap.Quorum.Present || snap.Stale {
		t.Fatalf("snapshot = %+v ok=%v, want a fresh observation of absence", snap, ok)
	}

	store.markStale()
	if snap, _ := store.CurrentFailoverQuorum(); !snap.Stale {
		t.Error("contact loss did not mark the retained observation stale")
	}
}

// TestImageCatalogSnapshotResolvesByName proves the lookup the rendering
// layer uses to pair the cluster's reference with an observed catalog.
func TestImageCatalogSnapshotResolvesByName(t *testing.T) {
	t.Parallel()
	store := NewImageCatalogStore()
	store.publish([]ImageCatalogFacts{
		{Name: "postgres", UID: "u1"},
		{Name: "postgis", UID: "u2"},
	}, ImageCatalogFacts{}, ClusterCatalogNotReferenced, time.Unix(1000, 0), false)

	snap, _ := store.CurrentImageCatalogs()
	if snap.Catalogs[0].Name != "postgis" {
		t.Errorf("catalogs = %+v, want name-sorted", snap.Catalogs)
	}
	if got, ok := snap.Catalog("postgres"); !ok || got.UID != "u1" {
		t.Errorf("Catalog(postgres) = %+v ok=%v, want the named catalog", got, ok)
	}
	if _, ok := snap.Catalog("absent"); ok {
		t.Error("an unobserved catalog resolved")
	}
}

// TestImageCatalogStoreBoundsAndFlagsTruncation proves a namespace with
// more catalogs than the bound cuts visibly.
func TestImageCatalogStoreBoundsAndFlagsTruncation(t *testing.T) {
	t.Parallel()
	catalogs := make([]ImageCatalogFacts, MaxImageCatalogs+5)
	for i := range catalogs {
		catalogs[i] = ImageCatalogFacts{Name: string(rune('a'+i%26)) + string(rune('a'+i/26)), UID: "u"}
	}
	store := NewImageCatalogStore()
	store.publish(catalogs, ImageCatalogFacts{}, ClusterCatalogNotReferenced, time.Unix(1000, 0), false)

	snap, _ := store.CurrentImageCatalogs()
	if len(snap.Catalogs) != MaxImageCatalogs || !snap.Truncated {
		t.Errorf("catalogs len=%d truncated=%v, want the bound applied and visible", len(snap.Catalogs), snap.Truncated)
	}
}
