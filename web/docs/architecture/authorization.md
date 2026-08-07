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
unrecognized value is `none`, and `none` reaches nothing.

| Level | Reaches |
|---|---|
| *none* | nothing but the denial page, `/healthz`, `/readyz` and the embedded assets |
| `view` | the overviews — global, cluster, backups, databases and poolers — and the two metrics screens |
| `poweruser` | additionally every other read screen: object inventories, the pod rosters and their detail, object history, repository evidence, and the bounded log tails |
| `dba` | additionally the four day-2 operations when `ALLOW_OPERATIONS=true`, the access-request review panel when `ALLOW_ACCESS_REVIEW=true`, and the pgAdmin link-out |

**There is no ungated baseline.** Reaching the console is not
authorization. The proxy authenticates; the level it forwards decides
which screens the request may reach, and a request carrying none is
refused with a page that states the ladder so the reader knows what to
ask for.

pgAdmin is the one link-out the level decides. It is a SQL console onto
the database, so it reaches past everything this console will show anyone
below `dba`; offering the door to a reader who may not open it would be
the console advertising a way round its own ladder.

Setting `TRUSTED_LEVEL_HEADER` empty does **not** open the console — it
closes it. With no level to read, nothing is admitted, and the denial
page names the deployment rather than the reader as the reason.

The server derives navigation affordances from the same identity and level
inputs as the route gates. A level without a usable forwarded identity does
not render a gated link because the route would refuse the unauditable actor.
This remains presentation only: direct requests always pass through the
route gate.

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
