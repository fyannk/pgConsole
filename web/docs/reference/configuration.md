---
sidebar_position: 1
title: Configuration variables
---

# Configuration variables

Every value pgConsole accepts, with defaults and bounds. The contract is
validated totally before the listener opens; an invalid value fails startup
naming the variable and the constraint, never the value. `CLUSTER_NAME` and
`NAMESPACE` are required DNS-1123 labels; everything else defaults.

| Variable | Default | Meaning |
|---|---|---|
| `CLUSTER_NAME` | *required* | The one target CloudNativePG `Cluster`. |
| `NAMESPACE` | *required* | Its namespace, normally via the downward API. |
| `LISTEN_ADDR` | `:3000` | Plain HTTP listen address (TLS is the proxy's job). |
| `TRUSTED_USER_HEADER` | `X-Forwarded-User` | Proxy identity header — display and audit only; empty disables identity and denies every level-gated route. |
| `TRUSTED_LEVEL_HEADER` | `X-PgToolBox-Level` | Proxy level header carrying `view`, `poweruser`, or `dba`; empty closes the console entirely — every screen answers 503, because a deployment that forwards no level can admit nobody. |
| `ALLOW_OPERATIONS` | `false` | Enables the enumerated day-2 operation routes. Strict boolean. |
| `ALLOW_ACCESS_REVIEW` | `false` | Enables the dba access-request review panel. Strict boolean; needs its own Role. |
| `ALLOW_DIAGNOSTICS` | `false` | Enables the diagnostics screen, which correlates facts the other screens already carry into findings. Strict boolean; needs no Role, because it observes nothing of its own. |
| `ALLOW_CLUSTER_CATALOGS` | `false` | Lets the console read the one cluster-scoped `ClusterImageCatalog` its Cluster references. Strict boolean; needs its own ClusterRole. The only setting that grants authority outside the namespace. |
| `ALLOW_LOGS` | `true` | Master switch for the bounded log tail; when on, the tail still requires the `poweruser` level. |
| `LOG_STREAM_ENABLED` | `false` | Follows the member containers' logs continuously so the diagnostics matcher can analyse each line as it arrives. Retains no log text of its own — only what matched, bounded per rule per container. Requires `ALLOW_LOGS=true`. |
| `LOG_BUFFER_BYTES` | `0` | Retained log text **per container**, 0–8 MiB. `0` retains nothing, which is the default. A non-zero value is a deliberate exposure decision: it holds recent PostgreSQL log lines, which can include statements and their literal values, in the console's memory. Requires `LOG_STREAM_ENABLED=true`. |
| `LOG_BUFFER_TOTAL_BYTES` | `33554432` | Cap across every container, 0–128 MiB, so one noisy container cannot consume the whole budget. |
| `LOG_BUFFER_MAX_AGE` | `1h` | Retained lines older than this are dropped, `1m`–`24h`. |
| `LOG_TAIL_LINES` | `200` | Lines per log request, 1–2000. |
| `LOG_TAIL_MAX_BYTES` | `1048576` | Bytes per log request, 4 KiB–8 MiB. |
| `EVENTS_MAX_AGE` | `1h` | Event age window, 1m–24h. |
| `API_REQUEST_TIMEOUT` | `10s` | Per-request Kubernetes API bound, 1s–1m. |
| `HISTORY_ENABLED` | `true` | Records the bounded, scrubbed object-definition timeline from the existing watches. `false` constructs no recorder and registers no history route. |
| `HISTORY_MAX_REVISIONS` | `2000` | Global retained revision bound, 100–20000. |
| `HISTORY_MAX_BYTES` | `16777216` | Global retained manifest/attribution byte bound, 1 MiB–64 MiB. |
| `HISTORY_PER_OBJECT_REVISIONS` | `20` | Per-object retained revision bound, 2–200. |
| `HISTORY_COALESCE_WINDOW` | `1m` | Window for folding consecutive status-only transitions, 1s–1h. |
| `HISTORY_PATH` | *empty* | Absolute bbolt journal path. Empty keeps history in memory only; a value requires `HISTORY_ENABLED=true`, a writable PVC, and one replica. |
| `METRICS_ENABLED` | `true` | Sweeps the instance and pooler exporters and serves the metrics screens. Strict boolean. On by default, so the console makes direct HTTP requests to pod IPs on `9187` and `9127` unless you turn it off — the NetworkPolicy must allow that egress. |
| `METRICS_INTERVAL` | `10s` | Sweep period, 5s–5m. The exporters refresh their own caches on the order of seconds, so a faster sweep only rereads the same claims. |
| `METRICS_RETENTION` | `168h` | Retained window, `1h`–`720h`. Bounds the rollup ring the window is stored in. |
| `METRICS_PATH` | *empty* | Absolute snapshot path. Empty keeps the window in memory only; a value requires `METRICS_ENABLED=true`. An unusable path fails before listen; an unreadable snapshot merely starts the window empty. |
| `OBJECTSTOREVIEWER_URL` | *empty* | ObjectStoreViewer link-out, absolute or root-relative; empty hides it. |
| `PGADMIN_URL` | *empty* | pgAdmin link-out, absolute or root-relative (`/pgadmin`); empty hides it. |
| `MONITORING_URL` | *empty* | Monitoring link-out, absolute or root-relative; empty hides it. |
| `ALLOW_INSECURE_LINKS` | `false` | Permits `http://` link-outs (lab use only). Does not apply to root-relative paths. |
| `REPOSITORY_EVIDENCE_URL` | *empty* | Evidence sidecar socket — `unix://` URI or absolute path; loopback and TCP refuse to start. |
| `REPOSITORY_EVIDENCE_TOKEN_FILE` | *empty* | Absolute path to the operator-mounted pod-local bearer token. |
| `REPOSITORY_EXPECTED_FINGERPRINT` | *empty* | Expected `sha256:` destination fingerprint responses must carry. |
| `REPOSITORY_BARMAN_SERVER` | *empty* | Exact Barman server name of the operator-supplied identity mapping. |

## Notes

- **Strict booleans** accept only the literals `"true"` and `"false"`; any
  other value fails startup.
- **Link-outs** take either an absolute URL or a root-relative path.
  - An **absolute URL** must be `https` and carry no user information,
    unless `ALLOW_INSECURE_LINKS=true` permits `http`.
  - A **root-relative path** such as `/pgadmin` points at a sibling
    application on the console's own origin, which is how a single Route or
    Ingress usually exposes the family. It inherits the reader's scheme, so
    it cannot downgrade and `ALLOW_INSECURE_LINKS` does not apply to it.
    Prefer it behind a proxy that terminates TLS and rewrites `Host`, where
    the console cannot know its own external URL. Protocol-relative values
    (`//host/path`, and the `/\host` spelling browsers normalise to it) are
    refused: they name a different origin.
- The four **`REPOSITORY_*`** variables validate all-or-nothing: set any and
  all are required; set none and the evidence consumer is disabled entirely.
- Setting `TRUSTED_USER_HEADER` or `TRUSTED_LEVEL_HEADER` to an explicit
  empty string is a valid, fail-safe configuration — it removes a capability
  rather than loosening one.
- The history screen renders 100 manifest-free entries per page. Revision
  manifests are scrubbed before storage and capped at 256 KiB when displayed;
  structural diffs retain their 256-entry/value bounds. These UI ceilings do
  not change the configured in-memory retention budget.
