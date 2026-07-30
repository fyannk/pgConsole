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
	"log/slog"
	"net/http"

	"github.com/fyannk/pgconsole/internal/ops"
	"github.com/fyannk/pgconsole/internal/redact"
)

// OpsExecutor is the mutation origin the operation routes call into.
// The web layer decides nothing about mutation itself: it renders
// confirmation, checks CSRF and request provenance, and delegates.
type OpsExecutor interface {
	// Catalog returns the closed operation set.
	Catalog() []ops.Descriptor
	// Issue mints a CSRF token for an operation and target.
	Issue(id ops.ID, target string) string
	// Verify checks a CSRF token for an operation and target.
	Verify(id ops.ID, target, token string) bool
	// Execute performs the operation and audits it.
	Execute(ctx context.Context, id ops.ID, target string, actor ops.Identity) (outcome string, err error)
}

// handleOperationsIndex lists the enumerated operations. It exists only
// in operations mode; there is no route otherwise.
func (h *Handler) handleOperationsIndex(w http.ResponseWriter, _ *http.Request) {
	view := OperationsView{ClusterName: h.cfg.ClusterName, Operations: h.executor.Catalog()}
	h.renderOps(w, http.StatusOK, "operations.html.tmpl", view)
}

// handleOperationConfirm renders the confirmation form with a fresh
// CSRF token. State-changing routes are never GET, so this GET has no
// side effect: it only mints a token bound to the operation and target.
func (h *Handler) handleOperationConfirm(w http.ResponseWriter, r *http.Request) {
	desc, ok := ops.Describe(ops.ID(r.PathValue("op")))
	if !ok {
		http.NotFound(w, r)
		return
	}
	target := r.URL.Query().Get("instance")
	if desc.NeedsInstance && !podNamePattern.MatchString(target) {
		target = ""
	}
	view := ConfirmView{
		ClusterName: h.cfg.ClusterName,
		Op:          desc,
		Target:      target,
		CSRFToken:   h.executor.Issue(desc.ID, target),
	}
	h.renderOps(w, http.StatusOK, "confirm.html.tmpl", view)
}

// handleOperationExecute performs one operation. It requires a POST
// with a same-origin provenance signal and a valid CSRF token bound to
// the operation and target; it then delegates to the executor and shows
// fire-and-observe result. The forwarded identity is recorded for
// audit, never used to authorize — the tier gate already did that.
func (h *Handler) handleOperationExecute(w http.ResponseWriter, r *http.Request) {
	desc, ok := ops.Describe(ops.ID(r.PathValue("op")))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !sameOriginPOST(r) {
		h.logger.Info("operation refused", slog.String("reason", "cross-origin"))
		h.renderDenied(w, http.StatusForbidden, "cross-origin request refused")
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderDenied(w, http.StatusBadRequest, "malformed request")
		return
	}
	target := r.PostForm.Get("instance")
	if desc.NeedsInstance && !podNamePattern.MatchString(target) {
		h.renderDenied(w, http.StatusBadRequest, "a valid instance name is required")
		return
	}
	if !desc.NeedsInstance {
		target = ""
	}
	if !h.executor.Verify(desc.ID, target, r.PostForm.Get("csrf")) {
		h.logger.Info("operation refused", slog.String("reason", "csrf"))
		h.renderDenied(w, http.StatusForbidden, "confirmation expired or invalid; try again")
		return
	}

	// This route is reached only past the poweruser level gate, which
	// requires a usable forwarded identity; the actor is therefore
	// present and proxy-asserted under the deployment invariant.
	actor := ops.Identity{}
	if h.auth.Extractor != nil {
		if id, ok := h.auth.Extractor.FromRequest(r); ok {
			actor = ops.Identity{User: id.User, Verified: true}
		}
	}

	outcome, err := h.executor.Execute(r.Context(), desc.ID, target, actor)
	view := ResultView{
		ClusterName: h.cfg.ClusterName,
		Op:          desc,
		Target:      target,
		Accepted:    err == nil,
		Outcome:     outcome,
	}
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadGateway
	}
	h.renderOps(w, status, "result.html.tmpl", view)
}

// renderOps renders one operations template.
func (h *Handler) renderOps(w http.ResponseWriter, status int, name string, view any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.tpl.ExecuteTemplate(w, name, view); err != nil {
		h.logger.Error("render failed",
			slog.String("route", "operations"),
			slog.String("category", redact.Safe(err)))
	}
}

// sameOriginPOST checks the browser-set Sec-Fetch-Site provenance
// signal, which a cross-site attacker cannot forge. A missing header
// (an older client) falls through to the CSRF token as the sole
// defense; a present cross-site value is refused outright.
func sameOriginPOST(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return false
	default:
		return true
	}
}
