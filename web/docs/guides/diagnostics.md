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

Two panels carry the screen, and the second is as load-bearing as the
first:

- **Findings** — what matched, most severe first. Every finding quotes
  its evidence verbatim and names the origin of each quoted claim (the
  operator, Kubernetes, a container log, the metrics exporter). A
  finding restates and correlates; it never concludes something no
  source reports.
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
| could not run | An input was never observed — log following off, metrics not scraped, a version not observed. A check that could not run rules nothing out. |
| does not apply | The rule's version pins exclude the versions actually observed. Distinct from clear on purpose: the rule ruled itself out, not the failure. |

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

## What each check kind needs

| Evidence | Requires |
|---|---|
| Operator status: phases, conditions, backup phases, declared database objects, primary-move timing | The cluster collectors (always on). |
| Events | The event collector (always on). Only events on the `Cluster` object and member pods are observable. |
| Container states | The pod collectors (always on). |
| Log messages | `LOG_STREAM_ENABLED=true` (which requires `ALLOW_LOGS=true`). With following off, every log-backed check reports "could not run". |
| Metric flags and thresholds | Metrics scraping enabled. |

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
