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

package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/fyannk/pgconsole/internal/observe"
	"github.com/fyannk/pgconsole/internal/redact"
	"github.com/fyannk/pgconsole/internal/review"
)

// AccessReviewSource supplies the access-review snapshot. Nil means the
// panel is disabled: no source, no route, nothing to render.
type AccessReviewSource interface {
	// CurrentAccessReview returns the snapshot and whether one exists.
	CurrentAccessReview() (observe.AccessReviewSnapshot, bool)
}

// ReviewExecutor is the decision origin the review routes call into. The
// web layer renders and gates; it decides no mutation itself.
type ReviewExecutor interface {
	// Issue mints a confirmation token for an action and request name.
	Issue(name, action string) string
	// Verify checks a confirmation token for an action and request name.
	Verify(name, action, token string) bool
	// Decide validates and records the decision, auditing it.
	Decide(ctx context.Context, name, action, role, decidedBy string, knownRoles []string) (outcome string, err error)
}

// AccessReviewView is the dba review panel's view model.
type AccessReviewView struct {
	// ClusterName is the target cluster.
	ClusterName string
	// Meta is the snapshot freshness line.
	Meta SectionMeta
	// HasSnapshot reports whether any observation exists yet.
	HasSnapshot bool
	// Truncated reports a source or display safety ceiling.
	Truncated bool
	// Roles are the approval picker options, name-sorted.
	Roles []string
	// Pending are undecided requests, each with confirmation tokens.
	Pending []PendingRequestView
	// Decided are the read-only audit rows for resolved requests.
	Decided []DecidedRequestView
}

// PendingRequestView is one undecided request with its action tokens.
type PendingRequestView struct {
	// Name is the request resource name.
	Name string
	// Subject is the identity that asked for access.
	Subject string
	// Message is the bounded request justification.
	Message string
	// Age is the time since the request was created.
	Age string
	// ApproveToken guards the approve POST for this request.
	ApproveToken string
	// DenyToken guards the deny POST for this request.
	DenyToken string
}

// DecidedRequestView is one resolved request, read-only.
type DecidedRequestView struct {
	// Name is the request resource name.
	Name string
	// Subject is the identity that asked for access.
	Subject string
	// State is the decision state.
	State string
	// RequestedRole is the role recorded on an approval, else empty.
	RequestedRole string
	// DecidedBy is the reviewer identity recorded on the decision.
	DecidedBy string
	// DecidedAge is the time since the decision.
	DecidedAge string
}

// AccessDecisionResultView is the fire-and-observe result of one
// decision. The list reflects the write once the informer catches up.
type AccessDecisionResultView struct {
	// ClusterName is the target cluster.
	ClusterName string
	// Request is the decided request name.
	Request string
	// Action is the recorded action.
	Action string
	// Accepted reports the decision was written.
	Accepted bool
	// Outcome is the stable result category.
	Outcome string
}

// handleAccessRequestsIndex renders the review panel from the current
// snapshot. It performs no API call: rendering is snapshot plus template.
func (h *Handler) handleAccessRequestsIndex(w http.ResponseWriter, _ *http.Request) {
	snap, ok := h.sources.AccessReview.CurrentAccessReview()
	view := h.buildAccessReviewView(snap, ok)
	h.renderReview(w, http.StatusOK, "access-requests.html.tmpl", view)
}

// buildAccessReviewView splits the snapshot into pending rows with fresh
// confirmation tokens and read-only decided rows.
func (h *Handler) buildAccessReviewView(snap observe.AccessReviewSnapshot, ok bool) AccessReviewView {
	view := AccessReviewView{
		ClusterName: h.cfg.ClusterName,
		Meta:        buildMeta(snap.Generation, snap.ObservedAt, snap.Stale, h.now()),
		HasSnapshot: ok,
		Truncated:   snap.RequestsTruncated,
		Roles:       snap.Roles,
	}
	if !ok {
		return view
	}
	now := h.now()
	for _, req := range snap.Requests {
		if req.Pending() {
			view.Pending = append(view.Pending, PendingRequestView{
				Name:         req.Name,
				Subject:      boundMessage(req.Subject),
				Message:      boundMessage(req.Message),
				Age:          formatAge(now.Sub(req.CreatedAt)),
				ApproveToken: h.reviewer.Issue(req.Name, review.ActionApprove),
				DenyToken:    h.reviewer.Issue(req.Name, review.ActionDeny),
			})
			continue
		}
		view.Decided = append(view.Decided, DecidedRequestView{
			Name:          req.Name,
			Subject:       boundMessage(req.Subject),
			State:         string(req.State),
			RequestedRole: req.RequestedRole,
			DecidedBy:     boundMessage(req.DecidedBy),
			DecidedAge:    formatTimeAge(req.DecidedAt, req.CreatedAt, now),
		})
	}
	return view
}

// handleAccessDecision records one approve or deny. It requires a POST
// with a same-origin provenance signal and a valid CSRF token bound to
// the action and request; it then delegates to the executor. The
// reviewer identity is recorded for audit, never used to authorize — the
// dba level gate already did that.
func (h *Handler) handleAccessDecision(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !podNamePattern.MatchString(name) {
			http.NotFound(w, r)
			return
		}
		if !sameOriginPOST(r) {
			h.logger.Info("access decision refused", slog.String("reason", "cross-origin"))
			h.renderDenied(w, http.StatusForbidden, "cross-origin request refused")
			return
		}
		if err := r.ParseForm(); err != nil {
			h.renderDenied(w, http.StatusBadRequest, "malformed request")
			return
		}
		if !h.reviewer.Verify(name, action, r.PostForm.Get("csrf")) {
			h.logger.Info("access decision refused", slog.String("reason", "csrf"))
			h.renderDenied(w, http.StatusForbidden, "confirmation expired or invalid; try again")
			return
		}

		decidedBy := ""
		if h.auth.Extractor != nil {
			if id, ok := h.auth.Extractor.FromRequest(r); ok {
				decidedBy = id.User
			}
		}
		var roles []string
		if snap, ok := h.sources.AccessReview.CurrentAccessReview(); ok {
			roles = snap.Roles
		}

		outcome, err := h.reviewer.Decide(r.Context(), name, action, r.PostForm.Get("role"), decidedBy, roles)
		view := AccessDecisionResultView{
			ClusterName: h.cfg.ClusterName,
			Request:     name,
			Action:      action,
			Accepted:    err == nil,
			Outcome:     outcome,
		}
		status := http.StatusOK
		switch {
		case err == nil:
		case errors.Is(err, review.ErrUnknownRole) || errors.Is(err, review.ErrInvalidAction):
			status = http.StatusBadRequest
			view.Outcome = "rejected: the chosen role is not one of the offered options"
		default:
			status = http.StatusBadGateway
			view.Outcome = "the decision could not be recorded"
		}
		h.renderReview(w, status, "access-result.html.tmpl", view)
	}
}

// renderReview renders one access-review template.
func (h *Handler) renderReview(w http.ResponseWriter, status int, name string, view any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.tpl.ExecuteTemplate(w, name, view); err != nil {
		h.logger.Error("render failed",
			slog.String("route", "access-requests"),
			slog.String("category", redact.Safe(err)))
	}
}
