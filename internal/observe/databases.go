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

// MaxDatabaseObjects bounds each retained and rendered declarative kind
// separately. One extra per kind is retained internally as a truncation
// sentinel.
const MaxDatabaseObjects = 200

// Declared is the spine every declarative object shares: what the
// operator did with the declaration, not what the database contains.
//
// pgConsole never connects to PostgreSQL. Everything here is the
// operator's report about reconciling a Kubernetes resource, which is
// why a database's size, a role's actual privileges in the cluster, or a
// publication's replicated rows appear nowhere: those are questions for
// pgAdmin, and answering them here would be doing a sibling's job.
type Declared struct {
	// Applied is the operator's reconciliation verdict. Nil means it has
	// not reported one yet, which is distinct from a reported failure.
	Applied *bool
	// Message is the operator's reconciliation output, bounded.
	Message string
	// ObservedGeneration is the spec generation the operator last
	// synchronized.
	ObservedGeneration int64
}

// DatabaseFacts is one declared PostgreSQL database.
type DatabaseFacts struct {
	// Name is the resource name.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// Database is the declared database name inside the cluster.
	Database string
	// Owner is the declared owning role.
	Owner string
	// Encoding is the declared encoding, empty when unset.
	Encoding string
	// Ensure is the declared reconciliation intent, present or absent.
	Ensure string
	// CreatedAt is the resource creation time.
	CreatedAt time.Time
	// Declared is the operator's reconciliation report.
	Declared
}

// DatabaseRoleFacts is one declared PostgreSQL role.
type DatabaseRoleFacts struct {
	// Name is the resource name.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// Role is the declared role name inside the cluster.
	Role string
	// Superuser, CreateDB and CreateRole are the declared privilege
	// attributes. They are the declaration, never a reading of the
	// cluster's catalogs.
	Superuser  bool
	CreateDB   bool
	CreateRole bool
	// ConnectionLimit is the declared limit; negative means unlimited.
	ConnectionLimit int64
	// InRoles are the declared memberships, bounded.
	InRoles []string
	// HasPasswordSecret reports that the declaration references a
	// password Secret. Only the fact of the reference crosses this
	// boundary — never the Secret's name and never its content. The
	// console holds no Secret permission and displays nothing that would
	// need one.
	HasPasswordSecret bool
	// ValidUntil is the declared expiry of the role's password.
	ValidUntil *time.Time
	// CreatedAt is the resource creation time.
	CreatedAt time.Time
	// Declared is the operator's reconciliation report.
	Declared
}

// PublicationFacts is one declared logical-replication publication.
type PublicationFacts struct {
	// Name is the resource name.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// Publication is the declared publication name.
	Publication string
	// Database is the database the publication lives in.
	Database string
	// AllTables reports the declared target as the whole database rather
	// than an enumerated object list. The objects themselves are not
	// rendered: a table list is database content.
	AllTables bool
	// CreatedAt is the resource creation time.
	CreatedAt time.Time
	// Declared is the operator's reconciliation report.
	Declared
}

// SubscriptionFacts is one declared logical-replication subscription.
type SubscriptionFacts struct {
	// Name is the resource name.
	Name string
	// UID distinguishes incarnations sharing a name.
	UID string
	// Subscription is the declared subscription name.
	Subscription string
	// Database is the database the subscription lives in.
	Database string
	// Publication is the publication it subscribes to.
	Publication string
	// ExternalCluster is the declared source cluster name.
	ExternalCluster string
	// CreatedAt is the resource creation time.
	CreatedAt time.Time
	// Declared is the operator's reconciliation report.
	Declared
}

// DatabaseObjectsState is one complete seed of all four kinds and the
// resource versions from which each watch resumes.
type DatabaseObjectsState struct {
	// Databases is the bounded selected Database seed.
	Databases []DatabaseFacts
	// Roles is the bounded selected DatabaseRole seed.
	Roles []DatabaseRoleFacts
	// Publications is the bounded selected Publication seed.
	Publications []PublicationFacts
	// Subscriptions is the bounded selected Subscription seed.
	Subscriptions []SubscriptionFacts
	// Truncated reports a source safety ceiling on any kind.
	Truncated bool
	// The resource versions each kind's watch resumes from.
	DatabaseResourceVersion     string
	RoleResourceVersion         string
	PublicationResourceVersion  string
	SubscriptionResourceVersion string
}

// DatabaseObjectDeletion identifies a removed incarnation of any
// declarative kind.
type DatabaseObjectDeletion struct {
	// Name is the removed resource name.
	Name string
	// UID is the removed incarnation.
	UID string
}

// DatabaseObjectsChange is one change from any of the four merged
// watches. Exactly one field is set.
type DatabaseObjectsChange struct {
	PutDatabase        *DatabaseFacts
	DeleteDatabase     *DatabaseObjectDeletion
	PutRole            *DatabaseRoleFacts
	DeleteRole         *DatabaseObjectDeletion
	PutPublication     *PublicationFacts
	DeletePublication  *DatabaseObjectDeletion
	PutSubscription    *SubscriptionFacts
	DeleteSubscription *DatabaseObjectDeletion
}

// DatabaseObjectsWatch is the merged watch over all four kinds.
type DatabaseObjectsWatch interface {
	// Changes streams changes until any underlying watch ends.
	Changes() <-chan DatabaseObjectsChange
	// Stop releases every underlying watch.
	Stop()
}

// DatabaseObjectsSource produces the target cluster's declarative
// objects. The Kubernetes adapter lists namespace-scoped and selects by
// spec.cluster.name.
type DatabaseObjectsSource interface {
	// FetchDatabaseObjects returns a complete bounded seed.
	FetchDatabaseObjects(ctx context.Context) (DatabaseObjectsState, error)
	// WatchDatabaseObjects follows all four kinds from the seed
	// versions.
	WatchDatabaseObjects(ctx context.Context, state DatabaseObjectsState) (DatabaseObjectsWatch, error)
}

// DatabaseObjectsSnapshot is the rendered declarative set: one section,
// one freshness, four lists.
type DatabaseObjectsSnapshot struct {
	// Generation increases by one on every publication.
	Generation uint64
	// ObservedAt is the time of the last successful contact.
	ObservedAt time.Time
	// Stale reports lost contact.
	Stale bool
	// Truncated reports that any kind was cut at its bound.
	Truncated bool
	// Each list is name-sorted and bounded by MaxDatabaseObjects.
	Databases     []DatabaseFacts
	Roles         []DatabaseRoleFacts
	Publications  []PublicationFacts
	Subscriptions []SubscriptionFacts
}

// DatabaseObjectsStore holds the current declarative snapshot for
// concurrent readers.
type DatabaseObjectsStore struct {
	mu   sync.RWMutex
	snap DatabaseObjectsSnapshot
	has  bool
}

// NewDatabaseObjectsStore returns an empty store.
func NewDatabaseObjectsStore() *DatabaseObjectsStore { return &DatabaseObjectsStore{} }

// CurrentDatabaseObjects returns the snapshot and whether one exists.
func (s *DatabaseObjectsStore) CurrentDatabaseObjects() (DatabaseObjectsSnapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap, s.has
}

// publish replaces the snapshot. Every list is sorted and bounded here,
// so no publication can bypass a bound.
func (s *DatabaseObjectsStore) publish(state DatabaseObjectsSnapshot, observedAt time.Time, sourceTruncated bool) {
	databases, cutDB := bounded(state.Databases, func(a, b DatabaseFacts) bool { return a.Name < b.Name }, MaxDatabaseObjects)
	roles, cutRole := bounded(state.Roles, func(a, b DatabaseRoleFacts) bool { return a.Name < b.Name }, MaxDatabaseObjects)
	publications, cutPub := bounded(state.Publications, func(a, b PublicationFacts) bool { return a.Name < b.Name }, MaxDatabaseObjects)
	subscriptions, cutSub := bounded(state.Subscriptions, func(a, b SubscriptionFacts) bool { return a.Name < b.Name }, MaxDatabaseObjects)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = DatabaseObjectsSnapshot{
		Generation:    s.snap.Generation + 1,
		ObservedAt:    observedAt,
		Truncated:     sourceTruncated || cutDB || cutRole || cutPub || cutSub,
		Databases:     databases,
		Roles:         roles,
		Publications:  publications,
		Subscriptions: subscriptions,
	}
	s.has = true
}

// markStale marks the retained snapshot stale, if one exists.
func (s *DatabaseObjectsStore) markStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has {
		return
	}
	s.snap.Stale = true
}

// Retention policies. Every kind is keyed by resource name and evicts
// the lexically largest, matching the published order so an evicted
// entry is one the page would have cut anyway. A declarative object is
// standing configuration with no meaningful recency to order by.
var (
	databaseRetention = retention[DatabaseFacts]{
		Name: func(d DatabaseFacts) string { return d.Name }, UID: func(d DatabaseFacts) string { return d.UID },
		Limit: MaxDatabaseObjects + 1, Evictable: func(a, b DatabaseFacts) bool { return a.Name > b.Name },
	}
	databaseRoleRetention = retention[DatabaseRoleFacts]{
		Name: func(r DatabaseRoleFacts) string { return r.Name }, UID: func(r DatabaseRoleFacts) string { return r.UID },
		Limit: MaxDatabaseObjects + 1, Evictable: func(a, b DatabaseRoleFacts) bool { return a.Name > b.Name },
	}
	publicationRetention = retention[PublicationFacts]{
		Name: func(p PublicationFacts) string { return p.Name }, UID: func(p PublicationFacts) string { return p.UID },
		Limit: MaxDatabaseObjects + 1, Evictable: func(a, b PublicationFacts) bool { return a.Name > b.Name },
	}
	subscriptionRetention = retention[SubscriptionFacts]{
		Name: func(s SubscriptionFacts) string { return s.Name }, UID: func(s SubscriptionFacts) string { return s.UID },
		Limit: MaxDatabaseObjects + 1, Evictable: func(a, b SubscriptionFacts) bool { return a.Name > b.Name },
	}
)

// DatabaseObjectsCollector maintains the declarative store on the shared
// loop. Four kinds are merged into one change stream, so the section has
// one freshness rather than four that could disagree about how current
// the same screen is.
type DatabaseObjectsCollector struct {
	source        DatabaseObjectsSource
	store         *DatabaseObjectsStore
	clock         Clock
	logger        *slog.Logger
	databases     keyed[DatabaseFacts]
	roles         keyed[DatabaseRoleFacts]
	publications  keyed[PublicationFacts]
	subscriptions keyed[SubscriptionFacts]
	truncated     bool
}

// NewDatabaseObjectsCollector wires a declarative collector onto a
// store.
func NewDatabaseObjectsCollector(source DatabaseObjectsSource, store *DatabaseObjectsStore, clock Clock, logger *slog.Logger) *DatabaseObjectsCollector {
	return &DatabaseObjectsCollector{source: source, store: store, clock: clock, logger: logger}
}

// Run blocks until ctx is done, maintaining the store.
func (c *DatabaseObjectsCollector) Run(ctx context.Context) error {
	return newLoop[DatabaseObjectsState, DatabaseObjectsChange](c, c.clock, c.logger).Run(ctx)
}

// op names this resource in contact-loss logs.
func (c *DatabaseObjectsCollector) op() string { return "database objects" }

// seed replaces all four retained sets. The cursor is the whole seed
// state: four watches resume, and each needs its own version.
func (c *DatabaseObjectsCollector) seed(ctx context.Context) (DatabaseObjectsState, error) {
	state, err := c.source.FetchDatabaseObjects(ctx)
	if err != nil {
		return DatabaseObjectsState{}, err
	}
	c.truncated = state.Truncated
	c.databases = make(keyed[DatabaseFacts], len(state.Databases))
	for _, d := range state.Databases {
		c.truncated = c.databases.put(d, databaseRetention) || c.truncated
	}
	c.roles = make(keyed[DatabaseRoleFacts], len(state.Roles))
	for _, r := range state.Roles {
		c.truncated = c.roles.put(r, databaseRoleRetention) || c.truncated
	}
	c.publications = make(keyed[PublicationFacts], len(state.Publications))
	for _, p := range state.Publications {
		c.truncated = c.publications.put(p, publicationRetention) || c.truncated
	}
	c.subscriptions = make(keyed[SubscriptionFacts], len(state.Subscriptions))
	for _, s := range state.Subscriptions {
		c.truncated = c.subscriptions.put(s, subscriptionRetention) || c.truncated
	}
	return state, nil
}

// follow starts the merged watch from the seed state. Any one stream
// ending ends the merged one, so the collector re-lists every kind
// rather than publishing a generation in which one kind has silently
// stopped arriving.
func (c *DatabaseObjectsCollector) follow(ctx context.Context, from DatabaseObjectsState) (<-chan DatabaseObjectsChange, func(), error) {
	w, err := c.source.WatchDatabaseObjects(ctx, from)
	if err != nil {
		return nil, nil, err
	}
	return w.Changes(), w.Stop, nil
}

// apply folds one change from any of the four watches into its set. It
// reports whether the change was recognized; a change carrying nothing
// is not.
func (c *DatabaseObjectsCollector) apply(change DatabaseObjectsChange) bool {
	switch {
	case change.PutDatabase != nil:
		c.truncated = c.databases.put(*change.PutDatabase, databaseRetention) || c.truncated
	case change.DeleteDatabase != nil:
		c.databases.remove(change.DeleteDatabase.Name, change.DeleteDatabase.UID, databaseRetention)
	case change.PutRole != nil:
		c.truncated = c.roles.put(*change.PutRole, databaseRoleRetention) || c.truncated
	case change.DeleteRole != nil:
		c.roles.remove(change.DeleteRole.Name, change.DeleteRole.UID, databaseRoleRetention)
	case change.PutPublication != nil:
		c.truncated = c.publications.put(*change.PutPublication, publicationRetention) || c.truncated
	case change.DeletePublication != nil:
		c.publications.remove(change.DeletePublication.Name, change.DeletePublication.UID, publicationRetention)
	case change.PutSubscription != nil:
		c.truncated = c.subscriptions.put(*change.PutSubscription, subscriptionRetention) || c.truncated
	case change.DeleteSubscription != nil:
		c.subscriptions.remove(change.DeleteSubscription.Name, change.DeleteSubscription.UID, subscriptionRetention)
	default:
		return false
	}
	return true
}

// publish snapshots all four retained sets into the store.
func (c *DatabaseObjectsCollector) publish(observedAt time.Time) {
	c.store.publish(DatabaseObjectsSnapshot{
		Databases:     c.databases.list(),
		Roles:         c.roles.list(),
		Publications:  c.publications.list(),
		Subscriptions: c.subscriptions.list(),
	}, observedAt, c.truncated)
}

// markStale marks the retained snapshot stale, if one exists.
func (c *DatabaseObjectsCollector) markStale() { c.store.markStale() }
