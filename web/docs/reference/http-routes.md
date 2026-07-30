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
| GET | `/` | baseline | always | The status console: cluster, pods, events, backups, evidence. Rendered from snapshots. |
| GET | `/healthz` | none | always | Liveness — a constant `ok`. Proves the process is alive, nothing more. |
| GET | `/readyz` | none | always | Readiness — reaches the API (and a required sidecar). A constant body; probe detail is logged as a category only. |
| GET | `/logs/{pod}` | `poweruser` | `ALLOW_LOGS=true` | One bounded, on-demand log tail for a membership-verified pod. A non-member pod is indistinguishable from a nonexistent one. |
| GET | `/operations` | `poweruser` | `ALLOW_OPERATIONS=true` | The closed catalog of day-2 operations. |
| GET | `/operations/{op}` | `poweruser` | `ALLOW_OPERATIONS=true` | The confirmation form with a fresh CSRF token. No side effect. |
| POST | `/operations/{op}` | `poweruser` | `ALLOW_OPERATIONS=true` | Executes one enumerated operation. CSRF + same-origin required. |
| GET | `/access-requests` | `dba` | `ALLOW_ACCESS_REVIEW=true` | The review panel: pending requests with forms, decided requests read-only. |
| POST | `/access-requests/{name}/approve` | `dba` | `ALLOW_ACCESS_REVIEW=true` | Records an approval with a chosen role. CSRF + same-origin required. |
| POST | `/access-requests/{name}/deny` | `dba` | `ALLOW_ACCESS_REVIEW=true` | Records a denial. CSRF + same-origin required. |
| GET | `/static/…` | none | always | Stylesheet and static assets. No cluster state. |

The read-only baseline (`/`, `/logs` at `poweruser`) makes no Kubernetes
API call at render time except the closed exceptions: the log tail,
readiness, the operation execution, and the access-review decision write.
Health, readiness, and static assets are never level-gated — probes and
stylesheets carry no cluster state.
