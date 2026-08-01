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

// Package web owns HTTP routing, the server-rendered templates, the
// security-header middleware, and the view models that carry claim
// attribution. Handlers render snapshots and view models; they never
// see Kubernetes types and never call the Kubernetes API — readiness's
// lightweight probe, injected as an interface, is the one exception
// this package hosts.
package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/fyannk/pgConsole/internal/authz"
	"github.com/fyannk/pgConsole/internal/evidence"
	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/observe"
	"github.com/fyannk/pgConsole/internal/redact"
	reviewpkg "github.com/fyannk/pgConsole/internal/review"
)

//go:embed templates static
var assets embed.FS

// ReadinessProber reports whether the application can serve honest
// cluster state. It is consulted by the readiness endpoint only; the
// result detail never reaches the response body.
type ReadinessProber interface {
	// Ready returns nil when the application is ready to serve.
	Ready(ctx context.Context) error
}

// SnapshotSource supplies the current cluster snapshot to render.
type SnapshotSource interface {
	// Current returns the snapshot and whether one exists.
	Current() (observe.Snapshot, bool)
}

// PodsSource supplies the current pods snapshot to render.
type PodsSource interface {
	// CurrentPods returns the snapshot and whether one exists.
	CurrentPods() (observe.PodsSnapshot, bool)
}

// EventsSource supplies the current events snapshot to render.
type EventsSource interface {
	// CurrentEvents returns the snapshot and whether one exists.
	CurrentEvents() (observe.EventsSnapshot, bool)
}

// BackupsSource supplies the current Backup and ScheduledBackup snapshot.
type BackupsSource interface {
	// CurrentBackups returns the snapshot and whether one exists.
	CurrentBackups() (observe.BackupsSnapshot, bool)
}

// PoolersSource supplies the current Pooler snapshot.
type PoolersSource interface {
	// CurrentPoolers returns the snapshot and whether one exists.
	CurrentPoolers() (observe.PoolersSnapshot, bool)
}

// FailoverQuorumSource supplies the current failover-quorum snapshot.
type FailoverQuorumSource interface {
	// CurrentFailoverQuorum returns the snapshot and whether one exists.
	CurrentFailoverQuorum() (observe.FailoverQuorumSnapshot, bool)
}

// ImageCatalogsSource supplies the current ImageCatalog snapshot.
type ImageCatalogsSource interface {
	// CurrentImageCatalogs returns the snapshot and whether one exists.
	CurrentImageCatalogs() (observe.ImageCatalogsSnapshot, bool)
}

// DatabaseObjectsSource supplies the current declarative-object
// snapshot.
type DatabaseObjectsSource interface {
	// CurrentDatabaseObjects returns the snapshot and whether one
	// exists.
	CurrentDatabaseObjects() (observe.DatabaseObjectsSnapshot, bool)
}

// EvidenceSource supplies the current repository-evidence status.
type EvidenceSource interface {
	// CurrentEvidence returns the status.
	CurrentEvidence() evidence.Status
}

// Sources bundles the snapshot suppliers of the page.
type Sources struct {
	// Cluster supplies the cluster snapshot.
	Cluster SnapshotSource
	// Pods supplies the pods snapshot.
	Pods PodsSource
	// Events supplies the events snapshot.
	Events EventsSource
	// Backups supplies the Backup and ScheduledBackup snapshot.
	Backups BackupsSource
	// Poolers supplies the Pooler snapshot.
	Poolers PoolersSource
	// FailoverQuorum supplies the failover-quorum snapshot.
	FailoverQuorum FailoverQuorumSource
	// ImageCatalogs supplies the ImageCatalog snapshot.
	ImageCatalogs ImageCatalogsSource
	// DatabaseObjects supplies the declarative-object snapshot.
	DatabaseObjects DatabaseObjectsSource
	// Evidence supplies the repository-evidence status. Nil means the
	// consumer is disabled: no section, no panel, nothing to probe.
	Evidence EvidenceSource
	// AccessReview supplies the access-request review snapshot. Nil means
	// the review panel is disabled: no source, no route, no writer.
	AccessReview AccessReviewSource
}

// LogTailer performs one bounded, on-demand log fetch for a verified
// member pod. It is one of the closed request-time API exceptions.
type LogTailer interface {
	// TailLogs fetches the bounded tail; a non-member pod is not found.
	TailLogs(ctx context.Context, pod string) (observe.LogTail, error)
}

// Auth bundles the identity capability of the handler. The authorization
// level is not decided here: it is read from the trusted level header per
// request and parsed by authz.ParseLevel.
type Auth struct {
	// Extractor reads the forwarded identity; nil disables extraction and
	// denies every level-gated route.
	Extractor *identity.Extractor
}

// Config is the static identity and link configuration of the handler.
type Config struct {
	// ClusterName is the configured target cluster.
	ClusterName string
	// Namespace is the configured target namespace.
	Namespace string
	// EventsWindow is the configured event age window.
	EventsWindow time.Duration
	// AllowLogs enables the log tail routes and affordances. Disabled,
	// there is no route, no link, and nothing to probe.
	AllowLogs bool
	// LevelHeader is the trusted proxy header carrying the authorization
	// level. Empty leaves only the read-only baseline: no level is ever
	// asserted, so no level-gated route can be reached.
	LevelHeader string
	// AllowOperations enables the enumerated day-2 operation routes.
	// Disabled, no operation route is registered and no writer exists.
	AllowOperations bool
	// AllowAccessReview enables the dba access-request review panel.
	// Disabled, no review route is registered and no writer exists.
	AllowAccessReview bool
	// Links are the operator-configured link-outs.
	Links Links
}

// Handler serves the console routes.
type Handler struct {
	cfg      Config
	sources  Sources
	prober   ReadinessProber
	tailer   LogTailer
	auth     Auth
	executor OpsExecutor
	reviewer ReviewExecutor
	now      func() time.Time
	logger   *slog.Logger
	tpl      *template.Template
}

// New builds the Handler. now supplies the time used for snapshot ages;
// production passes the clock's Now, tests pass a fixed instant. tailer
// may be nil when no Kubernetes access exists; the log routes then
// report unavailability. executor is non-nil only in operations mode and
// reviewer only in access-review mode; their absence is what makes the
// corresponding assembly graph writer-free.
func New(cfg Config, sources Sources, prober ReadinessProber, tailer LogTailer, auth Auth, executor OpsExecutor, reviewer ReviewExecutor, now func() time.Time, logger *slog.Logger) (*Handler, error) {
	tpl, err := template.New("pgconsole").Funcs(templateFuncs).ParseFS(assets, "templates/*.tmpl")
	if err != nil {
		return nil, redact.NewError("template parse", redact.CategoryInternal, err)
	}
	return &Handler{
		cfg:      cfg,
		sources:  sources,
		prober:   prober,
		tailer:   tailer,
		auth:     auth,
		executor: executor,
		reviewer: reviewer,
		now:      now,
		logger:   logger,
		tpl:      tpl,
	}, nil
}

// Routes returns the complete route set wrapped in the security-header
// middleware. Every state-reading route is GET; no route has side
// effects. The log route exists only when logs are allowed: disabled
// mode has no route to probe, not a route that refuses. Console routes
// pass the tier gate; health, readiness, and static assets do not —
// probes and stylesheets carry no cluster state.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	// The read-only status baseline is ungated: reaching the console means
	// the proxy already authenticated the request, and the deployment
	// confines ingress to that proxy. Each section is its own screen; the
	// Overview restates them in plain language.
	mux.HandleFunc("GET /{$}", h.handleIndex)
	mux.HandleFunc("GET /cluster/status", h.handleClusterStatus)
	mux.HandleFunc("GET /cluster/pods", h.handleClusterPods)
	mux.HandleFunc("GET /cluster/events", h.handleClusterEvents)
	mux.HandleFunc("GET /cluster/logs", h.handleClusterLogs)
	mux.HandleFunc("GET /backups", h.handleBackupsOverview)
	mux.HandleFunc("GET /backups/objects", h.handleBackupsObjects)
	mux.HandleFunc("GET /backups/evidence", h.handleBackupsEvidence)
	mux.HandleFunc("GET /databases", h.handleDatabases("databases-overview"))
	mux.HandleFunc("GET /databases/roles", h.handleDatabases("databases-roles"))
	mux.HandleFunc("GET /databases/publications", h.handleDatabases("databases-publications"))
	mux.HandleFunc("GET /databases/subscriptions", h.handleDatabases("databases-subscriptions"))
	mux.HandleFunc("GET /poolers", h.handlePoolers("poolers-overview"))
	mux.HandleFunc("GET /poolers/pods", h.handlePoolers("poolers-pods"))
	mux.HandleFunc("GET /poolers/logs", h.handlePoolers("poolers-logs"))
	mux.HandleFunc("GET /healthz", h.handleHealthz)
	mux.HandleFunc("GET /readyz", h.handleReadyz)
	// The instance log tail sits above the baseline: it requires the
	// poweruser level or above, and the affordance is hidden below it.
	if h.cfg.AllowLogs {
		mux.HandleFunc("GET /logs/{pod}", h.requireLevel(authz.TierPowerUser, "log access requires the poweruser or dba level", h.handleLogs))
	}
	// Operation routes exist only in operations mode with a wired
	// executor: disabled mode registers no route to abuse. They require
	// the poweruser level or above.
	if h.cfg.AllowOperations && h.executor != nil {
		operate := func(next http.HandlerFunc) http.HandlerFunc {
			return h.requireLevel(authz.TierPowerUser, "operations require the poweruser or dba level", next)
		}
		mux.HandleFunc("GET /operations", operate(h.handleOperationsIndex))
		mux.HandleFunc("GET /operations/{op}", operate(h.handleOperationConfirm))
		mux.HandleFunc("POST /operations/{op}", operate(h.handleOperationExecute))
	}
	// The access-request review panel exists only in access-review mode
	// with a wired reviewer and source: disabled mode registers no route.
	// It requires the dba level.
	if h.cfg.AllowAccessReview && h.reviewer != nil && h.sources.AccessReview != nil {
		review := func(next http.HandlerFunc) http.HandlerFunc {
			return h.requireLevel(authz.TierDBA, "the review panel requires the dba level", next)
		}
		mux.HandleFunc("GET /access-requests", review(h.handleAccessRequestsIndex))
		mux.HandleFunc("POST /access-requests/{name}/approve", review(h.handleAccessDecision(reviewpkg.ActionApprove)))
		mux.HandleFunc("POST /access-requests/{name}/deny", review(h.handleAccessDecision(reviewpkg.ActionDeny)))
	}
	mux.Handle("GET /static/", http.FileServerFS(assets))
	return securityHeaders(mux)
}

// securityHeaders is the single place response security headers are set.
// It applies to every response, including errors and unknown routes.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Cache-Control", "no-store")
		// script-src is 'self' only: the enhancement layer is embedded and
		// served from this binary, never fetched. 'unsafe-eval' is
		// deliberately absent — the vendored Alpine is the CSP build,
		// which parses its own restricted expression grammar instead of
		// calling new Function(). connect-src stays at default-src 'none'
		// so the scripts cannot originate a request of any kind.
		header.Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// assemble gathers the current snapshots and derives the page view once,
// then sets the shared shell for the named section. Every screen renders
// a slice of this same page: the build is snapshots plus derivation with
// no API call, so a per-section handler costs nothing the full page did
// not.
func (h *Handler) assemble(r *http.Request, current string) Page {
	// The log affordance follows the same poweruser gate as the route, so
	// a viewer never sees a link that would deny.
	logsVisible := h.cfg.AllowLogs && h.level(r) >= authz.TierPowerUser
	s := snapshots{window: h.cfg.EventsWindow, allowLogs: logsVisible}
	s.cluster, s.ok = h.sources.Cluster.Current()
	s.pods, s.podsOK = h.sources.Pods.CurrentPods()
	s.events, s.eventsOK = h.sources.Events.CurrentEvents()
	s.backups, s.backupsOK = h.sources.Backups.CurrentBackups()
	s.poolers, s.poolersOK = h.sources.Poolers.CurrentPoolers()
	s.quorum, s.quorumOK = h.sources.FailoverQuorum.CurrentFailoverQuorum()
	s.catalogs, s.catalogsOK = h.sources.ImageCatalogs.CurrentImageCatalogs()
	s.declared, s.declaredOK = h.sources.DatabaseObjects.CurrentDatabaseObjects()
	if h.sources.Evidence != nil {
		s.evidence = h.sources.Evidence.CurrentEvidence()
		s.evidenceEnabled = true
	}
	page := buildPage(h.cfg.ClusterName, h.cfg.Namespace, s, h.now(), h.cfg.Links)
	page.Identity = h.buildIdentityView(r)
	page.OperationsEnabled = h.cfg.AllowOperations && h.executor != nil
	page.Shell = h.shell(r, current)
	page.Shell.SnapshotState = page.SnapshotState
	page.Shell.SnapshotAge = page.SnapshotAge
	page.Shell.Generation = page.Generation
	page.Shell.Identity = page.Identity
	page.Shell.Links = page.Links
	return page
}

// renderPage writes one screen template, logging a render failure as a
// category only.
func (h *Handler) renderPage(w http.ResponseWriter, route, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tpl.ExecuteTemplate(w, name, data); err != nil {
		h.logger.Error("render failed",
			slog.String("route", route),
			slog.String("category", redact.Safe(err)))
	}
}

// handleIndex renders the plain-language Overview: the derived summary
// that opens the console, restating the attributed sections in one
// screen. The detail lives on the section screens the sidebar maps.
func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, "index", "index.html.tmpl", h.assemble(r, "overview"))
}

// handleClusterStatus renders the operator-reported cluster verdict,
// topology, and conditions.
func (h *Handler) handleClusterStatus(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, "cluster-status", "cluster-status.html.tmpl", h.assemble(r, "cluster-status"))
}

// handleClusterPods renders the Kubernetes-observed instance pods.
func (h *Handler) handleClusterPods(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, "cluster-pods", "cluster-pods.html.tmpl", h.assemble(r, "cluster-pods"))
}

// handleClusterEvents renders the age-windowed cluster events.
func (h *Handler) handleClusterEvents(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, "cluster-events", "cluster-events.html.tmpl", h.assemble(r, "cluster-events"))
}

// handleClusterLogs renders the per-pod log-tail launch points; the tail
// itself is the poweruser-gated /logs route.
func (h *Handler) handleClusterLogs(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, "cluster-logs", "cluster-logs.html.tmpl", h.assemble(r, "cluster-logs"))
}

// handleBackupsOverview renders the backup catalog verdict and the
// operator-versus-repository cross-check.
func (h *Handler) handleBackupsOverview(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, "backups", "backups-overview.html.tmpl", h.assemble(r, "backups-overview"))
}

// handleBackupsObjects renders the Backup and ScheduledBackup catalogs
// and the ObjectStore reference lookup.
func (h *Handler) handleBackupsObjects(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, "backups-objects", "backups-objects.html.tmpl", h.assemble(r, "backups-objects"))
}

// handleBackupsEvidence renders the repository-evidence section.
func (h *Handler) handleBackupsEvidence(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, "backups-evidence", "backups-evidence.html.tmpl", h.assemble(r, "backups-evidence"))
}

// handleDatabases renders the declarative-objects section. The four
// entries share one screen and one snapshot: they are four lists of the
// same kind of claim — what was declared and what the operator did with
// it — and giving each its own freshness would let one screen disagree
// with itself about how current it is. The named key sets which sidebar
// entry reads as current and which list the screen opens on.
func (h *Handler) handleDatabases(current string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.renderPage(w, current, "databases.html.tmpl", h.assemble(r, current))
	}
}

// handlePoolers renders the poolers section. The three entries share one
// screen: a Pooler's pods and logs are the Deployment's, which this
// console does not observe separately, so splitting them would promise
// detail it does not have. The named key sets which sidebar entry reads
// as current.
func (h *Handler) handlePoolers(current string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.renderPage(w, current, "poolers.html.tmpl", h.assemble(r, current))
	}
}

// shell builds the chrome shared by every page. It carries no cluster
// fact of its own: pages that have a snapshot copy their own state onto
// it after calling this, and pages that do not simply leave those fields
// empty so the top bar renders no snapshot line rather than a false one.
func (h *Handler) shell(r *http.Request, current string) ShellView {
	return ShellView{
		ClusterName:         h.cfg.ClusterName,
		Namespace:           h.cfg.Namespace,
		Identity:            h.buildIdentityView(r),
		Links:               buildLinks(h.cfg.Links),
		OperationsEnabled:   h.cfg.AllowOperations && h.executor != nil,
		AccessReviewEnabled: h.cfg.AllowAccessReview && h.reviewer != nil,
		Current:             current,
	}
}

// requireLevel gates a route on a minimum proxy-asserted level. A usable
// forwarded identity must be present — the operation is attributed to it
// — and the parsed level must meet the minimum. Both the identity and the
// level are proxy-asserted, trustworthy under the deployment's ingress
// confinement invariant; the console probes nothing. Denials render
// categories only; neither the forwarded identity nor the asserted level
// value ever enters a log line.
func (h *Handler) requireLevel(min authz.Tier, requirement string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.auth.Extractor == nil {
			h.renderDenied(w, r, http.StatusServiceUnavailable,
				"level gating unavailable without a trusted identity header")
			return
		}
		if _, ok := h.auth.Extractor.FromRequest(r); !ok {
			h.logger.Info("denied", slog.String("reason", "no-identity"))
			h.renderDenied(w, r, http.StatusForbidden,
				"identity required: the proxy forwarded no usable identity")
			return
		}
		if h.level(r) < min {
			h.logger.Info("denied", slog.String("reason", "insufficient-level"))
			h.renderDenied(w, r, http.StatusForbidden, "not authorized: "+requirement)
			return
		}
		next(w, r)
	}
}

// level parses the proxy-asserted authorization level for the request.
// With no level header configured, no level is ever asserted and every
// request stays at TierNone — the read-only baseline.
func (h *Handler) level(r *http.Request) authz.Tier {
	if h.cfg.LevelHeader == "" {
		return authz.TierNone
	}
	return authz.ParseLevel(r.Header.Get(h.cfg.LevelHeader))
}

// renderDenied writes the constant denial page.
func (h *Handler) renderDenied(w http.ResponseWriter, r *http.Request, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.tpl.ExecuteTemplate(w, "denied.html.tmpl", DeniedView{Shell: h.shell(r, ""), Message: message}); err != nil {
		h.logger.Error("render failed",
			slog.String("route", "denied"),
			slog.String("category", redact.Safe(err)))
	}
}

// buildIdentityView prepares the display-only identity line. Both the
// user and the level are proxy-asserted — trustworthy under the
// deployment's ingress-confinement invariant, never the user's own RBAC —
// and the label says so.
func (h *Handler) buildIdentityView(r *http.Request) *IdentityView {
	if h.auth.Extractor == nil {
		return nil
	}
	id, ok := h.auth.Extractor.FromRequest(r)
	if !ok {
		return nil
	}
	return &IdentityView{User: id.User, Level: h.level(r).String(), Label: "proxy-asserted"}
}

// podNamePattern bounds the pod path parameter to a DNS-1123 subdomain
// shape before anything touches the API.
var podNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)

// handleLogs serves one bounded, on-demand log tail for a verified
// member pod. This is a closed request-time API exception: the fetch
// runs on the request's context, so a client disconnect cancels it;
// nothing is cached or persisted. A non-member pod is indistinguishable
// from a nonexistent one.
func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request) {
	pod := r.PathValue("pod")
	if !podNamePattern.MatchString(pod) {
		http.NotFound(w, r)
		return
	}
	view := LogsView{
		Shell:       h.shell(r, ""),
		ClusterName: h.cfg.ClusterName,
		Pod:         pod,
		Origin:      OriginKubernetes,
	}
	status := http.StatusOK
	if h.tailer == nil {
		status = http.StatusServiceUnavailable
		view.State = "unavailable: no Kubernetes access"
	} else {
		tail, err := h.tailer.TailLogs(r.Context(), pod)
		switch {
		case err == nil:
			view.Bounds = fmt.Sprintf("last %d lines, at most %d bytes", tail.LineLimit, tail.ByteLimit)
			view.Content = tail.Content
			view.Truncated = tail.TruncatedByBytes
		case redact.Categorize(err) == redact.CategoryNotFound:
			status = http.StatusNotFound
			view.State = "no such member pod"
		case redact.Categorize(err) == redact.CategoryForbidden:
			status = http.StatusServiceUnavailable
			view.State = "not granted: pods/log"
		default:
			status = http.StatusServiceUnavailable
			view.State = "unavailable: " + redact.Safe(err)
		}
		h.logger.Info("log tail",
			slog.String("route", "logs"),
			slog.String("pod", pod),
			slog.String("category", redact.Safe(err)))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.tpl.ExecuteTemplate(w, "logs.html.tmpl", view); err != nil {
		h.logger.Error("render failed",
			slog.String("route", "logs"),
			slog.String("category", redact.Safe(err)))
	}
}

// handleHealthz proves process liveness and nothing else.
func (h *Handler) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// handleReadyz reports readiness. The response body is a constant: probe
// detail is logged as a category and never rendered, so readiness can
// never reveal cluster state.
func (h *Handler) handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := h.prober.Ready(r.Context()); err != nil {
		h.logger.Info("not ready",
			slog.String("route", "readyz"),
			slog.String("category", redact.Safe(err)))
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// EmptySnapshots is a SnapshotSource and PodsSource with no snapshot,
// wired when no collector exists.
type EmptySnapshots struct{}

// Current reports no cluster snapshot.
func (EmptySnapshots) Current() (observe.Snapshot, bool) {
	return observe.Snapshot{}, false
}

// CurrentPods reports no pods snapshot.
func (EmptySnapshots) CurrentPods() (observe.PodsSnapshot, bool) {
	return observe.PodsSnapshot{}, false
}

// CurrentEvents reports no events snapshot.
func (EmptySnapshots) CurrentEvents() (observe.EventsSnapshot, bool) {
	return observe.EventsSnapshot{}, false
}

// CurrentBackups reports no backup snapshot.
func (EmptySnapshots) CurrentBackups() (observe.BackupsSnapshot, bool) {
	return observe.BackupsSnapshot{}, false
}

// CurrentPoolers reports no pooler snapshot.
func (EmptySnapshots) CurrentPoolers() (observe.PoolersSnapshot, bool) {
	return observe.PoolersSnapshot{}, false
}

// CurrentFailoverQuorum reports no failover-quorum snapshot.
func (EmptySnapshots) CurrentFailoverQuorum() (observe.FailoverQuorumSnapshot, bool) {
	return observe.FailoverQuorumSnapshot{}, false
}

// CurrentImageCatalogs reports no image-catalog snapshot.
func (EmptySnapshots) CurrentImageCatalogs() (observe.ImageCatalogsSnapshot, bool) {
	return observe.ImageCatalogsSnapshot{}, false
}

// CurrentDatabaseObjects reports no declarative-object snapshot.
func (EmptySnapshots) CurrentDatabaseObjects() (observe.DatabaseObjectsSnapshot, bool) {
	return observe.DatabaseObjectsSnapshot{}, false
}
