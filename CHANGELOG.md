# Changelog

All notable changes to pgConsole are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

pgConsole is **pre-1.0**: 0.x releases may change environment variables,
route paths, and the forwarded-header contract without a deprecation
period. Pin an exact image tag and read the notes before upgrading.

## [Unreleased]

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

[Unreleased]: https://github.com/fyannk/pgConsole/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/fyannk/pgConsole/releases/tag/v0.1.0
