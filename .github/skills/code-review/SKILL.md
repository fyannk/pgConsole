---
name: code-review
description: Review pgConsole changes against the repository's hard invariants — the read-only boundary, the dependency boundary, proxy-asserted authorization, preserved uncertainty, redaction, bounded log exposure, and the enumerated mutation surface. Use this when reviewing any pull request in this repository.
license: Apache-2.0
---

pgConsole is a read-only Kubernetes console for CloudNativePG clusters. It
runs with in-cluster ServiceAccount credentials against production
databases, so most of what matters in a review here is not style — it is
whether a change quietly widens a boundary the architecture depends on.

`make lint` already enforces style (`gofmt`, `staticcheck`, `errcheck`,
`gosec`, `revive`, `godox`, and the rest of `.golangci.yml`). Do not spend
review on what the linter already fails. Spend it on the invariants below,
which no linter can see.

## The canonical source

The hard rules live in [`CONTRIBUTING.md`](../../../CONTRIBUTING.md) under
"Repository invariants", numbered 1–12.
[`AGENTS.md`](../../../AGENTS.md) adds product context and refers to them by
number without restating them. **Read the invariant in `CONTRIBUTING.md`
before asserting a change violates it.** An earlier duplicated copy of rule
3 in this repository contradicted the code for several releases; that is
the failure mode this rule exists to prevent. Quote the rule number.

## What to look for

### 1. The read-only boundary

Mutation-shaped calls — `Create`, `Update`, `Patch`, `Delete`, `Apply` —
may appear **only** in `internal/ops/` and the `internal/kube/ops.go`
transport. `hack/check-readonly.sh` enforces this, so a violation usually
means the scan was weakened rather than that a call slipped through. Treat
any edit to `hack/check-readonly.sh` that narrows its search as a change to
the boundary itself, and say so.

### 2. The dependency boundary

The only expected third-party modules are `client-go` and its transitive
core, `cloudnative-pg/api`, and the viewer's evidence-types module. Cloud
provider SDKs, SQL drivers, and repository parsers are forbidden in the
module graph — `hack/check-deps.sh` fails on them. **Any other new
dependency needs a written justification, a licence check, and an SBOM
entry**; a `go.mod` addition arriving with none of those is a finding even
when the module is harmless, because the process is the control.

Tool dependencies share the module graph with build dependencies. A change
that adds or bumps a tool can move a version that ships in the binary —
check whether `go.sum` movement is confined to tooling or reaches
`go list -deps ./...`.

### 3. Authorization is asserted, never decided

Route admission comes from the trusted proxy's `X-PgToolBox-Level` header.
There is no SubjectAccessReview and no cluster-scoped grant. A missing,
empty, or unknown level must reach **nothing** — every content route is
gated and there is no ungated baseline. If a change introduces a default,
a fallback tier, or a route registered outside the gate, that is a
security change, not a convenience. The fail-safe direction is closed.

### 4. Uncertainty is preserved

No layer may turn a broken watch, a forbidden response, or missing data
into a healthy, current cluster. The honest values are `unknown` and
`stale`. Look specifically for:

- an error path that falls back to a zero value which renders as healthy;
- a `stale` flag dropped while mapping a snapshot to a view model;
- a count presented as a total when the source could only supply a floor.

This is the invariant most often broken by accident, because the broken
version looks tidier.

### 5. Redaction at the boundary

Request URLs, header values, and injected tokens never appear in HTTP
bodies, headers, logs, metrics, or rendered pages. Errors carry a
category, not a cause. A new `%w`/`%v` of an error that wraps a URL, or a
new metric label carrying user-supplied text, is a finding.

### 6. Bounded log exposure

Instance logs can contain query text, and for PostgreSQL that can include
statements and their literal values. Every path reading them is bounded in
bytes and disappears under `ALLOW_LOGS=false`. There are exactly three
(on-demand tail, continuous matcher, line buffer) with increasing
retention. A fourth path, an unbounded read, or retention that grows with
log volume is a finding. Nothing writes log text to disk.

### 7. The mutation surface is enumerated

Every day-2 operation maps to an exact verb, resource, and — where the
verb supports it — a `resourceNames`-pinned rule. No generic "apply YAML",
no `Cluster` spec editing, no free-form patch path. A new operation must
arrive with its Role grant, and the grant must be as narrow as the verb
allows.

### 8. Rendering

All rendering goes through `html/template`. No `template.HTML` from
external data. Condition and event messages are length-bounded. No
embedded iframes, external scripts, fonts, styles, or telemetry — every
asset is served from the binary. A new vendored browser asset needs a
[`third_party/`](../../../third_party) entry with a pinned SHA-256 and
licence, a matching pinning test, and a [`NOTICE`](../../../NOTICE) line.

### 9. Injected time and single-point configuration

There is no naked `time.Now()` outside the clock implementation; anything
computing an age, a staleness, or a timeout takes a clock — that is what
makes the behavior testable. Only `internal/config` interprets environment
variables, through a lookup injected from `main`; every other package
receives typed, validated values. A new `os.Getenv` outside
`internal/config` is a finding.

### 10. Attribution of claims

Operator-reported, Kubernetes-reported, and application-derived state stay
distinct and the vocabularies never blend. An operator's backup claim is
never presented as repository evidence. The single carve-out is the
overview summary, with four conditions described in `AGENTS.md`.

## Documentation is not specification

The code and tests are the source of truth; `web/docs/` explains their
behavior. When prose and code disagree, **the code is right and the prose
is the defect** — do not report a code change as wrong because a README
badge, a CONTRIBUTING line, or a doc page says otherwise. Report the stale
prose instead.

Apply the same standard to prose *about* configuration. If a comment or
document paraphrases a condition, a filename, or a flag, check the exact
string and ask for the exact string, so a reader can grep for it. A
paraphrase that cannot be verified is the same class of defect as a stale
one.

## Tests

New behavior needs table-driven tests covering the successful, empty,
forbidden, not-found, timeout, watch-broken, stale, and canceled cases,
with deterministic output — stable sorting, UTC timestamps from the
injected clock, explicit `unknown`. A change touching
`internal/web/static/` or `internal/web/templates/` needs `make test-ui`,
which asserts what a rendered string cannot: that the enhancement layer
runs under the served CSP, that contrast clears WCAG 2 AA in both schemes,
that the page stays honest with scripting disabled, and that tables
survive a 375px viewport.

A pull request that adds an error path without a test for it is
incomplete, because rule 4 lives or dies in exactly those paths.

## How to report

Lead with the invariant number and what the change does to it. Prefer one
well-evidenced finding over several speculative ones — quote the line and
say what input reaches it. If a change looks wrong but the reasoning
depends on a file you have not read, read it or say the finding is
uncertain. State plainly when a change is clean; "no findings" is a useful
review here.
