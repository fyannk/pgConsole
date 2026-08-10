---
sidebar_position: 2
title: RBAC
---

# RBAC

The deployed Roles on the console's ServiceAccount are its **entire**
authority. There are four — one always, three opt-in — and none grants
anything on `secrets`. Three are namespaced; the fourth, the opt-in
cluster catalog reader, is the single cluster-scoped grant in the
project and is described below.

## Read-only Role (always)

The default authority. Namespace-scoped, no mutating verb.

| Purpose | API group | Resources | Verbs |
|---|---|---|---|
| Cluster status | `postgresql.cnpg.io` | `clusters` | `get` (pinned by `resourceNames`) |
| Cluster watch | `postgresql.cnpg.io` | `clusters` | `watch` (consumed via a `metadata.name` field selector) |
| Backups | `postgresql.cnpg.io` | `backups`, `scheduledbackups` | `get`, `list`, `watch` |
| Poolers | `postgresql.cnpg.io` | `poolers` | `get`, `list`, `watch` |
| Declared database objects | `postgresql.cnpg.io` | `databases`, `databaseroles`, `publications`, `subscriptions` | `get`, `list`, `watch` |
| Failover quorum | `postgresql.cnpg.io` | `failoverquorums` | `get` (pinned by `resourceNames`) and `watch` |
| Image catalogs (namespaced) | `postgresql.cnpg.io` | `imagecatalogs` | `get`, `list`, `watch` |
| Instance pods | `""` | `pods` | `get`, `list`, `watch` |
| Services | `""` | `services` | `get`, `list`, `watch` |
| Volume claims | `""` | `persistentvolumeclaims` | `get`, `list`, `watch` |
| Log tail | `""` | `pods/log` | `get` (omit when `ALLOW_LOGS=false`) |
| Events | `""` | `events` | `list`, `watch` |
| Workload owners | `apps` | `replicasets`, `deployments` | `get` |
| Configuration and identity | `""` | `configmaps`, `serviceaccounts` | `get`, `list`, `watch` |
| Disruption budgets | `policy` | `poddisruptionbudgets` | `get`, `list`, `watch` |
| RBAC inventory | `rbac.authorization.k8s.io` | `roles`, `rolebindings` | `get`, `list`, `watch` |
| Jobs | `batch` | `jobs` | `get`, `list`, `watch` |
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

Reads on one pgToolBox CRD plus one status write.

| Purpose | API group | Resources | Verbs |
|---|---|---|---|
| List and follow access requests | `pgtoolbox.fyannk.dev` | `pgtoolboxaccessrequests` | `get`, `list`, `watch` |
| Record a decision | `pgtoolbox.fyannk.dev` | `pgtoolboxaccessrequests/status` | `patch` |

The approval picker needs no grant: the grantable levels are the closed
set `view`/`poweruser`/`dba`, hardcoded on both sides of the operator's
contract, so there is nothing to list.

The console never creates or modifies users or proxy configuration; the
operator's controller materializes the `PgToolBoxUser` after an
approval.

## Cluster-catalog ClusterRole (`ALLOW_CLUSTER_CATALOGS=true`)

The one cluster-scoped grant in the project, shipped as a `ClusterRole`
and `ClusterRoleBinding` in `deploy/cluster-catalog-role.yaml`.

| Purpose | API group | Resources | Verbs |
|---|---|---|---|
| Read the referenced cluster-wide catalog | `postgresql.cnpg.io` | `clusterimagecatalogs` | `get` |

`get` and nothing else. RBAC cannot pin the name in advance, so the rule
is unpinned — but without `list` or `watch` the console can read a
catalog only when the `Cluster` it observes names one. Declining this
costs a single panel, which reads `unknown` rather than failing.

Beyond the resources above, the console reads one non-resource URL: the
API server's `/version` report, polled every five minutes so
version-pinned diagnostics can gate on the observed server version.
`/version` is readable by every authenticated principal; no Role grants
it and none could withhold it.

## Enforcement

The Role — not application logic — is the enforcement boundary. Apply an
opt-in Role only when you set its flag: with the flag off, RBAC alone denies
the writes; with the Role absent, the capability cannot act even if the flag
is set.

A static scan (`hack/check-readonly.sh`) holds the examples to their
boundaries rather than to the exact tables above: no mutating or
privilege verb in the read-only Role, no forbidden verb in the opt-in
ones, the operations `clusters` write pinned by `resourceNames`, no
cluster-scoped grant outside the catalog manifest, and no reference to
`secrets` anywhere.
