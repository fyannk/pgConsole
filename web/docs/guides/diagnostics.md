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
| could not run | An input this deployment **asked for** that the console cannot read: never observed yet, not permitted, or contact lost and what it holds is stale. A check that could not run rules nothing out: stale data can neither clear a check nor match one. Some of these are new — contact lost a minute ago — and some have held since a Role was written without a grant; what they share is a gap between what was asked for and what arrived. |
| needs a source that is switched off | An input this deployment has not turned on — log following, the object timeline, a scraper, the repository-evidence consumer. These rule nothing out either, but nothing is wrong with them: turning one on is a decision, not a repair, so they collapse under their own line instead of sitting among the faults. |
| does not apply | The rule's version pins exclude the versions actually observed. Distinct from clear on purpose: the rule ruled itself out, not the failure. |

Those last two are one outcome in the engine and two groups on the
screen, and the split is worth explaining. With log following off —
which can be switched off — every log-backed check reports that it
cannot run, permanently, identically, on every refresh of every healthy
cluster.
Put a scraper that stopped answering five minutes ago in that same list
and it is the twenty-eighth row of something a reader has already
learned to skip.

The line between them is not how long each has been true — an input the
console has never been permitted to read stays unreadable until someone
edits a Role — but whether this deployment asked for it. A switched-off
source is working exactly as configured. A configured source the console
cannot read is not, however long that has been so.

### Stale readings, and why the metrics window is judged differently

Most sources say when they have gone stale: the collector lost contact,
the snapshot is marked, and every check reading it withdraws. The
scraped metrics window cannot do that, for two reasons. The scraper
sweeps each instance separately, so losing one exporter freezes that
instance's readings while the rest stay current — a flag on the window
as a whole would be wrong in both directions. And a frozen exporter
does not look frozen: it answers every read with a perfectly plausible
number.

So a scraped reading is judged one at a time, against the age of the
sweep that produced it. A reading older than three scrape intervals —
never less than a minute, however fast the console sweeps — is refused,
and the check reports "could not run" naming how far behind the newest
refused reading is. This applies before the reading's value is looked
at, which is the point: a replication lag from an hour ago reported as
a match would be a claim about the past dressed as the present, and the
same reading sitting below its threshold would clear the check while
ruling out nothing at all. The second is the more dangerous, because
nothing on the screen would look wrong.

An instance still being swept is unaffected: a finding on it stands
whether or not another instance's exporter has gone quiet.

### Log checks, and why silence has to be earned

The log record is the one source where absence is the entire signal. A
log-backed check fires on a line appearing, so its clear result is the
claim that the line was **not written** — and that claim rests wholly on
the console having been listening. While a container's stream is not
open, it was not.

So a log check reports "could not run" whenever the follower is not
reading every container it means to, naming which container, for how
long, and the follower's own account of why. It does the same when the
pod roster cannot be read at all, because without it the console does
not know which containers ought to be talking, and a container it has
never heard of is one whose silence proves nothing.

A hole in the *past* record is a different matter and does not withhold
a clear. Following is best effort: a stream ends on every container
restart, and a check that could never clear because a pod once restarted
would teach a reader to ignore the screen. What withholds a clear is a
blind window that is open now.

Matches are unaffected — a line that was seen was seen. An unread
container elsewhere is a reason not to conclude silence, never a reason
to withdraw a finding the console actually has.

The follower reads the containers the kubelet reports **running**. A
container that has terminated has said everything it is going to say,
and one still waiting has not started; not having a stream open to
either is not blindness. This is also why a terminated container's log
stops being re-read: its stream replays the whole log from the beginning
on every reconnect, which would keep a finding's last-seen instant fresh
forever and let a line from hours ago read as current.

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

## How log checks match

CloudNativePG writes JSON, so a log check matches one of two ways.

| Match | When it is used |
|---|---|
| **field** | The field carrying the string has been read out of the emitting component's source. `postgres-fatal` reads `record.error_severity`, the field the operator's log pipe puts a server record's severity in; the four operator-message checks read `msg`. Paths are literal and dotted — no wildcards, no indexing. |
| **substring** | Everything else: the message is known but the field carrying it is not, as with the barman-cloud checks, whose strings the command-line tooling writes rather than any Go source this console can read. |

Field matching is the more precise of the two and the less widely
usable. Searching a whole line for `failed to run wal-archive command`
also matches an operator message quoting that failure back inside its
own `error` field; reading the `msg` field does not. Neither form is a
regular expression, deliberately: a rule that cannot express something
clever is a rule that cannot quietly match the wrong thing.

A line that is not valid JSON matches no field check, which is correct
— a line the component did not write in its structured format is not
one whose fields can be read. Each line is decoded once for the whole
rule set, and only when some rule asks for a field.

## Checks that count over time

Every other source is a snapshot of now, so a fault visible only in
repetition — a pod destroyed and remade over and over, a definition two
controllers keep rewriting in turn — is invisible to a check that reads
the present. Two checks read the **object timeline** instead, and count
what it records inside a trailing window:

| Check | Counts |
|---|---|
| `k8s-pod-replaced-repeatedly` | Distinct object identities under one pod name. Counting identities rather than edits separates a replacement from a change: an edited pod stays one object, a replaced one is a new object wearing the old name. |
| `k8s-definition-rewritten-repeatedly` | Records of the definition changing, as opposed to the status, with the field managers the API server attributed the writes to. Two managers alternating is one undoing the other. |

The timeline coalesces rapid repeats and evicts old revisions to stay
inside its bounds, so a count taken from it can only under-report. A
match is therefore sound and **an absence rules nothing out** — the same
footing as the log checks, and both findings say so. A record the
console only discovered after losing contact is counted but flagged,
because its timing is bounded rather than known.

With `HISTORY_ENABLED=false` both checks report "could not run".

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

Some of the console's own knowledge expires on a calendar rather than
on a release. A support boundary moves when upstream retires the next
version, and nothing in the observed world changes to announce it, so
the two end-of-life rules and the verified-release list each carry the
date their claim stops being safe to assert unreviewed. The catalog's
tests fail once that date passes, naming what to go and read. A console
that kept telling operators a supported version is unsupported — or
said nothing about one that no longer is — would be worse than a red
build.

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
| Log messages | `LOG_STREAM_ENABLED`, which defaults to on wherever it is read — diagnostics enabled and `ALLOW_LOGS=true` — and can be switched off explicitly. With following off, every log-backed check reports that it needs a source that is switched off. They are the largest group in the catalog and mostly critical — the checks that quote the server's own words rather than inferring a fault from a phase — so turning following off is a real reduction in what the screen can tell you. What it buys back: a line that matches a rule is retained verbatim as that finding's evidence, and a PostgreSQL error record can carry statement text with literal values. |
| Metric flags and thresholds | Metrics scraping enabled. |
| Pooler instance counts and pooler pod states | The pooler collectors (always on). |
| Pooler queue depth | Metrics scraping enabled; the PgBouncer exporter's window. |
| Failover quorum | The failover-quorum collector (always on). Absence of the resource is a clear result: the cluster runs no quorum. |
| Image catalogs | The image-catalog collector (always on) for namespaced catalogs; a cluster-scoped catalog needs the optional lookup enabled, and reads "could not run" otherwise. |
| Object timeline | `HISTORY_ENABLED=true` (the default). With history off, the checks that count over time report "could not run". |
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

For that window to mean anything, each stream carries only what is
written after it opens. A reconnecting follower does not re-read the
log it has already seen: doing so would count those lines again,
turning "at least *n* matching lines" into a count of re-reads, and
would renew the observation's window on every reconnect — so a finding
would stay current because the connection blinked rather than because
the fault recurred, and would never expire while the breaks continued.

The cost is that lines written while no stream was open are not
recovered, which is the honest trade: the console records that window
as a gap and reports the container as unread rather than pretending to
have read it. Looking at what was actually written in the meantime is
the **log tail** screen's job, and it asks for history of its own.
