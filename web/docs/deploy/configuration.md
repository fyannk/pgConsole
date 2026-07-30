---
sidebar_position: 2
title: Configuration
---

# Configuration

pgConsole is configured entirely through environment variables. The whole
contract is validated **totally** before the listener opens: an invalid
value fails startup with an error naming the variable and the constraint,
and never echoing the supplied value. Only `CLUSTER_NAME` and `NAMESPACE`
are required.

This page explains the groups; the exhaustive table with defaults and
bounds is the [configuration reference](../reference/configuration.md).

## Target and listener

`CLUSTER_NAME` and `NAMESPACE` (both DNS-1123 labels) name the one cluster
the console observes; `NAMESPACE` normally comes from the downward API.
`LISTEN_ADDR` is the plain HTTP listen address (default `:3000`) — TLS is
the proxy's job.

## Trusted headers

`TRUSTED_USER_HEADER` (default `X-Forwarded-User`) is the identity header,
used for display and audit attribution only. `TRUSTED_LEVEL_HEADER`
(default `X-PgToolBox-Level`) carries the authorization level `view`,
`poweruser`, or `dba`. Setting either to the empty string disables it:
with no user header there is no identity and every level-gated route is
denied; with no level header only the read-only baseline is reachable.

## Capability switches

- `ALLOW_OPERATIONS` (default `false`) — the enumerated day-2 operations.
- `ALLOW_ACCESS_REVIEW` (default `false`) — the dba review panel.
- `ALLOW_LOGS` (default `true`) — the master switch for the log tail; when
  on, the tail still requires the `poweruser` level.

Each is a strict boolean (`"true"` or `"false"`); anything else fails
startup. A disabled capability registers **no route** and constructs no
writer — it is provably absent, not merely refused.

## Bounds

`LOG_TAIL_LINES`, `LOG_TAIL_MAX_BYTES`, `EVENTS_MAX_AGE`, and
`API_REQUEST_TIMEOUT` bound the log tail, the event window, and each
Kubernetes API request. Every value has a validated minimum and maximum;
see the reference.

## Link-outs

`OBJECTSTOREVIEWER_URL`, `PGADMIN_URL`, and `MONITORING_URL` add sibling
link-outs; empty hides each. They must be `https` URLs carrying no user
information, unless `ALLOW_INSECURE_LINKS=true` (lab use only) permits
`http`.

## Repository evidence

The four `REPOSITORY_*` variables configure the optional evidence
consumer and validate **all-or-nothing**: set any one and all four are
required, or the process refuses to start; set none and the consumer is
disabled — no panel, no poller, no socket. The URL must be a `unix://`
socket URI or an absolute socket path; loopback and TCP forms refuse to
start, because the evidence API exists only on a pod-private Unix socket.
See [Repository evidence](../architecture/repository-evidence.md).
