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

	apiv1 "github.com/cloudnative-pg/api/pkg/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
)

// The four declarative CRDs. Each declares one PostgreSQL object and
// carries the operator's report of reconciling it; none of them is read
// for database content.
var (
	databaseGVR     = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "databases"}
	databaseRoleGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "databaseroles"}
	publicationGVR  = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "publications"}
	subscriptionGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "subscriptions"}
)

const (
	databaseObjectPageSize     = 200
	maxDatabaseObjectsScan     = 2000
	maxDeclaredRoleMemberships = 32
)

// FetchDatabaseObjects lists all four declarative kinds, selecting the
// configured Cluster by spec.cluster.name on each.
func (c *Client) FetchDatabaseObjects(ctx context.Context) (observe.DatabaseObjectsState, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opts.RequestTimeout)
	defer cancel()

	var state observe.DatabaseObjectsState
	var truncated bool
	var err error

	if state.Databases, state.DatabaseResourceVersion, truncated, err = listDeclared(ctx, c, databaseGVR, "databases", c.convertDatabase); err != nil {
		return observe.DatabaseObjectsState{}, err
	}
	state.Truncated = state.Truncated || truncated
	if state.Roles, state.RoleResourceVersion, truncated, err = listDeclared(ctx, c, databaseRoleGVR, "database roles", c.convertDatabaseRole); err != nil {
		return observe.DatabaseObjectsState{}, err
	}
	state.Truncated = state.Truncated || truncated
	if state.Publications, state.PublicationResourceVersion, truncated, err = listDeclared(ctx, c, publicationGVR, "publications", c.convertPublication); err != nil {
		return observe.DatabaseObjectsState{}, err
	}
	state.Truncated = state.Truncated || truncated
	if state.Subscriptions, state.SubscriptionResourceVersion, truncated, err = listDeclared(ctx, c, subscriptionGVR, "subscriptions", c.convertSubscription); err != nil {
		return observe.DatabaseObjectsState{}, err
	}
	state.Truncated = state.Truncated || truncated
	return state, nil
}

// listDeclared pages one declarative kind, keeping what convert selects.
// The two ceilings are independent: the kept set, and the items scanned,
// which is what protects against a namespace holding thousands of other
// clusters' declarations.
func listDeclared[T any](
	ctx context.Context,
	c *Client,
	gvr schema.GroupVersionResource,
	op string,
	convert func(map[string]any) (T, bool, error),
) ([]T, string, bool, error) {
	var kept []T
	examined := 0
	opts := metav1.ListOptions{Limit: databaseObjectPageSize}
	rv := ""
	for {
		list, err := c.dyn.Resource(gvr).Namespace(c.opts.Namespace).List(ctx, opts)
		if err != nil {
			return nil, "", false, categorize(op+" list", err)
		}
		rv = list.GetResourceVersion()
		for i := range list.Items {
			facts, member, err := convert(list.Items[i].Object)
			if err != nil {
				return nil, "", false, err
			}
			if member {
				kept = append(kept, facts)
			}
		}
		examined += len(list.Items)
		if list.GetContinue() == "" {
			break
		}
		if len(kept) > observe.MaxDatabaseObjects || examined >= maxDatabaseObjectsScan {
			return kept, rv, true, nil
		}
		opts.Continue = list.GetContinue()
	}
	return kept, rv, len(kept) > observe.MaxDatabaseObjects, nil
}

// WatchDatabaseObjects merges one watch per declarative kind. Any stream
// ending terminates the merged watch so the collector re-lists all four
// and republishes only a complete generation.
func (c *Client) WatchDatabaseObjects(ctx context.Context, state observe.DatabaseObjectsState) (observe.DatabaseObjectsWatch, error) {
	type opened struct {
		w  watch.Interface
		op string
	}
	var started []opened
	stopStarted := func() {
		for _, o := range started {
			o.w.Stop()
		}
	}

	for _, spec := range []struct {
		gvr schema.GroupVersionResource
		rv  string
		op  string
	}{
		{databaseGVR, state.DatabaseResourceVersion, "databases watch"},
		{databaseRoleGVR, state.RoleResourceVersion, "database roles watch"},
		{publicationGVR, state.PublicationResourceVersion, "publications watch"},
		{subscriptionGVR, state.SubscriptionResourceVersion, "subscriptions watch"},
	} {
		w, err := c.dyn.Resource(spec.gvr).Namespace(c.opts.Namespace).Watch(ctx, metav1.ListOptions{
			ResourceVersion: spec.rv,
		})
		if err != nil {
			stopStarted()
			return nil, categorize(spec.op, err)
		}
		started = append(started, opened{w: w, op: spec.op})
	}

	inner := make([]watch.Interface, 0, len(started))
	for _, o := range started {
		inner = append(inner, o.w)
	}
	items, stop := fanIn(ctx, inner, []pump[observe.DatabaseObjectsChange]{
		c.pumpDatabase, c.pumpDatabaseRole, c.pumpPublication, c.pumpSubscription,
	})
	return changeStream[observe.DatabaseObjectsChange]{stream[observe.DatabaseObjectsChange]{items: items, stop: stop}}, nil
}

// declaredPump builds one kind's pump from its converter and the two
// constructors that place the result in the shared change union. A
// declaration belonging to another cluster is skipped, not fatal: these
// watches are namespace-scoped because RBAC cannot pin a watch by name.
func declaredPump[T any](
	convert func(map[string]any) (T, bool, error),
	put func(T) observe.DatabaseObjectsChange,
	del func(name, uid string) observe.DatabaseObjectsChange,
	identity func(T) (name, uid string),
) pump[observe.DatabaseObjectsChange] {
	return func(event watch.Event) (observe.DatabaseObjectsChange, bool, bool) {
		var none observe.DatabaseObjectsChange
		obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
		if !ok {
			return none, false, true
		}
		facts, member, err := convert(obj.UnstructuredContent())
		if err != nil {
			return none, false, true
		}
		if !member {
			return none, false, false
		}
		switch event.Type {
		case watch.Added, watch.Modified:
			return put(facts), true, false
		case watch.Deleted:
			name, uid := identity(facts)
			return del(name, uid), true, false
		case watch.Bookmark:
			return none, false, false
		default:
			return none, false, true
		}
	}
}

func (c *Client) pumpDatabase(event watch.Event) (observe.DatabaseObjectsChange, bool, bool) {
	return declaredPump(c.convertDatabase,
		func(f observe.DatabaseFacts) observe.DatabaseObjectsChange {
			return observe.DatabaseObjectsChange{PutDatabase: &f}
		},
		func(name, uid string) observe.DatabaseObjectsChange {
			return observe.DatabaseObjectsChange{DeleteDatabase: &observe.DatabaseObjectDeletion{Name: name, UID: uid}}
		},
		func(f observe.DatabaseFacts) (string, string) { return f.Name, f.UID },
	)(event)
}

func (c *Client) pumpDatabaseRole(event watch.Event) (observe.DatabaseObjectsChange, bool, bool) {
	return declaredPump(c.convertDatabaseRole,
		func(f observe.DatabaseRoleFacts) observe.DatabaseObjectsChange {
			return observe.DatabaseObjectsChange{PutRole: &f}
		},
		func(name, uid string) observe.DatabaseObjectsChange {
			return observe.DatabaseObjectsChange{DeleteRole: &observe.DatabaseObjectDeletion{Name: name, UID: uid}}
		},
		func(f observe.DatabaseRoleFacts) (string, string) { return f.Name, f.UID },
	)(event)
}

func (c *Client) pumpPublication(event watch.Event) (observe.DatabaseObjectsChange, bool, bool) {
	return declaredPump(c.convertPublication,
		func(f observe.PublicationFacts) observe.DatabaseObjectsChange {
			return observe.DatabaseObjectsChange{PutPublication: &f}
		},
		func(name, uid string) observe.DatabaseObjectsChange {
			return observe.DatabaseObjectsChange{DeletePublication: &observe.DatabaseObjectDeletion{Name: name, UID: uid}}
		},
		func(f observe.PublicationFacts) (string, string) { return f.Name, f.UID },
	)(event)
}

func (c *Client) pumpSubscription(event watch.Event) (observe.DatabaseObjectsChange, bool, bool) {
	return declaredPump(c.convertSubscription,
		func(f observe.SubscriptionFacts) observe.DatabaseObjectsChange {
			return observe.DatabaseObjectsChange{PutSubscription: &f}
		},
		func(name, uid string) observe.DatabaseObjectsChange {
			return observe.DatabaseObjectsChange{DeleteSubscription: &observe.DatabaseObjectDeletion{Name: name, UID: uid}}
		},
		func(f observe.SubscriptionFacts) (string, string) { return f.Name, f.UID },
	)(event)
}

// declaredStatus lifts the reconciliation spine every declarative kind
// reports. The message is bounded here, at the boundary.
func declaredStatus(observedGeneration int64, applied *bool, message string) observe.Declared {
	d := observe.Declared{ObservedGeneration: observedGeneration, Message: boundOperatorMessage(message)}
	if applied != nil {
		v := *applied
		d.Applied = &v
	}
	return d
}

func (c *Client) convertDatabase(content map[string]any) (observe.DatabaseFacts, bool, error) {
	var db apiv1.Database
	if err := fromUnstructured(content, &db, "database convert"); err != nil {
		return observe.DatabaseFacts{}, false, err
	}
	return observe.DatabaseFacts{
		Name: db.Name, UID: string(db.UID), Database: db.Spec.Name,
		Owner: db.Spec.Owner, Encoding: db.Spec.Encoding, Ensure: string(db.Spec.Ensure),
		CreatedAt: db.CreationTimestamp.Time.UTC(),
		Declared:  declaredStatus(db.Status.ObservedGeneration, db.Status.Applied, db.Status.Message),
	}, c.declaredForTarget(db.Namespace, db.Spec.ClusterRef.Name), nil
}

func (c *Client) convertDatabaseRole(content map[string]any) (observe.DatabaseRoleFacts, bool, error) {
	var role apiv1.DatabaseRole
	if err := fromUnstructured(content, &role, "database role convert"); err != nil {
		return observe.DatabaseRoleFacts{}, false, err
	}
	facts := observe.DatabaseRoleFacts{
		Name: role.Name, UID: string(role.UID), Role: role.Spec.Name,
		Superuser: role.Spec.Superuser, CreateDB: role.Spec.CreateDB, CreateRole: role.Spec.CreateRole,
		ConnectionLimit: role.Spec.ConnectionLimit,
		// The reference, never the Secret. The console holds no Secret
		// permission and nothing it displays may need one.
		HasPasswordSecret: role.Spec.PasswordSecret != nil,
		CreatedAt:         role.CreationTimestamp.Time.UTC(),
		Declared:          declaredStatus(role.Status.ObservedGeneration, role.Status.Applied, role.Status.Message),
	}
	if role.Spec.ValidUntil != nil {
		t := role.Spec.ValidUntil.Time.UTC()
		facts.ValidUntil = &t
	}
	memberships := role.Spec.InRoles
	if len(memberships) > maxDeclaredRoleMemberships {
		memberships = memberships[:maxDeclaredRoleMemberships]
	}
	facts.InRoles = append([]string(nil), memberships...)
	return facts, c.declaredForTarget(role.Namespace, role.Spec.ClusterRef.Name), nil
}

func (c *Client) convertPublication(content map[string]any) (observe.PublicationFacts, bool, error) {
	var pub apiv1.Publication
	if err := fromUnstructured(content, &pub, "publication convert"); err != nil {
		return observe.PublicationFacts{}, false, err
	}
	return observe.PublicationFacts{
		Name: pub.Name, UID: string(pub.UID), Publication: pub.Spec.Name, Database: pub.Spec.DBName,
		// Only whether the target is the whole database. The enumerated
		// object list is a table list, which is database content and not
		// this console's to render.
		AllTables: pub.Spec.Target.AllTables,
		CreatedAt: pub.CreationTimestamp.Time.UTC(),
		Declared:  declaredStatus(pub.Status.ObservedGeneration, pub.Status.Applied, pub.Status.Message),
	}, c.declaredForTarget(pub.Namespace, pub.Spec.ClusterRef.Name), nil
}

func (c *Client) convertSubscription(content map[string]any) (observe.SubscriptionFacts, bool, error) {
	var sub apiv1.Subscription
	if err := fromUnstructured(content, &sub, "subscription convert"); err != nil {
		return observe.SubscriptionFacts{}, false, err
	}
	return observe.SubscriptionFacts{
		Name: sub.Name, UID: string(sub.UID), Subscription: sub.Spec.Name, Database: sub.Spec.DBName,
		Publication: sub.Spec.PublicationName, ExternalCluster: sub.Spec.ExternalClusterName,
		CreatedAt: sub.CreationTimestamp.Time.UTC(),
		Declared:  declaredStatus(sub.Status.ObservedGeneration, sub.Status.Applied, sub.Status.Message),
	}, c.declaredForTarget(sub.Namespace, sub.Spec.ClusterRef.Name), nil
}

// declaredForTarget is the membership rule every declarative kind
// shares: the exact spec.cluster.name reference, in the configured
// namespace.
func (c *Client) declaredForTarget(namespace, clusterRef string) bool {
	return namespace == c.opts.Namespace && clusterRef == c.opts.ClusterName
}

// fromUnstructured decodes into a typed CRD, categorizing the failure.
func fromUnstructured(content map[string]any, into any, op string) error {
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(content, into); err != nil {
		return redact.NewError(op, redact.CategoryInternal, err)
	}
	return nil
}
