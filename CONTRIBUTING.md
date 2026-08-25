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

- Go 1.26.6+ (the floor and the toolchain both live in
  [`go.mod`](go.mod); see the note there for why the floor is not the
  1.26.4 the module graph derives).
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
`check-boilerplate.sh`, `check-go-version.sh`).

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
   SubjectAccessReview. A missing, empty, or unknown level reaches
   **nothing** — every content route is gated and there is no ungated
   baseline. The fail-safe direction is closed. The Role is namespaced in
   every mode but one: `ALLOW_CLUSTER_CATALOGS=true` plus
   [`deploy/cluster-catalog-role.yaml`](deploy/cluster-catalog-role.yaml)
   grants `get` — never `list`, never `watch` — on `clusterimagecatalogs`,
   and `hack/check-readonly.sh` enforces that no other manifest carries a
   `ClusterRole`. Any further cluster-scoped authority is a non-goal;
   [`AGENTS.md`](AGENTS.md) describes the exception in full.
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

The ruleset on `main` requires every job
[`ci.yml`](.github/workflows/ci.yml) runs on a pull request — the
release job is push-only and is not among them — plus both CodeQL
analyses and the code-scanning result, and requires that review threads
be resolved. It requires no approvals: the gate is the pipeline and the
reading, not a rubber stamp. Copilot is requested on every pull request
automatically; its review is always a comment, never an approval, so it
cannot approve a change — but an unresolved thread it opens does hold
the merge until someone answers it.

Branches need not be up to date with `main` to merge. Requiring that
would catch the case where two pull requests pass alone and break
together, but nothing rebases a stale branch on its own: auto-merge only
waits and merges, and Dependabot refreshes a branch when its manifest
conflicts, not when `main` moves. The requirement would therefore trade
a rare class of conflict for a queue that stalls on every merge.
`ci.yml` runs on pushes to `main` as well — but an auto-merge armed with
`GITHUB_TOKEN` lands as a push made by that token, and such a push starts
no workflow run, so the merges no human watched are the ones the push
trigger misses. `ci.yml` therefore also runs on a daily schedule, which
is what surfaces a pair of changes that passed apart and break
together.

Dependabot's patch and minor bumps queue themselves through
[`automerge.yml`](.github/workflows/automerge.yml) and land the moment
the required checks go green. Majors are left for a person.

It merges on an allowlist — `version-update:semver-patch` or
`version-update:semver-minor`, named in full as
`dependabot/fetch-metadata` reports them — and not on "anything that is
not a major". An update type this workflow has not seen, or an empty one,
closes the gate rather than arming a merge nobody chose.

That workflow merges with `AUTOMERGE_TOKEN`, a fine-grained token with
Contents and Pull requests set to read and write on this repository. It
has to be registered **twice, in two different stores**, because the two
workflows that use it are triggered differently: `automerge.yml` runs on
`pull_request` and a Dependabot-triggered run sees only **Dependabot**
secrets, while `tool-pins.yml` runs on a schedule and needs the same
token as an ordinary **Actions** secret. It is not decoration: a push made with the
workflow's own `GITHUB_TOKEN` starts no workflow run, so a bump merged
that way would land on `main` with the pipeline never running against
the merged result — including the `release-artifacts` job, which no pull
request reaches because it carries `if: github.event_name == 'push'`.
Merging under a token that belongs to a person makes the merge an
ordinary push. If the secret is missing or expired the workflow fails
loudly and the bump waits for a human, rather than merging unobserved.

The Go version lives in `go.mod`. CI reads it with setup-go's
`go-version-file`, so a toolchain bump is one line rather than one per
workflow. The builder image in the `Dockerfile` has to carry the version
too — an image tag cannot be read from a module file — and Dependabot
moves that tag on its own schedule while nothing moves `go.mod` with it.
[`hack/check-go-version.sh`](hack/check-go-version.sh) fails the build
when the two disagree, because a release whose binaries and container
were built by different toolchains is not a release the attestations can
describe as one build.

The linter version lives in the `Makefile`, which is what a contributor
runs locally; CI reads it from there rather than carrying its own copy.
It moves with the toolchain — a Go release is not lintable until
staticcheck understands its syntax, so bumping one without the other
fails the build rather than skipping the analysis.

Both that pin and `GOVULNCHECK_VERSION` are invisible to Dependabot,
which reads manifests — `go.mod`, the lockfiles, the `Dockerfile`,
action refs — and not `Makefile` variables.
[`hack/check-tool-pins.sh`](hack/check-tool-pins.sh) compares them
against the module proxy, and
[`tool-pins.yml`](.github/workflows/tool-pins.yml) runs it weekly and
opens a pull request when one is behind. It proposes only; the required
checks decide, which is the point — a linter bump can fail the build and
a scanner bump can report something new.

The script is deliberately not in `make check`. It needs the network,
and it would turn CI red the day upstream tags a release, which has
nothing to do with the change under test. `ENVTEST_K8S_VERSION`,
`SETUP_ENVTEST_VERSION`, and the Node pins in `ci.yml` stay
hand-maintained: envtest tracks what assets exist rather than a
module's tags, and the Node pin names an LTS line on purpose. The
script says so in a comment, so the omission reads as a decision.

[`scorecard.yml`](.github/workflows/scorecard.yml) runs OpenSSF
Scorecard weekly, on pushes to `main`, and whenever a ruleset changes —
that last trigger is the point, because a weakened branch protection
should be visible at once rather than at the next scheduled run. It
audits what this repository does to itself: pinned actions, token
permissions, release signing, dangerous workflow patterns. Findings land
in code scanning beside the CodeQL ones.

Every `actions/checkout` in this repository sets `persist-credentials:
false` except the one in `tool-pins.yml`, which pushes the branch it
proposes. The default leaves the job's token in `.git/config` for every
later step to reach; nothing else here pushes, so nothing else needs it.
`automerge.yml` runs on `pull_request` rather than `pull_request_target`
for the same reason — it does not need the base repository's context, so
it does not ask for it, and cannot be turned into a credential leak by a
later change that adds a checkout.

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
