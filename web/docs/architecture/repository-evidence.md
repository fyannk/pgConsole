---
sidebar_position: 3
title: Repository evidence
---

# Repository evidence

pgConsole never reads object storage. When you want structural facts about
the backup destination — object counts, WAL continuity, retention — those
come from the **ObjectStoreViewer**, running as a loopback-only sidecar in
the console pod and publishing a versioned evidence API over a pod-private
Unix socket. pgConsole is a **consumer** of that API and nothing more: it
gains no store credentials and parses no repository.

## The channel

The evidence consumer is enabled by four environment variables that
validate all-or-nothing — set all four or set none:

| Variable | Meaning |
|---|---|
| `REPOSITORY_EVIDENCE_URL` | A `unix://` socket URI or absolute socket path. Loopback and TCP forms refuse to start — the API exists only on a pod-private socket. |
| `REPOSITORY_EVIDENCE_TOKEN_FILE` | Absolute path to the operator-mounted, pod-local bearer token. |
| `REPOSITORY_EXPECTED_FINGERPRINT` | The `sha256:` destination fingerprint the responses must carry. |
| `REPOSITORY_BARMAN_SERVER` | The exact Barman server name of the operator-supplied identity mapping. |

With none set, the consumer is disabled: no panel, no poller, no socket.

## What it shows, and what it never claims

The evidence panel renders the sidecar's reported facts, each attributed to
the **repository-evidence** origin and carrying its own staleness. If the
sidecar is unreachable, the console retains the last-good report marked
stale rather than blanking it. The console re-renders only the sidecar's
typed states, reason codes, counts, and times; the sidecar's free-text
diagnostic messages never leave the consumer and are never proxied to a
browser.

For a cluster using the barman-cloud plugin, the console also shows the
configured `barmanObjectName` and performs a single `ObjectStore` lookup per
catalog refresh, purely to confirm the reference resolves — it retains none
of that object's spec. A missing permission, CRD, or object degrades only
that one reference to `unknown`.

pgConsole never merges evidence into an operator claim, and never uses the
words "verified", "restorable", "validated backup", or "safe to restore".
The cross-check view places the operator's claim and the repository's
observation side by side and names the outcome without asserting that a
backup is usable. Correlation is strict: it matches on the exact
`status.backupId` and on the **observed** cluster UID — never on name
similarity, never guessed. The outcome is one of a closed set of six:

- **agreement** — both sides describe the same backup;
- **operator-claim-only** — the operator claims it; the repository has no
  matching object;
- **discrepancy** — both describe it, but disagree;
- **repository-orphan** — the repository has it; no operator claim does;
- **ambiguous** — the mapping is not one-to-one, so the console refuses to
  guess;
- **unknown** — there is not yet enough to say.

Correlation is `unknown` until the first successful sidecar observation, and
a stale cluster observation supports historical display only — it never
proves *current* agreement.

## Why a separate process

The separation is a security property. The only component that holds store
credentials and parses the repository is the viewer; compiling that into
pgConsole is a permanent non-goal. The console links to the standalone
viewer for deep inspection and consumes the sidecar's bounded, authenticated
snapshots for the at-a-glance cross-check.
