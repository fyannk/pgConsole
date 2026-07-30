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

// Package ops is the sole origin and authorization point for the
// enumerated day-2 operations. It holds the narrow mutation writer, the
// closed operation catalog, the CSRF mechanism, and the audit log.
// There is no generic apply, no spec editing, and no free-form patch:
// each operation is one typed, minimal, version-pinned interaction. The
// writer is constructed only in operations mode, so a read-only
// assembly graph contains no mutation path at all.
package ops

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/fyannk/pgconsole/internal/observe"
	"github.com/fyannk/pgconsole/internal/redact"
)

// Writer is the narrow mutation transport. Its implementation lives in
// the kube package (the transport), but only this package constructs
// and holds it, and only in operations mode.
type Writer interface {
	// CreateBackup requests one on-demand backup.
	CreateBackup(ctx context.Context, name string) error
	// ReloadCluster triggers a configuration reload.
	ReloadCluster(ctx context.Context, at time.Time) error
	// RestartCluster triggers a rolling restart.
	RestartCluster(ctx context.Context, at time.Time) error
	// PromoteInstance requests a switchover to the named instance.
	PromoteInstance(ctx context.Context, instance string, at time.Time) error
}

// ID is one enumerated operation. The set is closed.
type ID string

// The operation catalog.
const (
	// Backup requests an on-demand backup.
	Backup ID = "backup"
	// Reload reloads the cluster configuration.
	Reload ID = "reload"
	// Restart rolls the whole cluster.
	Restart ID = "restart"
	// Promote switches over to a chosen replica.
	Promote ID = "promote"
)

// Descriptor describes one operation for rendering and validation.
type Descriptor struct {
	// ID is the operation identifier.
	ID ID
	// Title is the human name.
	Title string
	// Summary is one sentence on the effect.
	Summary string
	// NeedsInstance reports that the operation targets a named instance.
	NeedsInstance bool
}

// catalog is the closed, ordered operation set.
var catalog = []Descriptor{
	{ID: Backup, Title: "Request on-demand backup", Summary: "Creates one Backup resource referencing this cluster."},
	{ID: Reload, Title: "Reload configuration", Summary: "Triggers a configuration reload across the cluster."},
	{ID: Restart, Title: "Restart cluster", Summary: "Triggers a rolling restart of every instance."},
	{ID: Promote, Title: "Promote a replica (switchover)", Summary: "Switches the primary role to the chosen instance.", NeedsInstance: true},
}

// Catalog returns the closed operation set.
func Catalog() []Descriptor { return catalog }

// Describe returns the descriptor for id, or false if id is not in the
// catalog. An operation not in the catalog does not exist.
func Describe(id ID) (Descriptor, bool) {
	for _, d := range catalog {
		if d.ID == id {
			return d, true
		}
	}
	return Descriptor{}, false
}

// Identity is the audited actor: the forwarded value and whether the
// trusted proxy channel makes it verifiable.
type Identity struct {
	// User is the forwarded username, possibly empty.
	User string
	// Verified reports that the identity is proxy-asserted over the
	// trusted channel.
	Verified bool
}

// Executor performs the enumerated operations. It is fire-and-observe:
// it creates or patches, writes one audit line, and returns — the
// console then shows the operator's reported progress. It runs no
// retries, scheduling, or orchestration.
type Executor struct {
	writer Writer
	clock  observe.Clock
	csrf   *CSRF
	logger *slog.Logger
}

// NewExecutor wires an executor over the writer. It is constructed only
// in operations mode.
func NewExecutor(writer Writer, csrf *CSRF, clock observe.Clock, logger *slog.Logger) *Executor {
	return &Executor{writer: writer, clock: clock, csrf: csrf, logger: logger}
}

// Catalog exposes the closed operation set to the caller.
func (e *Executor) Catalog() []Descriptor { return Catalog() }

// Issue mints a CSRF token binding the operation and target.
func (e *Executor) Issue(id ID, target string) string {
	return e.csrf.Issue(string(id) + "\x00" + target)
}

// Verify checks a CSRF token for the operation and target.
func (e *Executor) Verify(id ID, target, token string) bool {
	return e.csrf.Verify(string(id)+"\x00"+target, token)
}

// Execute runs one operation and writes exactly one audit line. It
// returns the outcome category, safe for display and identical to the
// audited outcome. An unknown operation or a promote without an
// instance is a rejected request, not a mutation.
func (e *Executor) Execute(ctx context.Context, id ID, target string, actor Identity) (outcome string, err error) {
	desc, ok := Describe(id)
	if !ok {
		return "rejected", redact.NewError("operation", redact.CategoryNotFound, nil)
	}
	if desc.NeedsInstance && target == "" {
		return "rejected", redact.NewError("operation", redact.CategoryInternal, nil)
	}

	now := e.clock.Now()
	switch id {
	case Backup:
		err = e.writer.CreateBackup(ctx, e.backupName(now))
	case Reload:
		err = e.writer.ReloadCluster(ctx, now)
	case Restart:
		err = e.writer.RestartCluster(ctx, now)
	case Promote:
		err = e.writer.PromoteInstance(ctx, target, now)
	}

	outcome = "accepted"
	if err != nil {
		outcome = redact.Safe(err)
	}
	e.audit(id, target, outcome, actor)
	return outcome, err
}

// backupName derives a deterministic backup name from the clock.
func (e *Executor) backupName(now time.Time) string {
	return "ondemand-" + strconv.FormatInt(now.Unix(), 10)
}

// audit writes the one structured operation line: operation, target,
// outcome category, and the actor with its verification label. The
// actor value is recorded as supplied — labeled proxy-asserted over the
// trusted channel, unverified otherwise — and never used for
// authorization here.
func (e *Executor) audit(id ID, target, outcome string, actor Identity) {
	verification := "unverified"
	if actor.Verified {
		verification = "proxy-asserted"
	}
	e.logger.Info("operation",
		slog.String("operation", string(id)),
		slog.String("target", target),
		slog.String("outcome", outcome),
		slog.String("actor", actor.User),
		slog.String("actor_verification", verification))
}
