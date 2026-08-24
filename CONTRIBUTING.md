# Contributing

Thanks for considering a contribution. This page explains how to build the
project, what the checks expect, and the invariants every change must keep.
It is the canonical statement of all three.

[`AGENTS.md`](AGENTS.md) sits beside it with the context this page does not
carry: what the product is, where it sits in the pgtoolbox family, which
non-goals are permanent, and why parts of the code are shaped the way they
are. It defers to this page for every rule. Read both before a first change;
the design rationale is what stops a locally reasonable change from
violating something the linters cannot see.

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
enforced by scans and tests.

**This list is the canonical one.** [`AGENTS.md`](AGENTS.md) adds product and
family context for AI agents but does not restate these rules; it refers to
them by number. State an invariant in one place or it drifts, and a drifted
invariant is worse than an absent one — an earlier copy of rule 3 in this
file said the opposite of the code for several releases.

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
   unknown level reaches **nothing** — every content route is gated and
   there is no ungated baseline. The fail-safe direction is closed.
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
9. **The mutation surface is enumerated, or it does not exist.** Every day-2
   operation maps to an exact verb, resource, and — where the verb supports
   it — a `resourceNames`-pinned rule. No generic "apply YAML", no `Cluster`
   spec editing, no free-form patch path.
10. **Bounded log exposure.** Instance logs can contain query text, and
    for PostgreSQL that can include statements and their literal values.
    Every path that reads them is bounded in bytes, and all of them
    disappear under `ALLOW_LOGS=false`. There are three, and they retain
    increasingly more:
    - the **on-demand tail** retains nothing: it is fetched per request,
      bounded in lines and bytes, and never cached;
    - the **continuous matcher** (`LOG_STREAM_ENABLED`) analyses each
      line once and retains only what matched — one bounded observation
      per rule per container, so its memory does not grow with log
      volume;
    - the **line buffer** (`LOG_BUFFER_BYTES`, default `0`) retains
      recent lines verbatim. This one is a standing corpus of log text
      and is therefore off unless a deployment asks for it, bounded per
      container *and* in total, and aged out.

    Nothing writes log text to disk. Following is best effort — a stream
    breaks on every container restart and Kubernetes cannot say what was
    missed — so a gap is recorded explicitly and a retained count is a
    floor, never a total.
11. **No third-party content at runtime.** No embedded iframes, external
    scripts, fonts, styles, or telemetry. Monitoring depth is a link-out to
    an operator-configured URL; every asset is served from the binary. A new
    vendored browser asset needs an entry in [`third_party/`](third_party)
    with its pinned SHA-256 and licence, a matching pinning test, and a line
    in [`NOTICE`](NOTICE) — the asset ships inside the binary, so its licence
    has to travel with it.
12. **Attribute every claim.** The UI keeps operator-reported,
    Kubernetes-reported, and application-derived state distinct and never
    blends the vocabularies. An operator's backup claim is never presented as
    repository evidence. The one carve-out — the overview summary — and the
    four conditions it must meet are described in
    [`AGENTS.md`](AGENTS.md#the-overview-summary-carve-out).

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
runtime, `test-multiarch` for both release architectures, `test-ui` for the
console in a browser, and `test-e2e` for the pinned kind + CloudNativePG
journey.

`make audit` covers the two npm trees (`web/` and `hack/uitest/`) the way
`make vuln` covers the Go one, resolving advisories from the lockfiles
without installing either. It runs through
[`hack/check-npm-audit.sh`](hack/check-npm-audit.sh), which subtracts the
advisories listed in
[`hack/npm-audit-accepted.txt`](hack/npm-audit-accepted.txt) and fails on
everything else — including a new advisory in a package that already has an
accepted entry. Prefer fixing; add an entry only when no fix exists, and say
in it what the exposure is, why it does not apply, and what would let the
entry be deleted.

Changes to `internal/web/static/` or `internal/web/templates/` need
`make test-ui`. It serves the fixture states from
`internal/web/uiharness_test.go` and drives them with headless Chromium
(`hack/uitest/drive.js`), asserting what a rendered string cannot show:
that the embedded enhancement layer runs at all under the served
Content-Security-Policy, that colour contrast clears WCAG 2 AA in both
schemes across every state, that the page stays complete and honest with
scripting disabled, and that tables survive a 375px viewport. It needs
Node and downloads a pinned Chromium on first run; it needs no cluster.
Screenshots and a summary land in `artifacts/ui/`.

## Merging

The ruleset on `main` requires every check listed in
[`ci.yml`](.github/workflows/ci.yml) plus both CodeQL analyses, and
requires that review threads be resolved. It requires no approvals: the
gate is the pipeline and the reading, not a rubber stamp. Copilot is
requested on every pull request automatically; its review is always a
comment, never an approval, so it cannot approve a change — but an
unresolved thread it opens does hold the merge until someone answers it.

Dependabot's patch and minor bumps queue themselves through
[`automerge.yml`](.github/workflows/automerge.yml) and land the moment
the required checks go green. Majors are left for a person.

The Go version lives in `go.mod` alone. CI reads it with setup-go's
`go-version-file`, so a toolchain bump is one line, not three. The
builder image in the `Dockerfile` carries its own pin, which Dependabot
keeps current.

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
- Write the commit body for someone reading `git log` in a year: say what
  changed and why it had to, not what the diff already shows.
- Commits carry no tooling attribution. `Co-Authored-By` is for a human who
  actually co-wrote the change.
