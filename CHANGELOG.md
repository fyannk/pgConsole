# Changelog

All notable changes to pgConsole are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

pgConsole is **pre-1.0**: 0.x releases may change environment variables,
route paths, and the forwarded-header contract without a deprecation
period. Pin an exact image tag and read the notes before upgrading.

## [Unreleased]

### Added

- **The console's dated knowledge expires on purpose.** A support
  boundary moves when upstream retires the next version, and nothing
  observable changes to announce it, so the PostgreSQL and Kubernetes
  end-of-life rules and the CloudNativePG verified-release list now each
  carry the date their claim stops being safe to assert unreviewed. The
  catalog's tests fail once that date passes, naming what to re-read and
  what to change. It is the discipline `make verify-pins` applies to the
  operator's strings, turned on the console's own claims: a stale
  boundary stops the build rather than quietly telling operators that a
  supported version is unsupported. A rule that fires on its version
  pins alone must declare one, which a second test holds.

- **Log checks can match a named field instead of the whole line.**
  CloudNativePG writes JSON, so a check that knows which field carries
  the string it looks for now says so: `postgres-fatal` and
  `postgres-panic` read `record.error_severity`, the field the
  operator's log pipe puts a server record's severity in, and the four
  operator-message checks read `msg`. Searching a whole line for
  `failed to run wal-archive command` also matched an operator message
  quoting that failure back inside its own `error` field; reading the
  field does not, and the match no longer depends on how the envelope's
  keys happen to be ordered. Each line is decoded once for the whole
  rule set and only when a rule asks for a field; a line that is not
  JSON matches no field check. The checks whose strings come from the
  barman-cloud command-line tooling stay on substrings, because the
  field carrying them cannot be read out of any source this console
  verifies against. Paths are verified segment by segment against the
  operator's own JSON tags by `make verify-pins`, alongside the values.

- **Diagnostics that count over time.** Every other source the engine
  reads is a snapshot of now, so a fault visible only in repetition was
  invisible. Two checks read the object timeline instead:
  `k8s-pod-replaced-repeatedly` counts distinct identities under one pod
  name, which separates a replacement from an edit, and
  `k8s-definition-rewritten-repeatedly` counts writes to a definition
  and names the field managers the API server attributed them to, so two
  controllers undoing each other are visible as such. The timeline
  coalesces rapid repeats and evicts old revisions, so both findings
  state their count as a floor and an absence rules nothing out, and a
  record discovered only after a contact gap is counted but flagged. The
  history snapshot, removed from the engine's input when nothing read
  it, is wired back now that these do.

- **Diagnostics for the replication incidents nothing reported.** Five
  checks over metrics the console already scrapes:
  `cnpg-replica-not-receiving` (a replica in recovery, with no WAL
  receiver, whose lag is not closing), `cnpg-replication-lag-high`,
  `cnpg-sync-replicas-short` (commits waiting on standbys that are not
  there), `cnpg-slot-retaining-wal` (a replication slot pinning WAL that
  fills the volume), and `postgres-long-transaction` (an open
  transaction holding the vacuum horizon). The last two are declared
  causes of findings that already existed: a full WAL volume and
  transaction-id wraparound now nest under the thing that caused them.
- **Thresholds are held, not spiked.** A metric check can require its
  threshold to be met across every retained sample of a trailing window,
  so a lag spike during a write burst is not a finding while the same
  lag held for a quarter of an hour is. An instance whose window is too
  short to show either way is reported as one the check could not judge.
  Every threshold is declared in one place per component and rendered in
  the check's own row.
- **Corroborating checks report on one subject.** A check built from
  several observations now requires them to be about the same instance,
  so two branches matching on different pods are two facts rather than
  one invented finding. A branch about no single object, such as a
  cluster-wide condition, corroborates any instance.
- **Diagnostics read every source the console publishes.** Five
  snapshots reached the engine and no check consumed them; each now
  has one. `cnpg-pooler-short` reports a Pooler with fewer ready
  instances than declared, and `cnpg-pooler-clients-waiting` reads the
  PgBouncer exporter's longest client wait. `cnpg-quorum-standbys-short`
  reports a failover quorum with fewer potentially synchronous standbys
  than transactions wait for. `cnpg-image-catalog-missing` and
  `cnpg-image-catalog-lacks-major` follow the Cluster's `imageCatalogRef`
  to the catalog it names, and are declared causes of the operator's
  own "incomplete or invalid image catalog" phase. Three repository
  checks restate the repository-evidence sidecar's typed state for WAL
  continuity, recovery coverage, and retention, naming every way the
  channel can be silent as the reason they could not run; the WAL
  finding nests under the operator's archiving condition. A test now
  proves that withholding any one source from the engine changes what
  a run reports, so a source can no longer be plumbed in and read by
  nothing. The history snapshot, which no check read, no longer reaches
  the engine.
- **A golden test of the incident view** over the real catalog: the
  object-store refusal, archiving failure, full WAL volume, panic, exit
  and crash loop on one instance render as one card with every link
  stating its terms, while a crash loop on another instance stays its
  own card and names the near misses.
- **Diagnostics relate findings on evidence, not on names.** Every
  finding now names its subject (the pod, backup, claim or quota it is
  about) and the time of the observation where its source reports one.
  The catalog's causal relations state a scope (same cluster or same
  pod), an optional window, and a qualitative strength (established
  mechanism or plausible), and a finding nests under its cause only when
  the relation admits the pair — a full WAL volume on one instance no
  longer swallows a crash loop on another. A cause that matched but on
  another object or at another time stays its own card, with the near
  miss listed beneath and the relation's own reason for keeping them
  apart. Nested cards show the terms they were admitted on.
- **`cnpg-primary-disagreement`**, the first check that compares two
  sources: the operator's `currentPrimary` against each instance's own
  `pg_is_in_recovery()` from the exporter. The contradiction is the
  finding; both sides are quoted and neither is presumed right. A
  primary move in flight keeps the check clear. Behind it, a rule can
  now state a condition as an `AllOf` of several, with a branch that
  could not run making the whole check one that could not run.
- **Catalog pins are verified against the operator's source.**
  `make verify-pins`, run in CI, fetches each verified CloudNativePG
  release through the Go module proxy and greps every pinned rule's
  strings in it. Rules whose literals are assembled at runtime name the
  source strings they rest on explicitly. A reworded upstream message
  now fails the build instead of silently never matching.

### Fixed

- **The Cluster and FailoverQuorum watches no longer resume from the
  pinned get's resource version.** A single object's resource version is
  its last modification, and on a cluster idle longer than the API
  server's watch window it is already expired — so every watch resumed
  from it died instantly with `410 Expired`, the re-seed handed back the
  same version, and the console froze on "Showing the last good view"
  (with a `contact lost` log line every second) even though contact was
  fine. The two singleton watches now start from the server's current
  state, which re-delivers the object once and then streams changes; the
  list-seeded watches were never affected, because a re-list always
  yields a fresh version.
- **Diagnostics honour staleness on every source.** The cluster, pod,
  pooler-pod, event and infrastructure snapshots all carry a stale flag
  when their collector loses contact, but only the backup and
  database-object checks consulted it. A phase, condition, primary-move,
  container-state or event check — and the quota, scheduling, image-pull
  and volume detectors — could report "clear" from a snapshot the
  console already knew was stale, or quote stale evidence as current.
  Every such check now reports "could not run" and names staleness as
  the reason, matching what the backup checks always did. Supporting
  evidence read from a stale snapshot (the instance shortfall beside a
  quota finding, the provisioner event beside an unbound claim) is left
  out rather than quoted.

## [0.6.1] - 2026-08-27

A rebuild, not a behaviour change. Nothing about how the console works is
different from 0.6.0. What changed is the toolchain underneath: the
binaries and the container are now built with Go 1.27.0, which does not
carry the eight standard-library vulnerabilities that the Go 1.26.5 used
for 0.6.0 does.

### Security

- **Rebuilt on Go 1.27.0.** The 0.6.0 artifacts were built with Go
  1.26.5, and `govulncheck` reports the published binary as affected by
  eight standard-library advisories disclosed after that release:
  [GO-2026-6218](https://pkg.go.dev/vuln/GO-2026-6218) (`net/url`),
  [GO-2026-6091](https://pkg.go.dev/vuln/GO-2026-6091) (`html/template`),
  [GO-2026-6090](https://pkg.go.dev/vuln/GO-2026-6090) (`crypto/tls`),
  [GO-2026-6089](https://pkg.go.dev/vuln/GO-2026-6089) and
  [GO-2026-5026](https://pkg.go.dev/vuln/GO-2026-5026) (`net/http`),
  [GO-2026-6088](https://pkg.go.dev/vuln/GO-2026-6088) (`encoding/xml`),
  [GO-2026-5972](https://pkg.go.dev/vuln/GO-2026-5972) (`encoding/asn1`),
  and [GO-2026-5942](https://pkg.go.dev/vuln/GO-2026-5942) (`net`). The
  console parses forwarded headers, serves HTML templates, and speaks TLS
  to the Kubernetes API, so several of these are on paths it uses. The
  scan shipped with 0.6.0 was clean when it ran — every one of these was
  disclosed afterwards.

- **The Go version can no longer drift.** It lives in `go.mod` alone, CI
  reads it from there, and `hack/check-go-version.sh` fails the build if
  the container's builder image disagrees. The language floor is 1.26.6,
  above what the module graph derives, so building from source with
  `GOTOOLCHAIN=local` cannot reproduce a vulnerable binary either.

- **Dependency updates** merged since 0.6.0: `golang.org/x/text`,
  `golang.org/x/term`, the pinned GitHub Actions, and the builder image.

### Changed

- Nothing user-facing. The remaining commits are CI and repository
  tooling: auto-merge gated on the full pipeline, a weekly watcher for
  the tool versions Dependabot cannot see, OpenSSF Scorecard, fuzz
  targets on the authorization, identity, and redaction parsers, and a
  code-review skill carrying the repository's invariants.

## [0.6.0] - 2026-08-11

### Added

- **The diagnostics screen answers the reader's three questions.** A
  cluster-state strip opens the page with the operator's own account —
  phase and reason, instances ready against declared, the current
  primary. Findings group into **incidents**: a finding whose declared
  cause also matched nests inside that cause's card, so "archiving
  failed → WAL filled the volume → PostgreSQL kept down" reads as one
  story with one root instead of three alarms; the relation is
  catalog-pinned and the card says so, and every nested finding keeps
  its own quoted evidence. Findings now carry **"What to do"** —
  console guidance, labeled as guidance and rendered apart from the
  evidence, because advice is the one thing on the screen no source
  reported.

- **ResourceQuotas are observed** (`resourcequotas` `list`/`watch`, in
  the shipped example Role), and a `quota-exhausted` check names the
  quota behind a refusal: which quota, its ceiling, its reported usage.
  It reads as a warning at capacity and escalates to critical when the
  cluster is visibly short of its declared instances at the same time —
  the refusal in progress. The unschedulable-pod and quota-refusal
  findings nest beneath it as one incident. Deployments predating the
  grant see the check report "could not run" until the Role is updated.

- **A broken-cluster gallery for dev** (`hack/dev-problems.sh`): six
  deliberately broken, deliberately tiny clusters beside the healthy
  dev environment — quota-refused PVCs, a conflicting archive
  destination, refused object-store credentials, a WAL volume and a
  data volume that genuinely fill, a memory limit too small to
  bootstrap — each with its own console. It exists to exercise the
  catalog against real failures, and both fixes below came out of it.

### Fixed

- **A cluster that broke before its first instance pod had no
  observable operator version**, so every pinned check stepped aside at
  the exact moment the operator declared the cluster unrecoverable. The
  bootstrap Jobs carry the same injected operator image, so the
  observed child Jobs now expose it and the version falls back to it —
  an instance pod still wins when one exists.

- **The object-store credential check pinned AWS's wording.** MinIO
  answers bad credentials with a bare 403 — "An error occurred (403)",
  observed live — where AWS says `AccessDenied`, so a second rule,
  `object-store-forbidden`, now covers the other dialect.

### Upgrading

One thing to do, and it is the Role: this release adds the
`resourcequotas` `list`/`watch` read, so re-apply the Role manifest
beside the image bump. Skipping it breaks nothing — the console
degrades honestly, with the `quota-exhausted` check reporting "could
not run" and saying why — but the quota diagnostics stay dark until
the grant lands. Everything else rides the image: the incident view,
the state strip, and the guidance blocks appear on the existing
`ALLOW_DIAGNOSTICS` surface with no new flag.

## [0.5.0] - 2026-08-10

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

- **Continuous log following** (`LOG_STREAM_ENABLED`, default `false`).
  The console follows every container of every member pod — sidecars and
  init containers included — and the diagnostics matcher analyses each
  line as it arrives, keeping only what matched: one bounded observation
  per rule per container, so memory does not grow with log volume.
  Following is best effort and says so: a stream breaks on every
  container restart, Kubernetes cannot report what was missed, and each
  break is recorded as an explicit gap rather than joined across.
  Membership is proven per stream by the same check the on-demand tail
  uses, on the same `pods/log` grant — no new RBAC.

- **Retained log screens** (`LOG_BUFFER_BYTES`, default `0`;
  `LOG_BUFFER_TOTAL_BYTES`, `LOG_BUFFER_MAX_AGE`). With retention on,
  `/logs/{pod}/{container}` serves the stream the console observed —
  which **outlives the container**, so a crash-looping sidecar's
  explanation stays readable after the live tail answers not-found —
  with gaps rendered between the runs they separate and the live tail
  one click away. `/logs/{pod}` keeps its meaning and lists the streams
  the console holds; `?raw=1` stays live so the follow poll keeps
  getting fresh lines. Retention is a deliberate exposure decision —
  PostgreSQL logs can carry statement text — so it is off unless asked
  for, bounded per container and in total, aged out, and never written
  to disk. The pod detail's containers table links each container to its
  tail.

- **Every observed source reaches the detectors.** The diagnostic input
  carries all fifteen snapshots the console publishes — poolers and
  their pods, failover quorum, image catalogs, declared database
  objects, the object-definition history, repository evidence, and both
  metrics windows joined the original five — each with its own flag so
  "not observed" stays distinct from "observed and empty". A reflective
  test holds the wiring: a source added and forgotten fails it.

### Changed

- **The log matcher's rules come from the catalog.** A log line is
  declared once — with its pins and its finding — and matched
  continuously; `logstream.DefaultRules` is gone. The `postgres-fatal`
  and `postgres-panic` rules now match the log pipe's `error_severity`
  field instead of a bare `FATAL:`, so they see real server records and
  stop counting quoted noise; FATAL reports as a warning, because
  routine failures — a wrong password — log at that severity too.

- **The diagnostics screen grew a face worth reading.** Findings are
  cards whose left edge and chip carry the severity in the console's
  status colours — the word stays, the colour restates — with evidence
  rendered as what it is, a quotation: origin and object on a meta line,
  the verbatim text in a monospace block. The checks panel stopped being
  a seventy-row wall: a counted summary strip, then one collapsible
  group per outcome, with matches and could-not-run expanded and the
  clear majority folded under one honest line. Plain markup throughout —
  a reader without script opens the groups the same way.

- **Repository invariant 10 rewritten.** It said tails are never
  persisted, which was true of the only log path that then existed. It
  now describes all three — the on-demand tail retaining nothing, the
  matcher retaining only matches, the buffer retaining lines and off by
  default — and states that nothing writes log text to disk.

### Fixed

- **Log rules can carve out known-benign lines.** Rules gain `Except`:
  substrings any one of which withdraws a match, with the same guarantee
  as `Contains` — a substring cannot be made pathological by a hostile
  line. `postgres-fatal` names the five lifecycle refusals PostgreSQL
  stamps FATAL — a connection during startup, shutdown, or crash
  recovery, and the backends an orderly switchover terminates — which
  the operator's own probes provoke on every start. Without the
  carve-out the rule fired on every restart of every cluster, forever.

- **Evidence quotes no longer end where their content begins.** Quoted
  evidence went through the ordinary 512-rune message bound, and the
  operator's JSON log envelope alone runs past five hundred characters
  before the message field — so every quoted record was cut exactly
  there. Evidence now has its own display bound, equal to the matcher's
  storage bound of 2048.

- **The console wears its own mark.** The favicon was still the
  placeholder from before the branding pass, which restyled the
  documentation site and never touched the console's. Every tab now
  carries the same multi-resolution product icon the site serves.

### Upgrading

Nothing to do. `LOG_STREAM_ENABLED` and `LOG_BUFFER_BYTES` default to
off and zero, so an existing deployment follows nothing and retains
nothing until asked — the console can hold log text now, but only opted
into twice. Diagnostics stays behind `ALLOW_DIAGNOSTICS`, and none of it
needs an RBAC change.

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

[Unreleased]: https://github.com/fyannk/pgConsole/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/fyannk/pgConsole/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/fyannk/pgConsole/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/fyannk/pgConsole/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/fyannk/pgConsole/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/fyannk/pgConsole/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/fyannk/pgConsole/releases/tag/v0.1.0
