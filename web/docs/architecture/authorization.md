---
sidebar_position: 2
title: Authorization
---

# Authorization

pgConsole decides **no** authorization of its own. The trusted proxy
authenticates the user and asserts a coarse level; the console reads that
level and gates which routes the user reaches. There is no
`SubjectAccessReview`, no capability probing, no cluster-scoped grant, and
no user token inside the console.

> The trusted proxy authenticates you and asserts your level; the console
> shows the routes that level admits, and the ServiceAccount's Role caps
> what can physically happen.

## The two headers

- `X-Forwarded-User` (`TRUSTED_USER_HEADER`) — the verified identity, used
  for display and audit attribution only.
- `X-PgToolBox-Level` (`TRUSTED_LEVEL_HEADER`) — the authorization level,
  one of `view`, `poweruser`, or `dba`.

These are trustworthy **only** because the operator's NetworkPolicy
confines the console's ingress to the proxy. No other component may reach
the console directly. The console never receives a user token and never
speaks to the identity provider.

## The level ladder

The level is parsed once per request against a closed set and mapped onto
an ordered ladder. Parsing is total: a missing, empty, malformed, or
unrecognized value is `none`, so nothing above the read-only baseline is
reached by an unrecognized value.

| Level | Reaches |
|---|---|
| *baseline* (any authenticated request) | the read-only status console: status, conditions, pods, events, backups |
| `view` | the read-only baseline, explicitly |
| `poweruser` | additionally the bounded log tail, and the day-2 operations when `ALLOW_OPERATIONS=true` |
| `dba` | additionally the access-request review panel |

The read-only status baseline is **ungated** — reaching the console means
the proxy already authenticated the request, so status renders for everyone
admitted. Only routes *above* the baseline — the log tail, operations, and
the review panel — require an explicit level.

## What it costs, stated plainly

A spoofed `X-PgToolBox-Level` is privilege escalation, not a cosmetic bug.
The console's defense is not to second-guess the value but to make the
channel that carries it unspoofable:

- The level may be trusted only where the operator confines ingress to the
  proxy — the NetworkPolicy is the documented invariant.
- The console logs the forwarded identity and level as **proxy-asserted**,
  never as "the user's RBAC". The level authorizes admission to a button,
  never the action itself.

Every mutation still executes under the console's ServiceAccount; the API
server's audit log shows the SA, not the user. Because the operations Role
grants only the enumerated writes, the console mints no authority beyond
those exact verbs for anyone the proxy elevates. The structured audit line
— recording the forwarded identity labeled proxy-asserted — is the
user-level record.

## What the Role can and cannot pin

Kubernetes RBAC bounds *which* resource a verb touches, never *how*. The
console is honest about two consequences:

- `patch` on `clusters` is `patch` on `clusters`. The guarantee that a
  reload or restart is a typed, minimal, annotation-level patch — never a
  spec rewrite — is application logic backed by the audit line, not
  something RBAC enforces. Promote goes further and patches
  `clusters/status`: authority over the very status the console renders.
  The enumerated-operation design and the per-operation audit are what keep
  that authority narrow.
- `list`/`watch` cannot be pinned by name, so pod membership is decided by
  the cluster's selector labels (for example `cnpg.io/cluster`) plus
  controller-ownership verification. Those labels are a **selection**
  mechanism, never a security boundary — the namespaced Role is the
  boundary. Before every log tail the console re-verifies the target pod's
  membership live, so a non-member pod is indistinguishable from one that
  does not exist.

## Running without pgToolBox

The console needs no code path of its own for a non-pgToolBox deployment:
any front end that authenticates the user and sets `X-Forwarded-User` and
`X-PgToolBox-Level`, under the same ingress-confinement invariant, drives
the same model. Audiences that need different *Role-level* authority (not
just different buttons) keep two deployments with two Roles — the level
splits what one deployment shows, never the ServiceAccount's authority.
