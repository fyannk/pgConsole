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
denied; with no level header the console can admit nobody and every
screen answers 503. Neither setting leaves a reduced console behind —
they close it.

## Capability switches

- `ALLOW_OPERATIONS` (default `false`) — the enumerated day-2 operations.
- `ALLOW_ACCESS_REVIEW` (default `false`) — the dba review panel.
- `ALLOW_CLUSTER_CATALOGS` (default `false`) — the cluster-scoped catalog
  read. This is the only switch that grants authority outside the
  console's namespace, so it needs its own ClusterRole
  (`deploy/cluster-catalog-role.yaml`) as well as the flag.
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

## Object-definition history

`HISTORY_ENABLED` defaults to `true`. Capture taps the lists and watches the
console already owns, so it adds no Kubernetes connection or RBAC verb. The
retained revision count, manifest bytes, per-object count, and status
coalescing window are independently bounded by the `HISTORY_*` settings.

History is in memory by default and restarts empty. `HISTORY_PATH` opts into a
bbolt journal on an explicitly mounted writable PVC. That deployment must use
one replica; an unusable or locked journal fails before listen. The commented
PVC example in `deploy/kubernetes-example.yaml` shows the required mount.

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

## The cluster-scoped catalog read

A CloudNativePG `Cluster` may draw its image from a `ClusterImageCatalog`
rather than naming the image directly. That resource is cluster-scoped,
so reading it needs authority the console's namespaced Role does not
have.

Leaving this off is a supported configuration, not a degraded one. The
console still shows the *reference*, because `spec.imageCatalogRef` is a
field on the Cluster, which is namespaced. What it will not do is claim
anything about the catalog's content — and, importantly, it will not
report the catalog as missing. "I was not permitted to look" and "it is
not there" are different statements, and the panel makes which one
applies explicit.

To turn it on, apply `deploy/cluster-catalog-role.yaml` and set
`ALLOW_CLUSTER_CATALOGS=true`. The grant is a `get` on
`clusterimagecatalogs` and nothing else: no `list` and no `watch`, so the
console can read the one catalog its Cluster names but cannot enumerate
the catalogs in the cluster. With the flag set but the ClusterRole
unbound, the read is refused and the panel says the content could not be
read.
