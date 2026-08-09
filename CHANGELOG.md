# Changelog

All notable changes to pgConsole are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

pgConsole is **pre-1.0**: 0.x releases may change environment variables,
route paths, and the forwarded-header contract without a deprecation
period. Pin an exact image tag and read the notes before upgrading.

## [Unreleased]

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

[Unreleased]: https://github.com/fyannk/pgConsole/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/fyannk/pgConsole/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/fyannk/pgConsole/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/fyannk/pgConsole/releases/tag/v0.1.0
