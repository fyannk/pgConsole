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

| Method | Path | Level | Exists when | Purpose |
|---|---|---|---|---|
| GET | `/` | baseline | always | Plain-language overview assembled from the attributed screens. |
| GET | `/cluster/status` | baseline | always | Operator-reported cluster status, conditions, topology, quorum, and image catalog. |
| GET | `/cluster/pods` | baseline | always | Membership-verified instance pods observed by Kubernetes. |
| GET | `/cluster/events` | baseline | always | Bounded, age-windowed Kubernetes events for cluster candidates. |
| GET | `/cluster/logs` | baseline | always | Per-pod log-tail launch points; actual tails remain level-gated. |
| GET | `/backups` | baseline | always | Backup overview and operator/repository cross-check. |
| GET | `/backups/objects` | baseline | always | `Backup`, `ScheduledBackup`, and ObjectStore reference catalog. |
| GET | `/backups/evidence` | baseline | always | Optional repository evidence, or its explicit unavailable/disabled state. |
| GET | `/databases` | baseline | always | Declared Database objects and operator status. |
| GET | `/databases/roles` | baseline | always | Declared DatabaseRole objects. |
| GET | `/databases/publications` | baseline | always | Declared Publication objects. |
| GET | `/databases/subscriptions` | baseline | always | Declared Subscription objects. |
| GET | `/poolers` | baseline | always | Pooler overview. |
| GET | `/poolers/pods` | baseline | always | Membership-verified Pooler pods. |
| GET | `/poolers/logs` | baseline | always | Pooler log-tail launch points. |
| GET | `/history` | baseline | `HISTORY_ENABLED=true` | Bounded object-definition revision timeline. Reads the existing watch recorder's store; no API call. |
| GET | `/history/revisions/{seq}` | baseline | `HISTORY_ENABLED=true` | One scrubbed, byte-bounded manifest and its bounded on-demand structural diff. |
| GET | `/healthz` | none | always | Liveness — a constant `ok`. Proves the process is alive, nothing more. |
| GET | `/readyz` | none | always | Readiness — reaches the API (and a required sidecar). A constant body; probe detail is logged as a category only. |
| GET | `/logs/{pod}` | `poweruser` | `ALLOW_LOGS=true` | One bounded, on-demand log tail for a membership-verified pod. A non-member pod is indistinguishable from a nonexistent one. |
| GET | `/poolers/logs/{pod}` | `poweruser` | `ALLOW_LOGS=true` | One bounded, on-demand tail for a live-verified Pooler member pod. |
| GET | `/operations` | `poweruser` | `ALLOW_OPERATIONS=true` | The closed catalog of day-2 operations. |
| GET | `/operations/{op}` | `poweruser` | `ALLOW_OPERATIONS=true` | The confirmation form with a fresh CSRF token. No side effect. |
| POST | `/operations/{op}` | `poweruser` | `ALLOW_OPERATIONS=true` | Executes one enumerated operation. CSRF + same-origin required. |
| GET | `/access-requests` | `dba` | `ALLOW_ACCESS_REVIEW=true` | The review panel: pending requests with forms, decided requests read-only. |
| POST | `/access-requests/{name}/approve` | `dba` | `ALLOW_ACCESS_REVIEW=true` | Records an approval with a chosen role. CSRF + same-origin required. |
| POST | `/access-requests/{name}/deny` | `dba` | `ALLOW_ACCESS_REVIEW=true` | Records a denial. CSRF + same-origin required. |
| GET | `/static/…` | none | always | Stylesheet and static assets. No cluster state. |

The read-only baseline makes no Kubernetes
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
