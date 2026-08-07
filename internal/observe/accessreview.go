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

// Access-review bounds. One extra request is retained internally as a
// truncation sentinel, so neither memory nor the rendered page grows
// with a namespace-wide flood of requests or roles.
const (
	// MaxAccessRequests bounds retained and rendered access requests.
	MaxAccessRequests = 500
	// MaxAccessRoles bounds retained and rendered role names.
	MaxAccessRoles = 200
)

// AccessRequestState is the operator-reported decision state of one
// access request.
type AccessRequestState string

const (
	// AccessRequestPending is an undecided request awaiting review.
	AccessRequestPending AccessRequestState = "pending"
	// AccessRequestApproved is a request a reviewer approved.
	AccessRequestApproved AccessRequestState = "approved"
	// AccessRequestDenied is a request a reviewer denied.
	AccessRequestDenied AccessRequestState = "denied"
	// AccessRequestUnknown is a request whose state the operator has not
	// reported, or reported as a value outside the closed set.
	AccessRequestUnknown AccessRequestState = "unknown"
)

// AccessRequestFacts is the bounded operator-reported state of one
// PgToolBoxAccessRequest. No token, credential, or Secret reference
// crosses the Kubernetes adapter boundary — only the review-relevant
// metadata.
type AccessRequestFacts struct {
	// Name is the resource name.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// Subject is the identity that asked for access.
	Subject string
	// Message is the operator-forwarded request justification.
	Message string
	// State is the decision state, an explicit unknown outside the set.
	State AccessRequestState
	// CreatedAt is the resource creation time.
	CreatedAt time.Time
	// RequestedRole is the role recorded on the decided request
	// (status.requestedRoleRef.name); empty until approved.
	RequestedRole string
	// DecidedBy is the reviewer identity recorded on a decided request.
	DecidedBy string
	// DecidedAt is the decision time recorded on a decided request.
	DecidedAt *time.Time
}

// Pending reports whether the request still awaits a decision, the only
// state that accepts an approve or deny action.
func (f AccessRequestFacts) Pending() bool { return f.State == AccessRequestPending }

// AccessReviewState is one complete seed and the request resource
// version from which the watch resumes.
type AccessReviewState struct {
	// Requests is the bounded selected access-request seed.
	Requests []AccessRequestFacts
	// Roles is the bounded selected role-name seed for the approval
	// picker.
	Roles []string
	// RequestsTruncated reports a source safety ceiling.
	RequestsTruncated bool
	// RequestsResourceVersion starts the request watch.
	RequestsResourceVersion string
}

// AccessRequestDeletion identifies a removed access-request incarnation.
type AccessRequestDeletion struct {
	// Name is the removed resource name.
	Name string
	// UID is the removed incarnation.
	UID string
}

// AccessRequestChange is one change from the request watch. Exactly one
// field is set.
type AccessRequestChange struct {
	// Put upserts one access request.
	Put *AccessRequestFacts
	// Delete removes one access-request incarnation.
	Delete *AccessRequestDeletion
}

// AccessReviewWatch is the access-request watch.
type AccessReviewWatch interface {
	// Changes streams changes until the underlying watch ends.
	Changes() <-chan AccessRequestChange
	// Stop releases the underlying watch.
	Stop()
}

// AccessReviewSource produces the namespace's PgToolBoxAccessRequest
// resources and the PgToolBoxRole names. The Kubernetes adapter performs
// namespace-scoped listing; roles are seeded and refresh on reseed.
type AccessReviewSource interface {
	// FetchAccessReview returns a complete bounded seed.
	FetchAccessReview(ctx context.Context) (AccessReviewState, error)
	// WatchAccessReview follows the requests from the seed version.
	WatchAccessReview(ctx context.Context, state AccessReviewState) (AccessReviewWatch, error)
}

// AccessReviewSnapshot is an immutable, independently stale review view.
type AccessReviewSnapshot struct {
	// Generation increases on every complete publication.
	Generation uint64
	// ObservedAt is the last successful source-contact time.
	ObservedAt time.Time
	// Stale reports lost contact while retaining the last-good view.
	Stale bool
	// RequestsTruncated reports a source or display safety ceiling.
	RequestsTruncated bool
	// Requests are pending-first (oldest first), then decided
	// (newest-first), bounded by MaxAccessRequests.
	Requests []AccessRequestFacts
	// Roles are name-sorted and bounded by MaxAccessRoles.
	Roles []string
}

// AccessReviewStore holds the current review snapshot for concurrent
// readers.
type AccessReviewStore struct {
	mu   sync.RWMutex
	snap AccessReviewSnapshot
	has  bool
}

// NewAccessReviewStore returns an empty store.
func NewAccessReviewStore() *AccessReviewStore { return &AccessReviewStore{} }

// CurrentAccessReview returns the snapshot and whether one exists.
func (s *AccessReviewStore) CurrentAccessReview() (AccessReviewSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.has
}

func (s *AccessReviewStore) publish(requests []AccessRequestFacts, roles []string, observedAt time.Time, sourceTruncated bool) {
	// The cut is decided by the length, never by the flag. Cutting on
	// the flag panicked: sourceTruncated is sticky for the life of a
	// seed, so once eviction set it, deletions bringing the retained set
	// back under the bound left a slice shorter than the cut.
	requestCopy, cut := bounded(requests, lessAccessRequest, MaxAccessRequests)
	truncated := sourceTruncated || cut

	roleCopy, _ := bounded(roles, func(a, b string) bool { return a < b }, MaxAccessRoles)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = AccessReviewSnapshot{
		Generation:        s.snap.Generation + 1,
		ObservedAt:        observedAt,
		RequestsTruncated: truncated,
		Requests:          requestCopy,
		Roles:             roleCopy,
	}
	s.has = true
}

// lessAccessRequest orders pending requests first (oldest first, so the
// longest-waiting request surfaces for action), then decided requests
// newest-first. Name breaks every tie for determinism.
func lessAccessRequest(a, b AccessRequestFacts) bool {
	if a.Pending() != b.Pending() {
		return a.Pending()
	}
	if a.Pending() {
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.Name < b.Name
	}
	at, bt := decidedOrder(a), decidedOrder(b)
	if !at.Equal(bt) {
		return at.After(bt)
	}
	return a.Name < b.Name
}

// decidedOrder is the time a decided request sorts by: its decision time
// when reported, else its creation time.
func decidedOrder(f AccessRequestFacts) time.Time {
	if f.DecidedAt != nil {
		return *f.DecidedAt
	}
	return f.CreatedAt
}

func (s *AccessReviewStore) markStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.has {
		s.snap.Stale = true
	}
}

// accessRequestRetention identifies retained access requests and bounds
// them in memory at one above the rendered bound, so the extra entry is
// the sentinel that lets the page say more exist without holding them.
// The oldest request loses: the queue is read newest-first, so under a
// flood the entries an approver would actually reach are the recent
// ones.
var accessRequestRetention = retention[AccessRequestFacts]{
	Name:      func(r AccessRequestFacts) string { return r.Name },
	UID:       func(r AccessRequestFacts) string { return r.UID },
	Limit:     MaxAccessRequests + 1,
	Evictable: func(a, b AccessRequestFacts) bool { return a.CreatedAt.Before(b.CreatedAt) },
}

// AccessReviewCollector maintains a bounded review view using seed,
// watch, immutable publication, stale retention, and bounded retry
// backoff. Roles are carried on the seed and refresh on each reseed.
type AccessReviewCollector struct {
	source    AccessReviewSource
	store     *AccessReviewStore
	clock     Clock
	logger    *slog.Logger
	requests  keyed[AccessRequestFacts]
	roles     []string
	truncated bool
}

// NewAccessReviewCollector wires a review collector onto a store.
func NewAccessReviewCollector(source AccessReviewSource, store *AccessReviewStore, clock Clock, logger *slog.Logger) *AccessReviewCollector {
	return &AccessReviewCollector{source: source, store: store, clock: clock, logger: logger}
}

// Run blocks until ctx is canceled, maintaining the store.
func (c *AccessReviewCollector) Run(ctx context.Context) error {
	return newLoop[AccessReviewState, AccessRequestChange](c, c.clock, c.logger).Run(ctx)
}

// op names this resource in contact-loss logs.
func (c *AccessReviewCollector) op() string { return "access review" }

// seed replaces the retained requests and the role list. The cursor is
// the whole seed state rather than a resource version: the review watch
// resumes two kinds and needs both.
//
// Roles are carried on the seed and refreshed on each reseed, not
// watched: the approval picker needs the names that exist now, and a
// stale name there would offer a role that cannot be granted.
func (c *AccessReviewCollector) seed(ctx context.Context) (AccessReviewState, error) {
	state, err := c.source.FetchAccessReview(ctx)
	if err != nil {
		return AccessReviewState{}, err
	}
	c.truncated = state.RequestsTruncated
	c.requests = make(keyed[AccessRequestFacts], len(state.Requests))
	for _, request := range state.Requests {
		if c.requests.put(request, accessRequestRetention) {
			c.truncated = true
		}
	}
	c.roles = append([]string(nil), state.Roles...)
	return state, nil
}

// follow starts the review watch from the seed state.
func (c *AccessReviewCollector) follow(ctx context.Context, from AccessReviewState) (<-chan AccessRequestChange, func(), error) {
	w, err := c.source.WatchAccessReview(ctx, from)
	if err != nil {
		return nil, nil, err
	}
	return w.Changes(), w.Stop, nil
}

// apply folds one change into the retained requests. It reports whether
// the change was recognized; a change carrying nothing is not.
func (c *AccessReviewCollector) apply(change AccessRequestChange) bool {
	switch {
	case change.Put != nil:
		if c.requests.put(*change.Put, accessRequestRetention) {
			c.truncated = true
		}
	case change.Delete != nil:
		c.requests.remove(change.Delete.Name, change.Delete.UID, accessRequestRetention)
	default:
		return false
	}
	return true
}

// publish snapshots the retained requests and roles into the store.
func (c *AccessReviewCollector) publish(observedAt time.Time) {
	c.store.publish(c.requests.list(), c.roles, observedAt, c.truncated)
}

// markStale marks the retained snapshot stale, if one exists.
func (c *AccessReviewCollector) markStale() { c.store.markStale() }
