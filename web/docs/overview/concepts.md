---
sidebar_position: 2
title: Concepts
---

# Concepts

A short vocabulary that the rest of the documentation assumes.

## Observer of reported state

pgConsole renders **claims**, not verified facts. Everything it shows comes
from one of four origins, and every claim keeps its origin from the
Kubernetes adapter all the way to the rendered page:

- **operator-reported** — what CloudNativePG wrote to a resource's status.
- **Kubernetes-observed** — what the API server reports about pods, events,
  and resource existence.
- **instance-reported metrics** — what an instance's or pooler's own
  metrics endpoint claimed, recorded verbatim on the metrics screens.
  Times are when this process scraped them, not when the instance
  computed them, and a failed sweep is a gap in the line rather than a
  value interpolated across it.
- **repository-evidence** — what the optional ObjectStoreViewer sidecar
  publishes about the backup destination (never read by pgConsole itself).

A backup shown as `completed` is an operator claim. The console never says
a backup is "verified", "restorable", or "safe to restore" — that language
is deliberately absent.

## Snapshots and staleness

Each observed resource kind has a **collector** that seeds from a list,
follows a watch, and publishes an immutable **snapshot**. Rendering reads
snapshots, not the API — so API latency never becomes page latency. When a
collector loses contact it marks its snapshot **stale** and keeps the
last-good data; it never blanks the section or invents a healthy state.
Each section carries its own staleness independently.

Two honest values you will see:

- **`unknown`** — the console has no observation (a missing optional
  permission, an absent resource kind, or no snapshot yet).
- **stale** — contact was lost; the shown data is the last good
  observation, and the section says so.

## Bounds and truncation

Every collection the console renders is bounded, so a large or hostile
cluster can never turn a page into an unbounded document. When a bound is
hit the section shows an explicit **truncated** state rather than silently
dropping rows — truncation is a visible fact, never a lie of omission.

The backup catalog is the one with concrete numbers worth knowing. It
selects only resources whose `spec.cluster.name` matches the target cluster
exactly, then renders **newest-first, at most 500 `Backup` and 200
`ScheduledBackup` rows**; the traversal that seeds those lists is itself
capped at **2,000 `Backup` and 1,000 `ScheduledBackup`** candidates. Reach
either ceiling and the panel says so. Events and pods are bounded the same
way, each with its own limit and its own truncation marker.

## Levels and route admission

pgConsole authenticates nobody. A trusted proxy forwards two headers:

- `X-Forwarded-User` — the verified identity, for display and audit only.
- `X-PgToolBox-Level` — a coarse authorization level: `view`, `poweruser`,
  or `dba`.

The console maps the level onto an ordered ladder and gates route
admission — never widening its own ServiceAccount authority. There is no
ungated baseline: a missing, empty, or unrecognized level reaches no
screen at all, including the index. See
[Authorization](../architecture/authorization.md).

## The closed request-time exceptions

The rule is that handlers render from snapshots and make no API calls. The
exceptions are a closed, enumerated list:

1. the **log tail** — one bounded, on-demand fetch for a membership-verified
   pod;
2. **readiness** — a lightweight probe behind `/readyz`;
3. **day-2 operations** — one pinned write per enumerated operation;
4. the **access-request decision** — one status-subresource write.

Everything else is snapshot plus template.

## The trust boundary

pgConsole performs no authentication and terminates no TLS. Its trust
boundary is the proxy plus the operator-managed network path: a default-deny
NetworkPolicy admits only the proxy to the console's port. The forwarded
headers are trustworthy **only** because of that confinement — a directly
reachable console would let anyone assert any level. The channel, not the
console, is what makes the headers trustworthy.
