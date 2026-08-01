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
| `TRUSTED_LEVEL_HEADER` | `X-PgToolBox-Level` | Proxy level header carrying `view`, `poweruser`, or `dba`; empty leaves only the read-only baseline. |
| `ALLOW_OPERATIONS` | `false` | Enables the enumerated day-2 operation routes. Strict boolean. |
| `ALLOW_ACCESS_REVIEW` | `false` | Enables the dba access-request review panel. Strict boolean; needs its own Role. |
| `ALLOW_CLUSTER_CATALOGS` | `false` | Lets the console read the one cluster-scoped `ClusterImageCatalog` its Cluster references. Strict boolean; needs its own ClusterRole. The only setting that grants authority outside the namespace. |
| `ALLOW_LOGS` | `true` | Master switch for the bounded log tail; when on, the tail still requires the `poweruser` level. |
| `LOG_TAIL_LINES` | `200` | Lines per log request, 1–2000. |
| `LOG_TAIL_MAX_BYTES` | `1048576` | Bytes per log request, 4 KiB–8 MiB. |
| `EVENTS_MAX_AGE` | `1h` | Event age window, 1m–24h. |
| `API_REQUEST_TIMEOUT` | `10s` | Per-request Kubernetes API bound, 1s–1m. |
| `OBJECTSTOREVIEWER_URL` | *empty* | ObjectStoreViewer link-out; empty hides it. |
| `PGADMIN_URL` | *empty* | pgAdmin link-out; empty hides it. |
| `MONITORING_URL` | *empty* | Monitoring link-out; empty hides it. |
| `ALLOW_INSECURE_LINKS` | `false` | Permits `http://` link-outs (lab use only). |
| `REPOSITORY_EVIDENCE_URL` | *empty* | Evidence sidecar socket — `unix://` URI or absolute path; loopback and TCP refuse to start. |
| `REPOSITORY_EVIDENCE_TOKEN_FILE` | *empty* | Absolute path to the operator-mounted pod-local bearer token. |
| `REPOSITORY_EXPECTED_FINGERPRINT` | *empty* | Expected `sha256:` destination fingerprint responses must carry. |
| `REPOSITORY_BARMAN_SERVER` | *empty* | Exact Barman server name of the operator-supplied identity mapping. |

## Notes

- **Strict booleans** accept only the literals `"true"` and `"false"`; any
  other value fails startup.
- **Link-outs** must be `https` and carry no user information, unless
  `ALLOW_INSECURE_LINKS=true` permits `http`.
- The four **`REPOSITORY_*`** variables validate all-or-nothing: set any and
  all are required; set none and the evidence consumer is disabled entirely.
- Setting `TRUSTED_USER_HEADER` or `TRUSTED_LEVEL_HEADER` to an explicit
  empty string is a valid, fail-safe configuration — it removes a capability
  rather than loosening one.
