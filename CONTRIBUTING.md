# Contributing

Thanks for considering a contribution. This page explains how to build the
project, what the checks expect, and the invariants every change must keep.

## Development environment

- Go 1.26+ (the module pins the toolchain in [`go.mod`](go.mod)).
- `make`, `docker` (or `podman`).
- For the Docusaurus site: Node ≥ 20 and `npm` (CI uses Node 22).

```bash
make build            # static binary in bin/pgconsole
make clean            # remove local build, test, and site outputs
make test             # fast, hermetic unit tests
make test-race        # the unit suite under the race detector
make lint             # gofmt check, go vet, golangci-lint
make check            # complete non-Docker verification
make docs             # install, typecheck, and build the site
make docker-build     # the distroless, non-root image
make package          # Linux amd64/arm64 binaries and checksums
make release-check    # complete local release validation
```

Before you consider a change done:

```bash
go build ./... && go vet ./... && go test ./... -race -count=1
```

must pass, and so must `make lint` and the boundary scans in
[`hack/`](hack) (`check-readonly.sh`, `check-deps.sh`,
`check-boilerplate.sh`).

## Repository invariants

These are hard rules — a change that violates one is a bug, and several are
enforced by scans and tests:

1. **Read-only unless explicitly enabled.** Mutation-shaped calls
   (`Create`/`Update`/`Patch`/`Delete`/`Apply`) may exist **only** in
   `internal/ops` and the `internal/kube/ops.go` transport;
   `hack/check-readonly.sh` fails otherwise. In read-only mode the assembly
   graph contains no writer at all.
2. **No SQL, no object storage, no Secrets.** pgConsole speaks only to the
   Kubernetes API. The example Roles grant nothing on `secrets`, ever. This
   boundary is enforced in the dependency graph, not just at runtime: the
   only expected third-party modules are `client-go` (plus its transitive
   core), `cloudnative-pg/api`, and the viewer's evidence-types module.
   Provider SDKs (AWS/Azure/GCS), SQL drivers, and repository parsers are
   forbidden in the module graph and `hack/check-deps.sh` (run by
   `make check`) fails if one appears. Any other dependency needs a written
   justification, a licence check, and an SBOM entry.
3. **The console decides no authorization of its own.** Route admission is
   the trusted proxy's asserted `X-PgToolBox-Level`; there is no
   SubjectAccessReview and no cluster-scoped grant. A missing, empty, or
   unknown level reaches only the read-only baseline.
4. **Preserve uncertainty.** No layer may turn a broken watch, a forbidden
   response, or missing data into a healthy, current cluster: honest values
   are `unknown` and `stale`.
5. **Snapshots are the rendering path.** HTTP handlers render from
   snapshots; the only request-time API calls are the closed exception list
   (log tail, readiness, operations, and the access-review decision write).
6. **Redaction at the boundary.** Request URLs, header values, and injected
   tokens never appear in HTTP bodies, headers, logs, metrics, or rendered
   pages. Errors carry a category, not a cause.
7. **All rendering goes through `html/template`.** No `template.HTML` from
   external data; condition and event messages are length-bounded.
8. **License boilerplate** (`hack/boilerplate.go.txt`) on every new Go file;
   `hack/check-boilerplate.sh` enforces it.

Planning vocabulary (slices, milestones, gates) lives in the docs and in
issues — never in source code or comments.

## Code style

Enforced mechanically by `make lint` (`.golangci.yml`), not by review taste:

- **Linters** (`disable-all`, then explicitly enabled): `govet`,
  `staticcheck`, `errcheck`, `ineffassign`, `unused`, `revive`, `gosec`,
  `godot`, `godox` (no `TODO`/`FIXME` left in the tree), `copyloopvar`,
  `errorlint`, `bodyclose`, `contextcheck`, `noctx`. `gofmt`/`goimports`
  are mandatory; a `//nolint` needs a same-line reason and counts as debt.
- **Time is injected.** There is no naked `time.Now()` outside the clock
  implementation; anything computing an age, a staleness, or a timeout takes
  a clock. That is what makes those behaviors testable.
- **The environment is read in one place.** Only `internal/config`
  interprets environment variables — through a lookup function injected from
  `main` — and every other package receives typed, validated values.

## Layout

- `cmd/pgconsole/` — the binary's `main`.
- `internal/config/` — the environment configuration contract.
- `internal/kube/` — the only package that imports client-go; reads, the
  narrow writer transport, and error categorization at the boundary.
- `internal/observe/` — bounded, stale-retaining collectors, stores, and
  immutable snapshots.
- `internal/authz/` — the proxy-level → capability-tier mapping.
- `internal/identity/` — the forwarded-identity extractor.
- `internal/ops/` — the day-2 operation origin, CSRF, and audit.
- `internal/review/` — the access-request decision origin.
- `internal/web/` — routing, view models, `html/template`, security
  headers.
- `internal/evidence/`, `internal/redact/`, `internal/application/` —
  the evidence consumer, redaction, and process assembly.
- `deploy/` — example manifests mirroring what the operator generates.
- `web/` — the Docusaurus documentation site (the human-readable
  explanation of what the code does; the code is the source of truth).

## Tests and checks

New behavior needs table-driven tests covering the successful, empty,
forbidden, not-found, timeout, watch-broken, stale, and canceled cases,
with deterministic output (stable sorting, UTC timestamps from an injected
clock, explicit `unknown`). `make test` covers those behavior and security
cases hermetically; `make test-race` repeats them under the race detector.
Distinct environments keep distinct targets: `test-integration` for envtest,
`test-scale` for bounded-resource cases, `test-container` for the restricted
runtime, `test-multiarch` for both release architectures, and `test-e2e` for
the pinned kind + CloudNativePG journey.

## Documentation

User-facing behavior changes must come with doc updates in `web/docs/`.
Build the site locally:

```bash
cd web
npm ci
npm start
```

The site is built in CI (typecheck + build) on every pull request and
published to GitHub Pages on every push to `main`.

## Releases

Releases start from an annotated `vMAJOR.MINOR.PATCH` tag. CI validates the
exact tag commit first; only then may the separate privileged workflow push
the multi-architecture GHCR image, create provenance attestations, and
publish the binaries and supply-chain reports. The same artifacts can be
built locally with `make package supply-chain`.

## Git workflow

- One focused change per commit; keep tests and docs with their source
  change.
- Branch off `main`; do not commit generated build artifacts.
- End commit messages with the project's `Co-Authored-By` trailer where
  applicable.
