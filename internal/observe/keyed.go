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

import "sort"

// keyed is the retained item set of a collection collector: one item per
// resource name.
//
// A defined map type rather than a struct, so a collector holding one
// can still be assembled from a plain map literal — which the tests do —
// and so a collector holding two independent sets keeps one truncation
// flag per set instead of one shared flag meaning two different things.
type keyed[T any] map[string]T

// retention identifies the items of one keyed set. It is a value rather
// than a set of hooks on the shared loop, so a resource's decisions stay
// in that resource's file next to the comment justifying them.
type retention[T any] struct {
	// Name is the retention key, the resource name.
	Name func(T) string
	// UID is the incarnation a deletion must match before it removes an
	// entry.
	UID func(T) string
	// Limit bounds the retained set. Zero is unbounded retention, for a
	// resource that bounds only at publication so that which items
	// survive a flood is decided by one sort and never by arrival order.
	Limit int
	// Evictable reports that a is dropped before b when a new name
	// arrives at Limit. Consulted only at the bound, and only when
	// Limit is non-zero.
	Evictable func(a, b T) bool
}

// put upserts one item, evicting the entry Evictable names first when a
// new name arrives at Limit. It reports whether it evicted, which a
// caller records as its own truncation — the event collector
// deliberately does not, because the bound an operator is shown is the
// rendered one and not this one.
func (k keyed[T]) put(item T, p retention[T]) bool {
	name := p.Name(item)
	evicted := false
	if _, exists := k[name]; !exists && p.Limit > 0 && len(k) >= p.Limit {
		delete(k, k.evictee(p))
		evicted = true
	}
	k[name] = item
	return evicted
}

// evictee is the name the policy drops first. Ties fall to map
// iteration order, exactly as the hand-written retainers did.
func (k keyed[T]) evictee(p retention[T]) string {
	loser, chosen := "", false
	for name, item := range k {
		if !chosen || p.Evictable(item, k[loser]) {
			loser, chosen = name, true
		}
	}
	return loser
}

// remove drops name only when uid still matches the retained
// incarnation. A deletion whose UID no longer matches is discarded: the
// name was reused by a newer incarnation that must not be removed.
func (k keyed[T]) remove(name, uid string, p retention[T]) {
	if current, ok := k[name]; ok && p.UID(current) == uid {
		delete(k, name)
	}
}

// list copies the retained items for publication.
func (k keyed[T]) list() []T {
	out := make([]T, 0, len(k))
	for _, item := range k {
		out = append(out, item)
	}
	return out
}

// bounded copies items, orders them with less, cuts at limit, and
// reports whether the cut removed anything. Every collection store
// publishes through it, so no snapshot can carry an unordered or
// unbounded list.
//
// sort.Slice, not sort.SliceStable: every comparator passed here breaks
// ties by name, so the order is total and the unstable sort is
// equivalent — and keeping the unstable call keeps published order
// byte-for-byte what it was before this helper existed.
func bounded[T any](items []T, less func(a, b T) bool, limit int) ([]T, bool) {
	kept := append([]T(nil), items...)
	sort.Slice(kept, func(i, j int) bool { return less(kept[i], kept[j]) })
	if len(kept) > limit {
		return kept[:limit], true
	}
	return kept, false
}
