---
sidebar_position: 3
title: HTTP routes
---

# HTTP routes

Every route pgConsole serves. Each carries the security headers
(`Cache-Control: no-store`, a strict CSP, `X-Content-Type-Options`,
`X-Frame-Options: DENY`, and a referrer policy). State-changing routes are
POST-only, CSRF-guarded, and same-origin-checked. A route for a disabled
capability is **not registered** — it returns 404, never a route that
refuses.

Every screen carries a level. There is no ungated baseline: a request
with no usable forwarded level reaches the denial page, the readiness
endpoints and the static assets, and nothing else.

| Method | Path | Level | Exists when | Purpose |
|---|---|---|---|---|
| GET | `/` | `view` | always | Plain-language overview assembled from the attributed screens. |
| GET | `/objects` | `poweruser` | always | Inventory of every observed object, grouped by the resource it belongs to, each kind carrying its own watch's freshness. |
| GET | `/cluster/pods` | `poweruser` | always | Membership-verified instance pods observed by Kubernetes. |
| GET | `/backups` | `view` | always | Backup overview and operator/repository cross-check. |
| GET | `/backups/objects` | `poweruser` | always | `Backup`, `ScheduledBackup`, and ObjectStore reference catalog. |
| GET | `/backups/evidence` | `poweruser` | always | Optional repository evidence, or its explicit unavailable/disabled state. |
| GET | `/databases` | `view` | always | Declared Database objects and operator status. |
| GET | `/databases/roles` | `poweruser` | always | Declared DatabaseRole objects. |
| GET | `/databases/publications` | `poweruser` | always | Declared Publication objects. |
| GET | `/databases/subscriptions` | `poweruser` | always | Declared Subscription objects. |
| GET | `/poolers` | `view` | always | Pooler overview. |
| GET | `/poolers/pods` | `poweruser` | always | Membership-verified Pooler pods. |
| GET | `/history` | `poweruser` | `HISTORY_ENABLED=true` | Bounded object-definition revision timeline. Reads the existing watch recorder's store; no API call. |
| GET | `/history/revisions/{seq}` | `poweruser` | `HISTORY_ENABLED=true` | One scrubbed, byte-bounded manifest and its bounded on-demand structural diff. |
| GET | `/cluster/metrics` | `view` | `METRICS_ENABLED=true` | The instance exporter's bounded window: a tab per instance, charts and status tiles. |
| GET | `/cluster/metrics/series` | `view` | `METRICS_ENABLED=true` | One catalog series as JSON, for the charts. |
| GET | `/poolers/metrics` | `view` | `METRICS_ENABLED=true` | The same screen over the PgBouncer exporter's window. |
| GET | `/poolers/metrics/series` | `view` | `METRICS_ENABLED=true` | One pooler series as JSON. |
| GET | `/cluster/pods/{pod}` | `poweruser` | always | One instance pod: status, merged timeline, and the log tail above its gate. |
| GET | `/poolers/pods/{pod}` | `poweruser` | always | The same screen for a pod one of this cluster's poolers owns. |
| GET | `/healthz` | none | always | Liveness — a constant `ok`. Proves the process is alive, nothing more. |
| GET | `/readyz` | none | always | Readiness — reaches the API (and a required sidecar). A constant body; probe detail is logged as a category only. |
| GET | `/logs/{pod}` | `poweruser` | `ALLOW_LOGS=true` | One bounded, on-demand log tail for a membership-verified pod. A non-member pod is indistinguishable from a nonexistent one. |
| GET | `/poolers/logs/{pod}` | `poweruser` | `ALLOW_LOGS=true` | One bounded, on-demand tail for a live-verified Pooler member pod. |
| GET | `/operations` | `dba` | `ALLOW_OPERATIONS=true` | The closed catalog of day-2 operations. |
| GET | `/operations/{op}` | `dba` | `ALLOW_OPERATIONS=true` | The confirmation form with a fresh CSRF token. No side effect. |
| POST | `/operations/{op}` | `dba` | `ALLOW_OPERATIONS=true` | Executes one enumerated operation. CSRF + same-origin required. |
| GET | `/access-requests` | `dba` | `ALLOW_ACCESS_REVIEW=true` | The review panel: pending requests with forms, decided requests read-only. |
| POST | `/access-requests/{name}/approve` | `dba` | `ALLOW_ACCESS_REVIEW=true` | Records an approval with a chosen role. CSRF + same-origin required. |
| POST | `/access-requests/{name}/deny` | `dba` | `ALLOW_ACCESS_REVIEW=true` | Records a denial. CSRF + same-origin required. |
| GET | `/static/…` | none | always | Stylesheet and static assets. No cluster state. |

The read screens make no Kubernetes
API call at render time except the closed exceptions: the log tail,
readiness, the operation execution, and the access-review decision write.
Health, readiness, and static assets are never level-gated — probes and
stylesheets carry no cluster state.

With JavaScript enabled, same-origin GET navigation and refresh select and
replace one server-rendered application root using the vendored htmx asset.
The routes and responses are unchanged: without JavaScript the same links
perform ordinary full-document navigation. htmx evaluation, response script
execution, and browser history caching are disabled; sensitive rendered HTML
is never placed in local storage.
