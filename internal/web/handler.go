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
	"github.com/fyannk/pgConsole/internal/history"
	"github.com/fyannk/pgConsole/internal/identity"
	"github.com/fyannk/pgConsole/internal/metrics"
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

// PoolerPodsSource supplies the current pooler pod snapshot.
type PoolerPodsSource interface {
	// CurrentPoolerPods returns the snapshot and whether one exists.
	CurrentPoolerPods() (observe.PodsSnapshot, bool)
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

// InfrastructureSource supplies the cluster's observed services,
// volume claims and volume snapshots.
type InfrastructureSource interface {
	// CurrentInfrastructure returns the snapshot and whether one exists.
	CurrentInfrastructure() (observe.InfrastructureSnapshot, bool)
}

// EvidenceSource supplies the current repository-evidence status.
type EvidenceSource interface {
	// CurrentEvidence returns the status.
	CurrentEvidence() evidence.Status
}

// HistorySource supplies the bounded object-definition timeline and its
// on-demand revision details. Reads are in-memory snapshots; they never call
// Kubernetes and never create another watch.
type HistorySource interface {
	// Snapshot returns the retained manifest-free timeline.
	Snapshot() (history.Snapshot, bool)
	// Revision resolves one retained, scrubbed definition.
	Revision(seq uint64) (history.Revision, bool)
	// Diff compares a revision with its previous retained definition.
	Diff(seq uint64) (history.Diff, bool)
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
	// PoolerPods supplies the pooler pod snapshot.
	PoolerPods PoolerPodsSource
	// FailoverQuorum supplies the failover-quorum snapshot.
	FailoverQuorum FailoverQuorumSource
	// ImageCatalogs supplies the ImageCatalog snapshot.
	ImageCatalogs ImageCatalogsSource
	// DatabaseObjects supplies the declarative-object snapshot.
	DatabaseObjects DatabaseObjectsSource
	// Infrastructure supplies the services, volume claims and volume
	// snapshots. Nil means they were never observed.
	Infrastructure InfrastructureSource
	// Evidence supplies the repository-evidence status. Nil means the
	// consumer is disabled: no section, no panel, nothing to probe.
	Evidence EvidenceSource
	// AccessReview supplies the access-request review snapshot. Nil means
	// the review panel is disabled: no source, no route, no writer.
	AccessReview AccessReviewSource
	// History supplies the object-definition timeline. Nil means history is
	// disabled and no history route is registered.
	History HistorySource
	// Metrics supplies the bounded instance-metrics window. Nil means
	// metrics are disabled and no metrics route is registered.
	Metrics MetricsSource
}

// MetricsSource supplies the bounded instance-metrics window the
// scraper fills. Implemented by *metrics.Store.
type MetricsSource interface {
	// Instances lists the tracked instance names, sorted.
	Instances() []string
	// Range reads one series at one tier as aligned columns.
	Range(key string, tier metrics.Tier) (times []int64, byInstance map[string][]*float64)
	// SeriesStats summarises one series per instance.
	SeriesStats(key string) map[string]metrics.Stats
	// InstantReadings returns every instance's latest point-in-time
	// claims, keyed by instance then by Instants key.
	InstantReadings() map[string]map[string]metrics.Instant
	// Interval is the scrape cadence, for the pages to state.
	Interval() time.Duration
	// Retention is the rollup window, for the pages to state.
	Retention() time.Duration
}

// LogTailer performs one bounded, on-demand log fetch for a verified
// member pod. It is one of the closed request-time API exceptions.
type LogTailer interface {
	// TailPoolerLogs performs the same bounded fetch for a pod proven to
	// belong to one of the cluster's connection poolers.
	TailPoolerLogs(ctx context.Context, pod string) (observe.LogTail, error)
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
	mux.HandleFunc("GET /objects", h.handleObjects)
	mux.HandleFunc("GET /cluster/overview", h.handleClusterOverview)
	mux.HandleFunc("GET /cluster/pods", h.handleClusterPods)
	mux.HandleFunc("GET /cluster/pods/{pod}", h.handlePodDetail)
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
	// The timeline is manifest-free metadata and stays at the baseline;
	// the revision detail is the object's definition verbatim minus the
	// scrub — more revealing than any other screen — so it sits behind
	// the same gate as the log tail, and the timeline hides the links
	// below that level.
	if h.sources.Metrics != nil {
		mux.HandleFunc("GET /cluster/metrics", h.handleClusterMetrics)
		mux.HandleFunc("GET /cluster/metrics/series", h.handleMetricsSeries)
	}
	if h.sources.History != nil {
		mux.HandleFunc("GET /history", h.handleHistory)
		mux.HandleFunc("GET /history/revisions/{seq}", h.requireLevel(authz.TierPowerUser,
			"revision details require the poweruser or dba level", h.handleHistoryRevision))
	}
	mux.HandleFunc("GET /healthz", h.handleHealthz)
	mux.HandleFunc("GET /readyz", h.handleReadyz)
	// The instance log tail sits above the baseline: it requires the
	// poweruser level or above, and the affordance is hidden below it.
	if h.cfg.AllowLogs {
		mux.HandleFunc("GET /logs/{pod}", h.requireLevel(authz.TierPowerUser, "log access requires the poweruser or dba level", h.handleLogs))
		mux.HandleFunc("GET /poolers/logs/{pod}", h.requireLevel(authz.TierPowerUser, "log access requires the poweruser or dba level", h.handlePoolerLogs))
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
		// calling new Function(). Browser-originated requests are confined
		// to this application: htmx uses them for enhanced GET navigation
		// and refresh, while selfRequestsOnly independently enforces the
		// same boundary.
		header.Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
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
	access := h.requestAccess(r)
	logsVisible := h.cfg.AllowLogs && access.hasIdentity && access.level >= authz.TierPowerUser
	s := snapshots{window: h.cfg.EventsWindow, allowLogs: logsVisible}
	s.cluster, s.ok = h.sources.Cluster.Current()
	s.pods, s.podsOK = h.sources.Pods.CurrentPods()
	s.events, s.eventsOK = h.sources.Events.CurrentEvents()
	s.backups, s.backupsOK = h.sources.Backups.CurrentBackups()
	s.poolers, s.poolersOK = h.sources.Poolers.CurrentPoolers()
	s.poolerPods, s.poolerPodsOK = h.sources.PoolerPods.CurrentPoolerPods()
	s.quorum, s.quorumOK = h.sources.FailoverQuorum.CurrentFailoverQuorum()
	s.catalogs, s.catalogsOK = h.sources.ImageCatalogs.CurrentImageCatalogs()
	s.declared, s.declaredOK = h.sources.DatabaseObjects.CurrentDatabaseObjects()
	if h.sources.Infrastructure != nil {
		s.infra, s.infraOK = h.sources.Infrastructure.CurrentInfrastructure()
	}
	if h.sources.Evidence != nil {
		s.evidence = h.sources.Evidence.CurrentEvidence()
		s.evidenceEnabled = true
	}
	page := buildPage(r.Context(), h.cfg.ClusterName, h.cfg.Namespace, s, h.now(), h.cfg.Links)
	page.Identity = h.buildIdentityView(r)
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

// handleObjects renders the inventory: every observed object grouped by
// the resource it belongs to, each kind carrying its own freshness.
//
// The raw-definition affordance is attached here rather than in the view
// builder because it depends on the request's level, and the builder
// takes no request. It is the same gate the revision routes enforce, so
// the link never appears where following it would refuse.
func (h *Handler) handleObjects(w http.ResponseWriter, r *http.Request) {
	page := h.assemble(r, "objects")
	if access := h.requestAccess(r); access.hasIdentity && access.level >= authz.TierPowerUser {
		h.attachRetainedRevisions(page.Objects)
	}
	h.renderPage(w, "objects", "objects.html.tmpl", page)
}

// attachRetainedRevisions resolves each listed object against the
// history journal, so a row can offer the definition the console already
// holds. It never reads the API server: the journal is fed by the
// watches the console already runs, and its manifests were scrubbed at
// that capture boundary. A kind the journal does not record — every
// supporting object, by design — resolves to nothing and offers no link.
func (h *Handler) attachRetainedRevisions(objects *ObjectsView) {
	if objects == nil || h.sources.History == nil {
		return
	}
	snap, ok := h.sources.History.Snapshot()
	if !ok {
		return
	}
	// Newest first, so the first match for a name is the current
	// definition and later revisions of the same object are skipped.
	type key struct{ kind, name string }
	seq := map[key]uint64{}
	for _, entry := range snap.Entries {
		if !entry.HasManifest {
			continue
		}
		k := key{entry.Kind, entry.Name}
		if _, seen := seq[k]; !seen {
			seq[k] = entry.Seq
		}
	}
	for gi := range objects.Groups {
		for ki := range objects.Groups[gi].Kinds {
			kind := &objects.Groups[gi].Kinds[ki]
			if kind.APIKind == "" {
				continue
			}
			for ri := range kind.Rows {
				kind.Rows[ri].RawSeq = seq[key{kind.APIKind, kind.Rows[ri].Name}]
			}
		}
	}
}

// handleClusterOverview renders the power-user wiring: the observed
// shape with placement and replication facts, and the configured backup
// path.

// handleClusterOverview renders the power-user wiring: the observed
// shape with placement and replication facts, and the configured backup
// path.
func (h *Handler) handleClusterOverview(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, "cluster-overview", "cluster-overview.html.tmpl", h.assemble(r, "cluster-overview"))
}

// handleClusterPods renders the Kubernetes-observed instance pods and
// the merged recent per-pod timeline.
func (h *Handler) handleClusterPods(w http.ResponseWriter, r *http.Request) {
	page := h.assemble(r, "cluster-pods")
	page.PodHistory, _ = h.buildPodTimeline("", recentPodHistoryBound, h.now())
	h.renderPage(w, "cluster-pods", "cluster-pods.html.tmpl", page)
}

// recentPodHistoryBound caps the roster screen's timeline; the per-pod
// detail carries the rest.
const recentPodHistoryBound = 8

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
	access := h.requestAccess(r)
	return ShellView{
		ClusterName:           h.cfg.ClusterName,
		Namespace:             h.cfg.Namespace,
		CurrentURL:            r.URL.RequestURI(),
		Identity:              h.buildIdentityView(r),
		Links:                 buildLinks(h.cfg.Links),
		OperationsAvailable:   h.cfg.AllowOperations && h.executor != nil,
		CanOperate:            access.canOperate(h),
		AccessReviewAvailable: h.cfg.AllowAccessReview && h.reviewer != nil && h.sources.AccessReview != nil,
		CanReviewAccess:       access.canReviewAccess(h),
		HistoryAvailable:      h.sources.History != nil,
		MetricsAvailable:      h.sources.Metrics != nil,
		Current:               current,
	}
}

// requestAccess is the request-scoped input to UI capability rendering.
// It deliberately carries only the proxy assertions the route gates already
// consume: rendering never probes Kubernetes RBAC and never widens the
// ServiceAccount's authority.
type requestAccess struct {
	level       authz.Tier
	hasIdentity bool
}

// requestAccess resolves the forwarded identity and asserted level once for
// capability decisions. A level without a usable identity exposes no gated
// affordance because the corresponding route would refuse it for lack of an
// auditable actor.
func (h *Handler) requestAccess(r *http.Request) requestAccess {
	access := requestAccess{level: h.level(r)}
	if h.auth.Extractor != nil {
		_, access.hasIdentity = h.auth.Extractor.FromRequest(r)
	}
	return access
}

func (a requestAccess) canOperate(h *Handler) bool {
	return a.hasIdentity && a.level >= authz.TierPowerUser && h.cfg.AllowOperations && h.executor != nil
}

func (a requestAccess) canReviewAccess(h *Handler) bool {
	return a.hasIdentity && a.level >= authz.TierDBA && h.cfg.AllowAccessReview && h.reviewer != nil && h.sources.AccessReview != nil
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

// serveRawTail writes the bounded tail as plain text. It sits behind
// the same requireLevel gate as the page form of the route.
func (h *Handler) serveRawTail(w http.ResponseWriter, r *http.Request, pod string) {
	if h.tailer == nil {
		http.Error(w, "no Kubernetes access", http.StatusServiceUnavailable)
		return
	}
	tail, err := h.tailer.TailLogs(r.Context(), pod)
	if err != nil {
		if redact.Categorize(err) == redact.CategoryNotFound {
			http.NotFound(w, r)
			return
		}
		http.Error(w, redact.Safe(err), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(tail.Content))
}

// handleLogs serves one bounded, on-demand log tail for a verified
// member pod. This is a closed request-time API exception: the fetch
// runs on the request's context, so a client disconnect cancels it;
// nothing is cached or persisted. A non-member pod is indistinguishable
// from a nonexistent one. With ?raw=1 the same gated tail is served as
// plain text, which is what the pod detail's follow enhancement polls.
func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request) {
	pod := r.PathValue("pod")
	if !podNamePattern.MatchString(pod) {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("raw") == "1" {
		h.serveRawTail(w, r, pod)
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

// handlePoolerLogs serves one bounded, on-demand tail of a pooler pod's
// pgbouncer container, on the same terms as the instance tail: the same
// level gate, the same bounds, and membership re-verified live before
// the fetch.
func (h *Handler) handlePoolerLogs(w http.ResponseWriter, r *http.Request) {
	pod := r.PathValue("pod")
	if !podNamePattern.MatchString(pod) {
		http.NotFound(w, r)
		return
	}
	view := LogsView{
		Shell:       h.shell(r, "poolers-logs"),
		ClusterName: h.cfg.ClusterName,
		Pod:         pod,
		Origin:      OriginKubernetes,
	}
	status := http.StatusOK
	if h.tailer == nil {
		status = http.StatusServiceUnavailable
		view.State = "unavailable: no Kubernetes access"
	} else {
		tail, err := h.tailer.TailPoolerLogs(r.Context(), pod)
		switch {
		case err == nil:
			view.Bounds = fmt.Sprintf("last %d lines, at most %d bytes", tail.LineLimit, tail.ByteLimit)
			view.Content = tail.Content
			view.Truncated = tail.TruncatedByBytes
		case redact.Categorize(err) == redact.CategoryNotFound:
			status = http.StatusNotFound
			view.State = "no such pooler pod"
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

// CurrentPoolerPods reports no pooler pod snapshot.
func (EmptySnapshots) CurrentPoolerPods() (observe.PodsSnapshot, bool) {
	return observe.PodsSnapshot{}, false
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

// CurrentInfrastructure reports no infrastructure snapshot.
func (EmptySnapshots) CurrentInfrastructure() (observe.InfrastructureSnapshot, bool) {
	return observe.InfrastructureSnapshot{}, false
}
