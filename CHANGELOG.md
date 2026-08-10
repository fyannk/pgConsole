# Changelog

All notable changes to pgConsole are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

pgConsole is **pre-1.0**: 0.x releases may change environment variables,
route paths, and the forwarded-header contract without a deprecation
period. Pin an exact image tag and read the notes before upgrading.

## [Unreleased]

### Added

- **A version-aware diagnostic rule catalog.** A diagnostic is now data:
  a rule declaring the component it is about, the version pins its claim
  was verified against, the observation — a log line, an event, an
  operator condition or phase, a metric flag or threshold, a backup
  phase, a declared object's reconciliation report, a container state —
  and the finding it means. Rules live one package per component under
  `internal/diagnose/catalog/`, and each rule gets its own row on the
  checks panel with a fourth honest outcome beside matched, clear, and
  could-not-run: **"does not apply"**, when the observed versions fall
  outside the rule's pins.

- **The CloudNativePG catalog: 54 rules mined from the operator's own
  source**, every string verified verbatim against releases 1.28.4,
  1.29.2 and 1.30.0 and pinned to the span the verification covered.
  Every blocked or waiting phase with its reason quoted; the conditions
  that carry failure text the phase does not (`ContinuousArchiving`
  above all — a cluster reports healthy while WAL archiving fails and
  the disk fills); the operator's event-recorded refusals; fencing and
  supervised-switchover flags from the exporter; failed, stuck, and
  archiving-blocked backups; suspended and silently-stopped backup
  schedules; a primary move still in flight on the operator's own clock;
  declared databases, roles, publications and subscriptions the operator
  cannot apply; and twenty instance-manager log messages, from
  `pg_rewind` failures to the WAL volume running dry.

- **Kubernetes and PostgreSQL rules** filling the gaps the hand-written
  detectors leave: volumes that cannot mount or attach, evictions,
  crash loops and OOM kills (instance *and* pooler containers — a
  crash-looping sidecar breaks backups while the instance reads
  healthy), containers the kubelet cannot construct, transaction-id and
  multixact wraparound thresholds, and end-of-life rules for both
  PostgreSQL and Kubernetes whose version pin is the whole diagnostic.

- **Versions are observed, never configured.** CloudNativePG from the
  `bootstrap-controller` init container the operator injects into every
  instance pod; PostgreSQL from the operator's status; the Barman Cloud
  plugin from its sidecar image tag; Kubernetes from the API server's
  own `/version` report, polled every five minutes — the console's only
  poll, a non-resource URL needing no Role. A version the console has
  not observed leaves its pinned rules honestly at "could not run".

- **`LOG_MATCH_MAX_AGE`** (default `6h`). A diagnostics log observation
  now expires this long after its last matching line, so a finding
  cannot outlive its relevance; a recurring failure renews its window on
  every match. Previously a single matched line kept its finding until
  restart.

### Changed

- **The log matcher's rules come from the catalog.** A log line is
  declared once — with its pins and its finding — and matched
  continuously; `logstream.DefaultRules` is gone. The `postgres-fatal`
  and `postgres-panic` rules now match the log pipe's `error_severity`
  field instead of a bare `FATAL:`, so they see real server records and
  stop counting quoted noise; FATAL reports as a warning, because
  routine failures — a wrong password — log at that severity too.

## [0.4.0] - 2026-08-09

### Added

- **Diagnostics** (`ALLOW_DIAGNOSTICS=true`, `poweruser`). A screen that
  correlates facts the other screens already carry into findings: what is
  wrong, where, and the claim it rests on. It observes nothing of its own
  and needs **no Role** — every detector is a pure function over the
  snapshots the console already publishes.

  Every finding quotes its evidence verbatim with the origin named, and
  links to a screen rather than offering an action. The page also lists
  **what was checked**, including anything that could not run and why: an
  empty result means no detector matched, never that the cluster is
  healthy.

  Five detectors ship:

  | Detector | Reports |
  |---|---|
  | `backup-cadence` | a schedule running far more often than its author is likely to have meant |
  | `resource-quota` | a create the API server refused against a namespace quota |
  | `pod-scheduling` | a pod the scheduler cannot place |
  | `image-pull` | a container whose image cannot be pulled, naming the container |
  | `volume-binding` | a claim that never bound |

  `backup-cadence` is the one that finds something otherwise invisible.
  CloudNativePG takes a six-field cron, seconds first, so a five-field
  expression written from habit becomes valid and usually means hourly.
  Nothing reports it: the backups succeed and the cluster is healthy while
  a full base backup runs twenty-four times a day.

- **Container-addressable log tails.** `GET /logs/{pod}/{container}` reads
  any container the pod declares, including init containers and plugin
  sidecars; `GET /logs/{pod}` keeps its meaning, so no existing link
  changes. CNPG-I moves backup and WAL archiving into sidecars, and those
  failures were previously unreadable.

- **Per-container pod facts.** The pod detail screen lists every
  container with its image, state, the kubelet's reason, restarts, and
  readiness. This is where `ImagePullBackOff`, `CrashLoopBackOff`, and
  `OOMKilled` now surface.

### Fixed

- **A pod's restart count was the sum across every container**, so a
  crash-looping sidecar reported an unstable instance that had never
  restarted. It is now the PostgreSQL container's own count, with
  per-container counts listed beside it. `kubectl` shows the sum; kubectl
  is not making a claim about a database.

### Security

- The log tail's boundary moved from the container to the **pod**,
  deliberately. Once controller ownership proves the pod belongs to this
  cluster, restricting reads to the PostgreSQL container bought nothing —
  that container's log is the most sensitive stream in the pod, since it
  can carry query text, which is why the tail is `poweruser`-gated and
  byte-bounded. Meanwhile the restriction hid the sidecars the failures
  moved into. The container name is still checked against what the pod
  declares, and a name it does not declare is refused as not-found,
  matching a non-member pod.

### Upgrading

Nothing to do. `ALLOW_DIAGNOSTICS` defaults to `false`, so the screen is
absent until you ask for it, and it needs no RBAC change when you do. The
only behaviour an existing deployment sees is the corrected restart count
and the new log route.

## [0.3.0] - 2026-08-09

### Changed

- **The access-request review panel grants a level, not a role.** pgToolBox
  dropped the `PgToolBoxRole` kind: access is granted as one of the closed
  levels `view`, `poweruser`, `dba` — the same ladder the console admits
  routes by. Three parts of the contract move with it, and none is
  backwards compatible with 0.2.0:
  - the console writes **`status.requestedLevel`** (a plain string) on an
    approval, where it previously wrote `status.requestedRoleRef.name`;
  - the approval form field is **`level`**, previously `role`;
  - the approval picker is a constant, not a listing. It offers the three
    levels in every deployment, so it can no longer be emptied by a
    forbidden or slow read — which previously left a reviewer with a picker
    of no options and no way to approve anything.

  An approval is validated against the closed set and matched exactly, so
  a value the operator's `RoleLevel` enum would reject never reaches the
  API server.

### Removed

- **The review Role no longer needs `pgtoolboxroles`.** Drop the
  `get`/`list`/`watch` grant on that resource from any Role you maintain by
  hand; `deploy/access-review-role.yaml` no longer contains it. The panel's
  entire authority is now reading access requests and patching their
  status. Leaving the old grant in place is harmless but grants access to
  a kind that no longer exists.

### Upgrading

Nothing to do beyond the usual tag bump unless you maintain the review Role
yourself, in which case remove the `pgtoolboxroles` rule. Requests decided
under 0.2.0 keep their recorded `requestedRoleRef`; the panel reads
`requestedLevel`, so an older decision renders its level as `—`. The
decision itself, its reviewer, and its audit line are unaffected.

## [0.2.0] - 2026-08-08

### Added

- **Link-outs accept a root-relative path.** `PGADMIN_URL`,
  `OBJECTSTOREVIEWER_URL`, and `MONITORING_URL` now take either an absolute
  URL or a same-origin path such as `/pgadmin`, which is how a single Route
  or Ingress usually exposes the pgtoolbox family. A relative reference
  inherits the reader's scheme, so it cannot downgrade and
  `ALLOW_INSECURE_LINKS` does not apply to it; behind a proxy that
  terminates TLS and rewrites `Host` it is also the more honest value,
  because the console cannot know its own external URL. Protocol-relative
  values (`//host/path`, and the `/\host` spelling browsers normalise to
  it) are refused: they name a different origin.

### Fixed

- `nanoid` in the documentation-site tree bumped to 3.3.18 for
  GHSA-2v37-7h3g-55p8.

### Changed

- `make audit` runs through `hack/check-npm-audit.sh`, which keeps the
  `high` threshold and subtracts only advisories reviewed and recorded in
  `hack/npm-audit-accepted.txt`. Anything unlisted still fails, including a
  new advisory in a package that already has an entry. Two unfixable
  build-time `image-size` advisories are accepted there with their reasoning
  and the condition that would remove them.
- One error message: a link-out that is neither absolute nor root-relative
  now reports `must be an absolute URL or a root-relative path`.

### Internal

No behaviour change from these, listed because they alter the shipped
artifacts or the contributor contract:

- `NOTICE` records the htmx, uPlot, and Alpine notices for the assets
  embedded in the binary, and the image now carries `third_party/` licences
  alongside `LICENSE`.
- `CODEOWNERS` records ownership of the trust boundary and publishing path.
- `CONTRIBUTING.md` is now the single canonical statement of the repository
  invariants; `AGENTS.md` refers to them by number instead of restating
  them. The two copies had diverged, with one describing the authorization
  fail-safe backwards.
- `cmd/pgconsole` gained tests for its fail-before-listen contracts:
  invalid configuration, an unusable history journal, an unusable metrics
  snapshot path, and an unreadable evidence token.

## [0.1.0] - 2026-08-07

First public release.

### Added

- **Cluster observation.** Operator-reported status, conditions, and verdict;
  membership-verified instance pods with roles and restarts; recent events;
  and a cluster topology view. Every claim is attributed to its origin —
  operator-reported, Kubernetes-observed, or application-derived — and stays
  visibly distinct from the others.
- **Backups.** Backup and ScheduledBackup catalog, retained object listing,
  and an optional pgObjectStoreViewer sidecar correlation for repository
  evidence.
- **Declared objects.** Database, DatabaseRole, Publication, and Subscription
  resources, with a bounded object-definition history and per-revision diffs.
- **Poolers.** Pooler overview, member pods, and PgBouncer metrics.
- **Metrics.** Instance and pooler metric windows charted from the exporters,
  with an optional on-disk snapshot that survives restarts.
- **Log tails.** Bounded, `poweruser`-gated instance and pooler log tails.
- **Day-2 operations** (opt-in via `ALLOW_OPERATIONS`). Backup, reload,
  restart, and promote — the entire mutation surface — behind proxy-asserted
  levels, explicit confirmation, HMAC CSRF tokens, `Sec-Fetch-Site` checks,
  audit logging, and namespaced RBAC.
- **Access-request review** (opt-in via `ALLOW_ACCESS_REVIEW`), a `dba`-gated
  panel over the pgToolBox CRDs.
- **Supply chain.** Multi-architecture images and binaries with SHA-256
  checksums, SPDX SBOM, build provenance, and GitHub build attestations.

### Security

- The console authenticates nobody. A trusted proxy asserts the user's level
  via `X-PgToolBox-Level`; a missing, empty, or unrecognized level reaches
  **nothing**. There is no ungated baseline and no privilege inference.
- Strict Content-Security-Policy with no `unsafe-inline` and no `unsafe-eval`,
  backed by the Alpine CSP build and htmx configured without eval. No cookies
  are set at any point.
- Ships as a distroless non-root image with a read-only root filesystem, all
  capabilities dropped, `RuntimeDefault` seccomp, and no pod-wide
  ServiceAccount token automount.
- The example RBAC grants no access to `secrets` and holds no mutating verb;
  `hack/check-readonly.sh` enforces this in CI.

### Known limitations

- The pgToolBox operator and its `PgConsole` API are not published yet, so the
  standalone manifests are the only install path. Deploying standalone means
  you own the proxy and NetworkPolicy trust boundary.
- pgConsole reports what CloudNativePG and Kubernetes claim. It does not
  independently prove replication health, data integrity, or restoreability,
  and it provides no SQL access, database contents, or Secret reads.

[Unreleased]: https://github.com/fyannk/pgConsole/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/fyannk/pgConsole/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/fyannk/pgConsole/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/fyannk/pgConsole/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/fyannk/pgConsole/releases/tag/v0.1.0
