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

// Package review is the origin of the one access-request mutation: it
// records a reviewer's approve or deny on the status subresource of a
// PgToolBoxAccessRequest. It creates no user, role, or proxy
// configuration; the operator's controller materializes the user after
// an approval. The web layer renders and gates; this package validates
// the decision, stamps the reviewer and time, and delegates the single
// write to the narrow Kubernetes transport.
package review

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/fyannk/pgConsole/internal/authz"
	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/ops"
	"github.com/fyannk/pgConsole/internal/redact"
)

// The review actions, the closed set the routes and CSRF bind to.
const (
	// ActionApprove records an approval with a chosen level.
	ActionApprove = "approve"
	// ActionDeny records a denial without a level.
	ActionDeny = "deny"
)

// ErrInvalidAction reports a review action outside the closed set.
var ErrInvalidAction = errors.New("invalid review action")

// ErrUnknownLevel reports an approval naming a level outside the closed
// grantable set — the picker's own options are the only acceptable
// values, so a tampered form cannot mint one the operator would refuse.
var ErrUnknownLevel = errors.New("unknown level")

// Writer performs the single status-subresource write. The Kubernetes
// client implements it; it is the only mutation this package can cause.
type Writer interface {
	// WriteAccessRequestStatus records the decision on the named
	// request's status subresource. level is empty for a denial.
	WriteAccessRequestStatus(ctx context.Context, name, state, level, decidedBy string, decidedAt time.Time) error
}

// Executor validates and records access-request decisions, issuing and
// verifying the confirmation tokens that guard the write.
type Executor struct {
	writer Writer
	csrf   *ops.CSRF
	clock  observe.Clock
	logger *slog.Logger
}

// NewExecutor wires an executor over the writer and a CSRF issuer.
func NewExecutor(writer Writer, csrf *ops.CSRF, clock observe.Clock, logger *slog.Logger) *Executor {
	return &Executor{writer: writer, csrf: csrf, clock: clock, logger: logger}
}

// Issue mints a confirmation token binding one action and request name.
func (e *Executor) Issue(name, action string) string {
	return e.csrf.Issue(tokenContext(name, action))
}

// Verify checks a confirmation token for one action and request name.
func (e *Executor) Verify(name, action, token string) bool {
	return e.csrf.Verify(tokenContext(name, action), token)
}

func tokenContext(name, action string) string { return action + "\x00" + name }

// Decide validates and records one decision. An approval must name a
// level in the closed grantable set, so a tampered form cannot mint one
// the operator's enum would refuse. decidedBy is the reviewer identity,
// recorded as supplied and never used to authorize here; the level gate
// already did that. The outcome is a stable category for the result page.
func (e *Executor) Decide(ctx context.Context, name, action, level, decidedBy string) (outcome string, err error) {
	var state, granted string
	switch action {
	case ActionApprove:
		if !grantable(level) {
			e.audit(name, action, "", decidedBy, "rejected: unknown level")
			return "", ErrUnknownLevel
		}
		state, granted = string(observe.AccessRequestApproved), level
	case ActionDeny:
		state = string(observe.AccessRequestDenied)
	default:
		return "", ErrInvalidAction
	}

	if err := e.writer.WriteAccessRequestStatus(ctx, name, state, granted, decidedBy, e.clock.Now()); err != nil {
		e.audit(name, action, granted, decidedBy, "write failed: "+redact.Safe(err))
		return "", err
	}
	e.audit(name, action, granted, decidedBy, "recorded")
	return state, nil
}

// grantable reports whether the level is one the console may record.
// The set comes from internal/authz rather than from anything observed:
// it is the same closed ladder the console admits routes by, and it
// matches the operator's RoleLevel enum value for value.
func grantable(level string) bool {
	for _, allowed := range authz.GrantableLevels() {
		if level == allowed {
			return true
		}
	}
	return false
}

// audit writes the one structured decision line: action, request, role,
// outcome category, and the reviewer with its proxy-asserted label. The
// reviewer value is recorded as supplied and never used for
// authorization here.
func (e *Executor) audit(name, action, role, decidedBy, outcome string) {
	e.logger.Info("access request decision",
		slog.String("action", action),
		slog.String("request", name),
		slog.String("role", role),
		slog.String("outcome", outcome),
		slog.String("reviewer", decidedBy),
		slog.String("reviewer_verification", "proxy-asserted"))
}
