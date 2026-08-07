---
sidebar_position: 2
title: Day-2 operations
---

# Day-2 operations

When enabled, pgConsole exposes a **closed** set of four day-2 operations.
Each maps to one exact, pinned CloudNativePG interaction — there is no
generic "apply YAML", no `Cluster` spec editing, and no free-form patch
path.

| Operation | What it does | Interaction |
|---|---|---|
| Request backup | Creates one on-demand `Backup` for the cluster | `create backups` |
| Reload | Triggers a configuration reload | annotation `patch` on the cluster |
| Restart | Triggers a rolling restart | annotation `patch` on the cluster |
| Promote | Requests a switchover to a chosen instance | `patch` on `clusters/status` |

The interactions reproduce the `kubectl cnpg` plugin's behavior for the
pinned CloudNativePG version, byte-for-byte.

## Enabling

Set `ALLOW_OPERATIONS=true` and apply `deploy/operations-role.yaml`. With
the flag off there is no route; with the Role absent, RBAC denies the write
even if the flag is set. The routes require the `dba` level; a
`poweruser` is refused.

## The flow

Operations are **fire-and-observe**, never orchestrated:

1. A `dba` opens `/operations` and picks an operation.
2. The confirmation page (`GET`) mints a fresh CSRF token bound to the
   operation and target — this GET has no side effect.
3. Submitting (`POST`) checks same-origin provenance and the CSRF token,
   then issues the one pinned interaction.
4. The result page reports that the request was accepted. Progress appears
   on the **status console** as the operator reports it — the console does
   not poll or orchestrate.

Every execution writes one structured **audit** line: the operation, the
target, the outcome category, and the actor identity labeled
proxy-asserted. The identity is recorded for audit, never used to
authorize — the level gate already did that.

## What it never does

- No `delete` and no spec-replace: single-instance replica restart (a pod
  deletion in the plugin) is deliberately not offered — it would need a
  `delete` verb and its own risk review.
- No Secret access and no SQL. The console requests the operation; the
  operator carries it out under its own controllers.
