<p align="center">
  <img src="web/static/img/logo.png" width="150" alt="pgConsole logo">
</p>

<h1 align="center">pgConsole</h1>

<p align="center">
  <strong>Operate one CloudNativePG cluster without handing out kubectl.</strong><br>
  Source-attributed, bounded, and honest when Kubernetes or the operator is uncertain.
</p>

<p align="center">
  <a href="https://github.com/fyannk/pgConsole/actions/workflows/ci.yml"><img src="https://github.com/fyannk/pgConsole/actions/workflows/ci.yml/badge.svg" alt="CI status"></a>
  <a href="https://github.com/fyannk/pgConsole/actions/workflows/docs.yml"><img src="https://github.com/fyannk/pgConsole/actions/workflows/docs.yml/badge.svg" alt="Documentation status"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue.svg" alt="Apache-2.0 license"></a>
  <img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" alt="Go 1.26">
  <img src="https://img.shields.io/badge/status-0.x%20pre--1.0-orange.svg" alt="Pre-1.0">
</p>

pgConsole is a per-cluster operational console for one CloudNativePG
`Cluster`. It renders operator status and conditions, membership-verified
instance pods, recent events, backup resources, bounded log tails, and a
small set of explicitly enabled day-2 actions.

> [!IMPORTANT]
> pgConsole reports what CloudNativePG and Kubernetes claim. It does not
> independently prove replication health, data integrity, or restoreability.

## ✨ Why use it?

- **No kubectl for routine visibility** — application owners get a focused
  view of one cluster without receiving general Kubernetes credentials.
- **Every claim has a source** — operator-reported, Kubernetes-observed, and
  application-derived state remain visibly distinct.
- **Failure stays visible** — broken watches, forbidden reads, stale snapshots,
  truncation, and missing fields become `unknown` or `stale`, never healthy.
- **Bounded exposure** — event windows, retained objects, messages, and
  on-demand log tails all have explicit limits.
- **Guarded operations** — backup, reload, restart, and promote are the entire
  mutation surface, behind proxy-asserted levels, confirmation, CSRF, audit,
  feature flags, and namespaced RBAC.

## 🧩 What is available?

| Capability | Status |
|---|---|
| Cluster status, conditions, pods, roles, restarts, and events | ✅ Available |
| Backup and ScheduledBackup catalog | ✅ Available |
| Declared Database, DatabaseRole, Publication, and Subscription objects | ✅ Available |
| Pooler overview, member pods, and PgBouncer metrics | ✅ Available |
| Instance metrics windows, charted from the exporters | ✅ Available |
| Bounded object-definition history with per-revision diffs | ✅ Available |
| Bounded, poweruser-gated instance and pooler log tails | ✅ Available |
| Backup, reload, restart, and promote operations | ✅ Opt-in |
| DBA access-request review panel | ✅ Opt-in |
| Cluster-wide image catalog reads | ✅ Opt-in |
| pgObjectStoreViewer sidecar evidence correlation | ✅ Opt-in |
| SQL queries, database contents, or Secret access | ⛔ Not provided |
| Authentication, TLS termination, or user management | ⛔ Operator/proxy responsibility |

## 🚀 Quick start

pgConsole reads only in-cluster ServiceAccount credentials, so the development
path runs it inside a disposable kind cluster. You need Docker, kind, kubectl,
Go 1.26+, and `make`:

```bash
git clone https://github.com/fyannk/pgConsole.git
cd pgConsole
make dev-up
```

The command creates a CloudNativePG cluster, deploys pgConsole with its optional
DBA capabilities, seeds one access request, and forwards it to
[http://localhost:3000](http://localhost:3000). Add the proxy headers shown by
the command with a browser extension. Tear the environment down with:

```bash
kind delete cluster --name pgc-dev
```

## 📦 Run it as a container

Tagged releases publish multi-architecture images to GitHub Container Registry
with SBOM and provenance attestations:

```bash
docker pull ghcr.io/fyannk/pgconsole:<version>
```

For Kubernetes, adapt the hardened
[`deploy/kubernetes-example.yaml`](deploy/kubernetes-example.yaml) manifest.
The pgToolBox operator normally owns the `PgConsole` resource, authentication
proxy, exposure, default-deny NetworkPolicy, and exact namespaced RBAC.

> [!WARNING]
> pgConsole intentionally provides no authentication or TLS. Its forwarded
> identity and authorization-level headers are trustworthy only behind the
> operator-managed proxy and network boundary. Never expose port 3000 directly.
>
> Deploying standalone means you own that boundary. Your proxy must **set**
> `X-Forwarded-User` and `X-PgToolBox-Level` from its own authentication
> result and **drop** any copy the client sent — most proxies forward
> unrecognized request headers verbatim, so a header the proxy does not
> explicitly overwrite is one an authenticated `view` user can set to `dba`
> from their own browser. See
> [Running behind a proxy](web/docs/guides/running-behind-a-proxy.md).

## 📌 Project status

pgConsole is **pre-1.0**. The capabilities above are implemented and covered
by the test suites, but 0.x releases may change environment variables, route
paths, and the forwarded-header contract without a deprecation period. Pin an
exact image tag and read the release notes before upgrading. Security fixes go
to the most recent 0.x minor only — see [`SECURITY.md`](SECURITY.md).

## 📚 Documentation

The details live in the **[documentation site](https://fyannk.github.io/pgConsole/)**:

- [Overview](web/docs/overview/index.md)
- [Installation](web/docs/deploy/installation.md)
- [Configuration reference](web/docs/reference/configuration.md)
- [Authorization model](web/docs/architecture/authorization.md)
- [Day-2 operations](web/docs/guides/day-2-operations.md)
- [Repository evidence](web/docs/architecture/repository-evidence.md)
- [Troubleshooting](web/docs/deploy/troubleshooting.md)

## 🤝 Contributing

Bug reports, Kubernetes edge cases, security tests, documentation fixes, and
pull requests are welcome. Start with:

```bash
make test       # fast, hermetic unit suite
make check      # complete non-Docker verification
make docs       # type-check and build the documentation site
```

Docker-backed integration, scale, restricted-runtime, multiarch, and pinned
CloudNativePG checks are described in [`CONTRIBUTING.md`](CONTRIBUTING.md).
The Go code and tests are the source of truth; the site explains their behavior.

## 📄 License

pgConsole is available under the [Apache License 2.0](LICENSE).
