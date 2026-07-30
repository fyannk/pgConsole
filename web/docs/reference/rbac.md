---
sidebar_position: 2
title: RBAC
---

# RBAC

The deployed Role on the console's ServiceAccount is its **entire**
authority. There are three Roles — one always, two opt-in — and none grants
anything cluster-scoped or anything on `secrets`.

## Read-only Role (always)

The default authority. Namespace-scoped, no mutating verb.

| Purpose | API group | Resources | Verbs |
|---|---|---|---|
| Cluster status | `postgresql.cnpg.io` | `clusters` | `get` (pinned by `resourceNames`) |
| Cluster watch | `postgresql.cnpg.io` | `clusters` | `watch` (consumed via a `metadata.name` field selector) |
| Backups | `postgresql.cnpg.io` | `backups`, `scheduledbackups` | `get`, `list`, `watch` |
| Instance pods | `""` | `pods` | `get`, `list`, `watch` |
| Log tail | `""` | `pods/log` | `get` (omit when `ALLOW_LOGS=false`) |
| Events | `""` | `events` | `list`, `watch` |
| Repository reference (optional) | `barmancloud.cnpg.io` | `objectstores` | `get` |

RBAC cannot pin `list`/`watch` by `resourceNames`, so the namespace plus
application-side selection is the honest scope for listing; the cluster
`get` is pinned by name. A missing optional grant degrades one panel to
`unknown`.

## Operations Role (`ALLOW_OPERATIONS=true`)

Exactly the four enumerated writes, pinned where the verb allows.

| Operation | API group | Resources | Verbs |
|---|---|---|---|
| On-demand backup | `postgresql.cnpg.io` | `backups` | `create` |
| Reload / rolling restart | `postgresql.cnpg.io` | `clusters` | `patch` (pinned by `resourceNames`) |
| Promote (switchover) | `postgresql.cnpg.io` | `clusters/status` | `patch` (pinned by `resourceNames`) |

There is no `delete`, no spec-replace, and nothing on `secrets`.

## Access-review Role (`ALLOW_ACCESS_REVIEW=true`)

Reads on the two pgToolBox CRDs plus one status write.

| Purpose | API group | Resources | Verbs |
|---|---|---|---|
| List and follow access requests | `pgtoolbox.fyannk.dev` | `pgtoolboxaccessrequests` | `get`, `list`, `watch` |
| Record a decision | `pgtoolbox.fyannk.dev` | `pgtoolboxaccessrequests/status` | `update` |
| Populate the role picker | `pgtoolbox.fyannk.dev` | `pgtoolboxroles` | `get`, `list`, `watch` |

The console never creates or modifies users, roles, or proxy configuration;
the operator's controller materializes the `PgToolBoxUser` after an
approval.

## Enforcement

The Role — not application logic — is the enforcement boundary. Apply an
opt-in Role only when you set its flag: with the flag off, RBAC alone denies
the writes; with the Role absent, the capability cannot act even if the flag
is set. A static scan holds each example Role to exactly the shape above.
