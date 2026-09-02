---
sidebar_position: 4
title: Diagnostics
---

# Diagnostics

The diagnostics screen (`ALLOW_DIAGNOSTICS=true`, `poweruser` level)
correlates facts the other screens already carry into **findings**:
statements about what is wrong and where, each anchored to the claim it
rests on. It runs no probes and needs no extra Role — every check is a
pure function over the snapshots the console already publishes.

Three panels carry the screen:

- **Cluster state** — the operator's own account first: phase and
  reason, instances ready against declared, the current primary. A
  reader asking "what is wrong" gets "what state is it in" answered
  before any finding.
- **Findings, grouped into incidents** — what matched, most severe
  first. A finding whose declared cause also matched nests inside that
  cause's card, so a chain like *archiving failed → WAL filled the
  volume → PostgreSQL kept down* reads as one story with one root
  instead of three alarms. The relation is catalog-pinned knowledge and
  the card states its terms; every nested finding keeps its own quoted
  evidence. See [how findings relate](#how-findings-relate) for what
  the nesting rests on and what stays apart.
  Findings that carry it also show a **"What to do"** block — the
  console's guidance, labeled as guidance and rendered apart from the
  evidence, because advice is the one thing on this screen no source
  reported.
- **What was checked** — every check that ran, with its outcome. This
  is what keeps an empty screen honest: no findings means "none of
  these checks matched", never "the cluster is healthy".

## Checks and their four outcomes

A check is either a hand-written detector (correlations across
snapshots, such as quota refusals) or a **catalog rule**: a declarative
claim that, on the pinned versions, a particular observation means a
particular thing. Each check answers one of four ways:

| Outcome | Meaning |
|---|---|
| matched | The check found what it looks for; its findings are above. |
| clear | Its inputs were readable and nothing matched. |
| could not run | An input was never observed — log following off, metrics not scraped, a version not observed — or its collector has lost contact and the snapshot is stale. A check that could not run rules nothing out: a stale snapshot can neither clear a check nor match one. |
| does not apply | The rule's version pins exclude the versions actually observed. Distinct from clear on purpose: the rule ruled itself out, not the failure. |

## How findings relate

Every finding names its **subject** — the object it is about, such as
`Pod/orders-2` or `Backup/orders-20260902` — and, where its source
reports one, the **time** of the observation: an event's last
occurrence, a log line's most recent match, a backup's creation. A
condition or a phase is a current state with no instant, so findings
built on them carry no time.

A catalog relation between two checks states three terms:

| Term | Meaning |
|---|---|
| scope | *same cluster* (any two findings) or *same pod* (both must name the same pod). A cluster-level cause — an operator condition, a namespace quota — relates to effects anywhere; a pod-level cause, such as a full WAL volume read from one instance's log, explains a crash on that instance only. |
| window | The largest gap allowed between the two observations. Enforced only when both findings carry a time; otherwise the relation rests on scope alone rather than inventing an instant. |
| strength | *established mechanism* — something the upstream component documents or implements — or *plausible*, a common pattern the catalog names without a mechanism it can point to. |

A finding nests under its cause only when the relation admits the
pair. The nested card shows the terms it was admitted on. When a named
cause also matched but on another object or outside the window, the
finding stays its own card and lists the near miss beneath it with the
relation's own reason — *the catalog relates the two only on the same
pod; this one is on Pod/orders-1* — so a reader is told about the
possible connection rather than handed a nesting the evidence does not
support.

The repository checks restate the sidecar's typed state and stable
reason code for WAL continuity, recovery coverage, and retention, and
nothing else: the sidecar's diagnostic text stays in the sidecar, and no
finding says what the repository would or would not restore. A WAL
finding nests under the operator's archiving condition when both are
current, which is the console placing the repository's observation
beside the operator's claim without merging the two.

## Thresholds and holding windows

A metric check states the number it applies in its own check row, so
the threshold is never hidden in the code. Every one is console-pinned
knowledge, declared in one place per component rather than inline.

Several also require the reading to be **held**: every sample the
console retains from a trailing window must be past the threshold, and
the window must hold at least two of them. Replication lag, slot
retention and transaction age all spike in normal operation — a write
burst, a backup, a long report — and a check that reported the spike
would teach its reader to scroll past the screen. An instance whose
retained window is too short to show either way is reported as one the
check could not judge, never as a match.

## Corroborating checks

A check can require several observations at once, and then it reports
only when they are about **the same instance**. `cnpg-replica-not-receiving`
is the example: a replica in recovery is every replica, a replica with
no WAL receiver is also a replica replaying the archive on its way up,
and lag is a number that spikes. One instance showing all three is a
replica that stopped streaming and is not catching up either. Branches
matching on different instances are two facts, not one finding, and are
not joined. A branch about no single object — a cluster-wide condition
or phase — corroborates any instance, because it is a fact about all of
them.

The honesty rules compose the obvious way: a branch that could not run
makes the whole check one that could not run, never one that came back
clear.

One check compares two sources instead of reading one:
`cnpg-primary-disagreement` sets the operator's `currentPrimary`
against each instance's own `pg_is_in_recovery()`, read from the
exporter. It reports the contradiction as the finding, quoting both,
and presumes neither side right. A primary move in flight is not a
disagreement and keeps the check clear until the move settles.

## Version pinning

A claim mined from an operator's source code is a claim about specific
releases, so catalog rules carry version pins, and the pins state what
was actually verified. The current spans:

- **CloudNativePG** rules are pinned to the releases whose source the
  rule strings were read from verbatim: **1.28.4, 1.29.2 and 1.30.0**.
  Most rules span all three; machinery that first appears in 1.30 (the
  primary lease, the invalid-definition phase) is pinned 1.30-only. On
  an unverified release the rules answer "does not apply".
- **PostgreSQL** and **Kubernetes** each carry an end-of-life rule
  whose pin *is* the diagnostic, plus threshold rules (transaction-id
  wraparound) built on console-pinned knowledge that is stated as such.

The versions themselves are **observed, never configured**:

| Component | Source |
|---|---|
| CloudNativePG | The `bootstrap-controller` init container the operator injects into every instance pod runs the operator's own image; its tag is parsed. |
| PostgreSQL | The operator-reported major version in the `Cluster` status. |
| Barman Cloud plugin | The plugin sidecar's image tag. |
| Kubernetes | The API server's own `/version` endpoint, polled every five minutes — the console's only poll against the API server, a non-resource URL every authenticated principal may read. No Role change is involved. |

A version the console has not observed leaves its pinned rules at
"could not run" — never a silent skip and never a guess.

The pins are **verified, not merely recorded**: `make verify-pins`
(run in CI beside the other repository checks) fetches each verified
CloudNativePG release's source through the Go module proxy and greps
every pinned rule's strings in it — the phase, condition type, event
reason, or log line the rule matches, or the source strings a rule
names explicitly when its condition's literals are assembled at runtime
(a metric name, a JSON envelope). A reworded upstream message fails the
build instead of silently never matching, and widening a span means
adding the release to the verified list and letting that check pass.

## What each check kind needs

| Evidence | Requires |
|---|---|
| Operator status: phases, conditions, backup phases, declared database objects, primary-move timing | The cluster collectors (always on). |
| Events | The event collector (always on). Only events on the `Cluster` object and member pods are observable. |
| Resource quotas | The `resourcequotas` grant in the Role (in the shipped example). It is what lets a quota refusal name the quota — ceiling and usage — instead of only the refused object's symptom. |
| Container states | The pod collectors (always on). |
| Log messages | `LOG_STREAM_ENABLED=true` (which requires `ALLOW_LOGS=true`). With following off, every log-backed check reports "could not run". |
| Metric flags and thresholds | Metrics scraping enabled. |
| Pooler instance counts and pooler pod states | The pooler collectors (always on). |
| Pooler queue depth | Metrics scraping enabled; the PgBouncer exporter's window. |
| Failover quorum | The failover-quorum collector (always on). Absence of the resource is a clear result: the cluster runs no quorum. |
| Image catalogs | The image-catalog collector (always on) for namespaced catalogs; a cluster-scoped catalog needs the optional lookup enabled, and reads "could not run" otherwise. |
| Repository evidence | The repository-evidence consumer configured, the sidecar answering, and a completed scan. Every way the channel can be silent — not configured, never answered, contact lost, no scan yet, the sidecar's own staleness, an unrecognised report variant — is named as the reason a repository check could not run. |

## Log-backed findings and their window

Log following is best effort: a stream breaks on every container
restart and Kubernetes cannot report what was missed. Log-backed
findings therefore state counts as floors, and their absence rules
nothing out.

A log observation also **expires**: `LOG_MATCH_MAX_AGE` (default `6h`)
after the last matching line, the finding is dropped. A finding is a
claim about what the logs say now — a line nothing has repeated since
yesterday stops being that, and a stale finding that can never clear
teaches an operator to ignore the screen. A recurring failure renews
its window on every match and keeps its original first-seen time.
