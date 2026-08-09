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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/fyannk/pgConsole/internal/history"
	"github.com/fyannk/pgConsole/internal/redact"
)

// History capture rides the watches and lists this client already runs:
// a tapped pump records the raw event only after the pump accepted it,
// so membership filtering stays single-sourced in the pumps, and a seed
// recorder collects the raw members of one complete listing. Nothing
// here opens a connection of its own.

// The capture scopes, one per seed-and-watch unit. Instance pods and
// pooler pods are the same kind through two different listings, so the
// scope — not the kind — is what a complete seed accounts for.
const (
	scopeCluster          = "cluster"
	scopePods             = "pods"
	scopePoolerPods       = "pooler pods"
	scopeBackups          = "backups"
	scopeScheduledBackups = "scheduled backups"
	scopeObjectStore      = "object store"
	scopePoolers          = "poolers"
	scopeFailoverQuorum   = "failover quorum"
	scopeImageCatalogs    = "image catalogs"
	scopeDatabases        = "databases"
	scopeDatabaseRoles    = "database roles"
	scopePublications     = "publications"
	scopeSubscriptions    = "subscriptions"
	scopeAccessRequests   = "access requests"
	scopeServices         = "services"
	scopeVolumes          = "volumes"
	scopeSnapshots        = "snapshots"
)

// lastAppliedAnnotation embeds a full second copy of the object as
// applied, including anything the apply carried; it never survives into
// a stored manifest.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// redactedEnvValue replaces every inline container environment value.
// Inline env is the classic vector for secret material in a pod spec;
// references (valueFrom, envFrom) are names and survive.
const redactedEnvValue = "[redacted]"

// tap wraps a pump so every accepted event is also recorded. A rejected
// event — a bookmark, a non-member — is never captured: acceptance is
// the pump's decision and this wrapper adds no second opinion. With no
// recorder wired the pump is returned untouched, so a disabled history
// adds no code to the watch path at all.
func tap[E any](c *Client, scope string, p pump[E]) pump[E] {
	if c.opts.Recorder == nil {
		return p
	}
	return func(event watch.Event) (E, bool, bool) {
		item, ok, fatal := p(event)
		if ok {
			c.recordEvent(scope, event)
		}
		return item, ok, fatal
	}
}

// recordEvent captures one accepted watch event.
func (c *Client) recordEvent(scope string, event watch.Event) {
	obj, ok := event.Object.(interface{ UnstructuredContent() map[string]any })
	if !ok {
		return
	}
	obs, err := observationFrom(scope, obj.UnstructuredContent(), event.Type == watch.Deleted)
	if err != nil {
		c.logCaptureFailed(scope, err)
		return
	}
	c.opts.Recorder.Observe(obs)
}

// recordObject captures one object read outside a seed, such as the
// referenced ObjectStore get.
func (c *Client) recordObject(scope string, content map[string]any) {
	if c.opts.Recorder == nil {
		return
	}
	obs, err := observationFrom(scope, content, false)
	if err != nil {
		c.logCaptureFailed(scope, err)
		return
	}
	c.opts.Recorder.Observe(obs)
}

// seedRecorder collects the raw members of one listing and hands them
// over as a complete seed. A nil receiver is the disabled recorder, so
// call sites stay unconditional.
type seedRecorder struct {
	client *Client
	scope  string
	obs    []history.Observation
	// degraded records that one item failed to convert: the set is no
	// longer complete, so it must not imply deletions.
	degraded bool
}

// seedRecord starts collecting one listing's members. Nil when history
// is disabled.
func (c *Client) seedRecord(scope string) *seedRecorder {
	if c.opts.Recorder == nil {
		return nil
	}
	return &seedRecorder{client: c, scope: scope}
}

// add collects one member's raw content.
func (r *seedRecorder) add(content map[string]any) {
	if r == nil {
		return
	}
	obs, err := observationFrom(r.scope, content, false)
	if err != nil {
		r.degraded = true
		r.client.logCaptureFailed(r.scope, err)
		return
	}
	r.obs = append(r.obs, obs)
}

// commit hands the collected set over. Only a listing that is complete —
// fully paged and fully converted — may claim seed semantics, because a
// seed's absence implies deletion; anything less degrades to per-item
// observations, which imply nothing about what was not seen.
func (r *seedRecorder) commit(complete bool) {
	if r == nil {
		return
	}
	if complete && !r.degraded {
		r.client.opts.Recorder.Seed(r.scope, r.obs)
		return
	}
	for _, obs := range r.obs {
		r.client.opts.Recorder.Observe(obs)
	}
}

// logCaptureFailed records the category of a failed capture, never its
// text. Capture failure is never allowed to become collection failure.
func (c *Client) logCaptureFailed(scope string, err error) {
	c.logger.Info("history capture failed",
		slog.String("scope", scope),
		slog.String("category", redact.Safe(err)))
}

// observationFrom converts one raw object into a source-neutral history
// observation: identity and actor extracted, manifest normalized and
// scrubbed, spec and status digested separately.
func observationFrom(scope string, content map[string]any, deleted bool) (history.Observation, error) {
	obs := history.Observation{Scope: scope, Deleted: deleted}
	obs.Kind, _ = content["kind"].(string)
	if apiVersion, ok := content["apiVersion"].(string); ok {
		if group, version, ok := splitAPIVersion(apiVersion); ok {
			obs.Group, obs.Version = group, version
		}
	}
	meta, ok := content["metadata"].(map[string]any)
	if !ok {
		return history.Observation{}, redact.NewError("history capture", redact.CategoryInternal, errors.New("object carries no metadata"))
	}
	obs.Name, _ = meta["name"].(string)
	obs.Namespace, _ = meta["namespace"].(string)
	obs.UID, _ = meta["uid"].(string)
	obs.Generation, _ = meta["generation"].(int64)
	obs.Actor = actorOf(meta)
	if obs.UID == "" {
		return history.Observation{}, redact.NewError("history capture", redact.CategoryInternal, errors.New("object carries no uid"))
	}
	if deleted {
		// The last definition is the previous revision's word; a
		// deletion contributes identity and nothing else — not even
		// ownership, because there is no diff to attribute.
		return obs, nil
	}
	obs.Owners = ownersOf(meta)

	manifest := normalize(content)
	specHash, statusHash, encoded, err := digest(manifest)
	if err != nil {
		return history.Observation{}, redact.NewError("history capture", redact.CategoryInternal, err)
	}
	obs.Manifest = encoded
	obs.SpecHash = specHash
	obs.StatusHash = statusHash
	return obs, nil
}

// normalize deep-copies the object and removes what must never be
// stored: managed fields and the resource version because they churn
// without the definition changing, the last-applied annotation because
// it is a full second copy of the object, and inline container
// environment values because inline env is where secret material leaks
// into specs. The original is never touched — it still belongs to the
// pump that accepted it.
func normalize(content map[string]any) map[string]any {
	copied := runtime.DeepCopyJSON(content)
	if meta, ok := copied["metadata"].(map[string]any); ok {
		delete(meta, "managedFields")
		delete(meta, "resourceVersion")
		if annotations, ok := meta["annotations"].(map[string]any); ok {
			delete(annotations, lastAppliedAnnotation)
			if len(annotations) == 0 {
				delete(meta, "annotations")
			}
		}
	}
	scrubEnvValues(copied)
	return copied
}

// scrubEnvValues walks the whole tree and blanks the inline value of
// every env entry, wherever the kind nests its containers. Structural,
// not path-based: a pod spec, a pooler template and a Cluster's own env
// all match without this function knowing any of them.
func scrubEnvValues(node any) {
	switch typed := node.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "env" {
				if items, ok := child.([]any); ok {
					for _, item := range items {
						entry, ok := item.(map[string]any)
						if !ok {
							continue
						}
						if value, ok := entry["value"].(string); ok && value != "" {
							entry["value"] = redactedEnvValue
						}
					}
					continue
				}
			}
			scrubEnvValues(child)
		}
	case []any:
		for _, item := range typed {
			scrubEnvValues(item)
		}
	}
}

// digest encodes the normalized manifest and hashes its spec and status
// halves separately, so the domain can tell a definition change from a
// status transition without parsing manifests. encoding/json writes map
// keys in sorted order, which is all the canonical form this needs.
func digest(manifest map[string]any) (specHash, statusHash string, encoded []byte, err error) {
	encoded, err = json.Marshal(manifest)
	if err != nil {
		return "", "", nil, err
	}
	if status, ok := manifest["status"]; ok {
		statusBytes, err := json.Marshal(status)
		if err != nil {
			return "", "", nil, err
		}
		statusHash = hashOf(statusBytes)
	}
	withoutStatus := make(map[string]any, len(manifest))
	for key, value := range manifest {
		if key != "status" {
			withoutStatus[key] = value
		}
	}
	specBytes, err := json.Marshal(withoutStatus)
	if err != nil {
		return "", "", nil, err
	}
	return hashOf(specBytes), statusHash, encoded, nil
}

// hashOf is the revision digest, sha256 in hex.
func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// maxOwnedPaths bounds the owned field paths captured per observation,
// across all managers. The bound lives here, at the boundary, like
// every other bound: a pod's kubelet entry can own hundreds of status
// leafs, and attribution beyond the budget degrades to "unattributed"
// rather than to unbounded retention.
const maxOwnedPaths = 128

// ownersOf reads each field manager's owned paths from
// metadata.managedFields, in the shared field-path encoding, before
// normalize strips the managed fields away. Entries of one manager —
// an Apply and an Update entry can coexist — are merged.
func ownersOf(meta map[string]any) []history.FieldOwner {
	fields, ok := meta["managedFields"].([]any)
	if !ok {
		return nil
	}
	budget := maxOwnedPaths
	byManager := map[string][]string{}
	var managers []string
	for _, field := range fields {
		entry, ok := field.(map[string]any)
		if !ok {
			continue
		}
		manager, _ := entry["manager"].(string)
		fieldsV1, ok := entry["fieldsV1"].(map[string]any)
		if manager == "" || !ok {
			continue
		}
		var paths []string
		collectOwned("", fieldsV1, &paths, &budget)
		if len(paths) == 0 {
			continue
		}
		if _, seen := byManager[manager]; !seen {
			managers = append(managers, manager)
		}
		byManager[manager] = append(byManager[manager], paths...)
	}
	if len(managers) == 0 {
		return nil
	}
	sort.Strings(managers)
	owners := make([]history.FieldOwner, 0, len(managers))
	for _, manager := range managers {
		paths := byManager[manager]
		sort.Strings(paths)
		deduped := paths[:0]
		for i, path := range paths {
			if i == 0 || path != paths[i-1] {
				deduped = append(deduped, path)
			}
		}
		owners = append(owners, history.FieldOwner{Manager: manager, Paths: deduped})
	}
	return owners
}

// collectOwned walks one fieldsV1 tree. The format's markers: "f:NAME"
// descends into a field, "k:{json}" into an associative-list element,
// "v:..." claims a set member, and "." claims the node itself. An
// associative key that is exactly a name renders as the differ's named
// segment, so ownership and diffs meet on the same string.
func collectOwned(prefix string, node map[string]any, paths *[]string, budget *int) {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if *budget <= 0 {
			return
		}
		switch {
		case key == "." || strings.HasPrefix(key, "v:"):
			if prefix != "" {
				*paths = append(*paths, prefix)
				*budget--
			}
		case strings.HasPrefix(key, "f:"):
			child := history.FieldPathKey(prefix, strings.TrimPrefix(key, "f:"))
			descend(child, node[key], paths, budget)
		case strings.HasPrefix(key, "k:"):
			child := prefix + associativeSegment(strings.TrimPrefix(key, "k:"))
			descend(child, node[key], paths, budget)
		}
	}
}

// descend recurses into a non-empty subtree or claims the leaf.
func descend(path string, value any, paths *[]string, budget *int) {
	if sub, ok := value.(map[string]any); ok && len(sub) > 0 {
		collectOwned(path, sub, paths, budget)
		return
	}
	*paths = append(*paths, path)
	*budget--
}

// associativeSegment renders one k: element key. A single "name" key
// uses the differ's named encoding; anything else renders its merge
// keys sorted, which the differ never produces and therefore never
// falsely matches.
func associativeSegment(raw string) string {
	var keyFields map[string]any
	if err := json.Unmarshal([]byte(raw), &keyFields); err != nil {
		return "[" + raw + "]"
	}
	if name, ok := keyFields["name"].(string); ok && len(keyFields) == 1 {
		return history.FieldPathNamed("", name)
	}
	keys := make([]string, 0, len(keyFields))
	for key := range keyFields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		encoded, err := json.Marshal(keyFields[key])
		if err != nil {
			continue
		}
		parts = append(parts, key+"="+string(encoded))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// actorOf reads the most recently touching field manager. It is
// Kubernetes-reported attribution — the manager's self-declared name —
// read before normalize strips the managed fields away.
func actorOf(meta map[string]any) history.Actor {
	fields, ok := meta["managedFields"].([]any)
	if !ok {
		return history.Actor{}
	}
	var actor history.Actor
	var latest time.Time
	for _, field := range fields {
		entry, ok := field.(map[string]any)
		if !ok {
			continue
		}
		at := time.Time{}
		if raw, ok := entry["time"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
				at = parsed
			}
		}
		if actor.Manager != "" && !at.After(latest) {
			continue
		}
		manager, _ := entry["manager"].(string)
		operation, _ := entry["operation"].(string)
		if manager == "" {
			continue
		}
		actor = history.Actor{Manager: manager, Operation: operation}
		latest = at
	}
	return actor
}
