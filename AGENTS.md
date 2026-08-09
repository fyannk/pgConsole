# AGENTS.md

Instructions for AI coding agents working in this repository. These rules
apply to the entire repository unless a more specific `AGENTS.md` exists
below the path being changed.

## The code is the truth

This repository has no separate specification to keep in sync. The Go code,
its tests, and the `make check` / `make test-*` targets are the authoritative statement of
what pgConsole does and must do. The Docusaurus site under `web/` explains
that behaviour to humans in a readable way — it is documentation, not a
contract. When the site and the code disagree, **the code is right and the
site is the bug to fix**.

Concretely:

- Do not treat any prose as an authority that overrides the code. There is
  no brief, no delivery plan, and no decided-record set to consult or amend
  — those specs were removed once the code existed to speak for itself.
- A behaviour change is done when the code, its tests, and any affected
  `make test-*` target agree. If the change alters something the `web/`
  site describes, update the site in the same change so the explanation
  stays honest — but the site follows the code, never the reverse.
- When you need to know how something actually behaves, read the code and
  its tests first, not the documentation.

## What this repository is

pgConsole: a per-cluster operational console for **one** CloudNativePG
`Cluster`. A Go web application that renders what the CloudNativePG
operator and the Kubernetes API report about that cluster — conditions,
instance roles, pod state, events, backup resources, a bounded log tail —
so that platform teams and application owners do not need `kubectl`.

It is an observer of reported state, not a verifier. It renders claims and
attributes them to their source; it never asserts that replication is
healthy, that data is intact, or that a backup would restore.

v0.3.0 is released, and the console is feature-complete for its stated
scope — but it is **pre-1.0**, so environment variables, route paths, and
the forwarded-header contract may change within 0.x without a deprecation
period. "Feature-complete" describes the surface, not a stability promise.
What that surface covers: an immutable,
generation-carrying snapshot from a pinned get plus a name-scoped watch (no
list verb, proven against real RBAC in envtest); operator-reported status,
conditions, and staleness labels; instance pods observed through label
selection plus controller-ownership verification, with primary-disagreement
findings carrying both origins; namespace-wide events filtered to the
cluster's candidates at the boundary, membership-checked at rendering, and
age-windowed; the exact-cluster `Backup`/`ScheduledBackup` catalog in its
own bounded, stale-retaining snapshot; a bounded on-demand log tail that
re-verifies membership live before each fetch and disappears entirely under
`ALLOW_LOGS=false`; per-user route admission from the proxy's asserted
level; the four enumerated day-2 operations (backup, reload, restart,
promote) behind `ALLOW_OPERATIONS=true`, confirmation, CSRF, audit, and
RBAC; the dba access-request review panel behind `ALLOW_ACCESS_REVIEW=true`;
and the optional repository-evidence consumer that reads the
ObjectStoreViewer sidecar over a pod-private socket behind an
all-or-nothing four-variable contract. All of this is proven against the
pinned tuple (CloudNativePG 1.30.0 on Kubernetes 1.34) by `make test-e2e`.

## Family context — read before designing anything

This application does not stand alone. It is the third tool of
**pgtoolbox**, a Kubernetes/OpenShift operator that deploys a family of
PostgreSQL tools, each built in its own repository:

| Application | Answers | Kind |
|---|---|---|
| pgAdmin | SQL-level questions inside the database | `PgAdmin` (shipped) |
| pgObjectStoreViewer | structural questions about the backup repository in object storage | `ObjectStoreViewer` (reserved) |
| pgConsole (this repo) | operator-level questions about one CloudNativePG cluster | `PgConsole` (reserved) |

"Reserved" describes the *kind*, not the application: pgObjectStoreViewer
ships (its `api` module is a dependency of this repo), but the operator that
would reconcile these resources is not published yet.

**pgtoolbox owns everything outside the process**: deployment, the
authentication proxy (`oauth-proxy` or OIDC), exposure
(Route/Ingress/HTTPRoute/ClusterIP), the default-deny NetworkPolicy, and the
ServiceAccount/Role/RoleBinding that are this application's entire
authority. This repository builds the application and image only, plus
example manifests mirroring what the operator generates.

Consequences that constrain every design decision here:

1. **Never implement authentication, TLS termination, or user management.**
   The proxy is the trust boundary. `TRUSTED_USER_HEADER` is display and
   audit only, and must be treated as spoofable if the deployment invariant
   is broken. It is never used for authorization.
2. **Never do a sibling's job.** ObjectStoreViewer is the only component
   that reads object storage; pgConsole never gains store credentials or
   repository parsing, and compiling the viewer into this binary is a
   permanent non-goal. Repository evidence reaches the console only through
   the loopback sidecar API consumed by `internal/evidence`, whose pod
   invariants (credential and token mount isolation, no shared PID
   namespace, no host network) are load-bearing. pgAdmin is the only
   component that speaks SQL; pgConsole never connects to PostgreSQL, never
   reads database credential Secrets, and never renders database contents.
3. **The application's authority is Kubernetes RBAC on its own
   ServiceAccount.** The application never impersonates users and never
   handles user tokens. Per-user logic is route admission only: the trusted
   proxy asserts an authorization level in `X-PgToolBox-Level` — `view`,
   `poweruser`, or `dba` — which the console maps onto an ordered ladder
   (`none < view < poweruser < dba`) to decide which routes above the
   read-only baseline a request may reach. This gating never widens what the
   ServiceAccount can do. The console performs **no capability probing of
   its own**: there is no SubjectAccessReview and nothing cached — the
   level is trustworthy only because the deployment confines the console's
   ingress to that proxy.

   **There is no ungated baseline.** Reaching the console is not
   authorization: every screen is admitted by the forwarded level, and a
   missing, empty, or unrecognized one reaches nothing but the denial
   page, the readiness endpoints and the embedded assets. The ladder is:
   `view` sees the overviews and the two metrics screens; `poweruser`
   adds every other read screen — inventories, rosters, pod detail,
   history, evidence and the bounded log tails; `dba` adds the four
   day-2 operations, the access-request review panel, and the pgAdmin
   link-out, which is a SQL console onto the data and so reaches past
   anything this console shows below that level. `ALLOW_OPERATIONS=false`
   removes the write surface regardless of any asserted level.

   A consequence worth stating: setting `TRUSTED_LEVEL_HEADER` empty does
   not open the console, it closes it — with no level to read, nothing is
   admitted, and the denial page says the deployment is at fault rather
   than the reader. Two audiences needing different
   Role-level authority means two deployments with different Roles.

   The Role is namespaced in every mode but one, and that exception is
   deliberately narrow. `ALLOW_CLUSTER_CATALOGS=true` plus the separate
   `deploy/cluster-catalog-role.yaml` grants `get` — never `list`, never
   `watch` — on `clusterimagecatalogs`, because a Cluster may draw its
   image from a cluster-scoped catalog and the console can otherwise see
   the reference but not its content. Declining the grant costs nothing:
   the panel reports that the content was not read, and never that the
   catalog is absent. `hack/check-readonly.sh` enforces that no other
   manifest contains a ClusterRole and that this one grants nothing
   beyond that get. Any further cluster-scoped authority is a non-goal.

## Hard rules

The numbered invariants live in
[`CONTRIBUTING.md`](CONTRIBUTING.md#repository-invariants) and are not
restated here. Read them before changing anything; several are enforced by
`hack/check-*.sh` and by tests, and a change that violates one is a bug.

Two of them carry consequences specific to this application's position in
the family, which the numbered list states but does not explain:

- **Rule 4 (one cluster, one namespace)** holds because RBAC cannot pin
  `list`/`watch` by `resourceNames`. The namespace boundary plus
  application-side selection is therefore the honest scope for listing, and
  pod labels are a *selection* mechanism, never a security boundary. The
  opt-in `clusterimagecatalogs` grant is a `get` precisely so nothing
  cluster-wide is ever enumerated.
- **Rule 1 (read-only unless enabled)** is enforced twice on purpose: the
  assembly graph constructs no writer, *and* the deployed Role independently
  denies every mutating verb. RBAC is the final boundary, not application
  logic — never argue that one of the two makes the other redundant.

## The overview summary carve-out

Rule 12 says the UI never blends the operator-reported,
Kubernetes-reported, and application-derived vocabularies. The
plain-language block that opens the console page is the one exception,
because an operator arriving at an incident needs one sentence before they
need three taxonomies.

It earns that only under all four of these, and a reviewer should reject it
if any is missing:

1. **Derived, never sourced.** `buildSummary` takes the assembled `Page`
   and nothing else. It cannot reach a snapshot, so it has no way to state
   a fact the attributed sections below do not already carry.
   `TestSummaryRestatesOnlyWhatThePageCarries` holds this.
2. **Paraphrase anchored to quotation.** The headline may paraphrase; the
   sub-line quotes the underlying claim verbatim beneath it.
3. **Every card names one origin.** The blend exists at the level of the
   block, never inside a single card.
4. **No claim the sources do not make.** The summary restates; it does not
   conclude. It never asserts recoverability — that a backup can be
   restored is not something either the operator or the repository scan
   reports, so the console does not say it.

## Object definition history

`internal/history` retains a bounded, in-memory revision timeline of the
watched object definitions — what changed, when it was observed, and by
which field manager. Capture rides the existing watches: `internal/kube`
taps each pump after it accepts an event and hands each complete listing
over as a seed, so membership filtering stays single-sourced and no
second watch connection exists. Its invariants:

- **In-memory and bounded; durable only by explicit mount.** Retention
  is bounded on three axes (global revisions, global manifest bytes,
  revisions per object) from validated configuration, and the in-memory
  store is always authoritative; `HISTORY_ENABLED=false` means no
  recorder is constructed and no tap wraps any pump. By default history
  lives and dies with the process — the stateless-process rule stands.
  `HISTORY_PATH` opts into a bbolt journal (`internal/history/bolt`)
  that mirrors mutations write-behind and reloads them at boot; it
  implies a PVC and a single replica, an unusable journal fails before
  listen, and a hard kill loses at most the flush interval of history,
  never the file's integrity. On boot the first seeds reconcile against
  the journal's state, so a change made while the process was down is an
  explicit after-gap revision, not a silent first observation.
- **Scrubbed at the boundary.** A stored manifest never contains managed
  fields, the resource version, the last-applied annotation, or an
  inline container environment value; env names and secret *references*
  survive. The scrub is structural, not path-based, and its totality is
  a tested negative case.
- **Kubernetes-reported, and it says so.** A revision records what the
  API server delivered and when this process observed it. Actor
  attribution is the manager name from `managedFields` — self-declared,
  never an authenticated identity — and observation times are this
  process's clock, never server timestamps.
- **Gaps are explicit.** A change or deletion discovered by a re-seed
  after contact loss is recorded as having happened inside a named
  unobserved window, never as if it happened at observation time. Only a
  complete, fully-converted listing may claim seed semantics; a
  truncated one degrades to per-item observations, which imply nothing
  about what was not seen.
- **Logs stay out.** Rule 6 is untouched: history stores object
  definitions, never log content, and Kubernetes Events are excluded —
  they are already a timeline of their own.

The read side is `Snapshot` for the metadata timeline, `Revision` for
one manifest, and `Diff` for the changed paths between a revision and
the previous retained definition of the same object. Diffs are computed
on demand and never stored; named Kubernetes lists compare by their
`name` merge key, so a reorder is not a change; the entry list and every
rendered value are bounded. Each diff entry may attribute its path to
the field manager that owned it — from `managedFields` ownership
captured (bounded) at the boundary — and attributes to nobody when no
manager, or more than one, plausibly owns the path: a guessed
attribution is worse than an absent one. A revision whose baseline was
evicted or never observed reports `HasBase` false rather than diffing
against the wrong thing. The rendering screen arrives with the design
work and is not wired yet.

## Engineering conventions

Match the surrounding code. Style, linters, layout, the build and test
targets, and the boundary scans are all described in
[`CONTRIBUTING.md`](CONTRIBUTING.md) and enforced by `.golangci.yml`, the
`hack/check-*.sh` scans, and the tests — read them there rather than here.

What follows is the design rationale a reader cannot recover from the rules
alone: why the code looks the way it does, and which of its shapes are
load-bearing rather than incidental.

- `k8s.io/client-go` with typed informers behind a narrow, consumer-owned
  interface; CloudNativePG types from the upstream `cloudnative-pg/api`
  module. Do not mirror whole client-go clients in interfaces or mocks — the
  interface exists to state what a consumer needs, and a mirrored client
  states nothing.
- Attribution is a type, not a convention. Snapshot-first rendering has a
  closed set of request-time exceptions (log tail, readiness, day-2
  operation write, access-review decision write); adding a fifth is a design
  change, not an implementation detail.
- The two wiring drawings use no layout engine: placement is a set of
  stated rules — pinned columns and rows, trunk buses in the alleys
  between dotted category frames, kind frames flowing into fixed
  columns — computed as arithmetic, deterministic by construction, so
  each drawing is safe to screenshot and cheap to test. The Overview
  serves the grouped wiring (poolers, cluster, backup schedules,
  storage); the cluster overview serves the children inventory (the
  Cluster, the objects it owns via controller owner reference, and the
  objects referencing it). Earlier engine-backed drawings — Graphviz in
  WebAssembly, a Cytoscape panel, an ELK panel — were retired with the
  fourth iteration; only their orthogonal-route renderer
  (`toporoute.go`) survives.
- The console UI is server-rendered `html/template` plus one embedded
  stylesheet and three narrowly separated, vendored enhancement layers:
  htmx 2.x owns same-origin HTML navigation and atomic screen refresh;
  the Alpine CSP build owns local component state (never the standard
  Alpine build, which would require `script-src 'unsafe-eval'`); uPlot
  draws the metrics charts from a same-origin JSON fetch of the series
  endpoint, lazily, as each panel approaches the viewport on the visible
  tab. That fetch replaced an inlined payload once the screen grew to a
  full catalog on a tab per instance: inlining put every instrument's
  whole raw window into the document several times over, nearly all of
  it for tabs nobody opened. The no-script contract is unchanged and is
  met the same way — the window summary is served as text beside every
  chart, on every tab. No layer draws the diagram of record: that
  ships finished, so a reader without scripting sees the finished
  drawing and never an empty frame. All are served from the binary,
  htmx evaluation and browser history caching are disabled, and no
  layer decides authorization or interprets cluster state. Content is
  never gated behind scripting: the document is complete before any
  script runs, enhancement-only controls carry `x-cloak`, a chart's
  window summary is stated in text beside it, and no state word is ever
  replaced by a colour or a mark. `make test-ui` enforces all of that
  in a browser.
- **Absolute times are rendered in UTC and restated, never replaced.**
  Every absolute moment the server renders goes through the `Stamp`
  view type and the `stamp` template: the UTC text is the claim, and it
  is what a reader with no script and every printed page sees. The
  RFC3339 twin beside it exists only so `console.js` may restate that
  same instant in the reader's own zone, which the top bar's toggle
  switches and persists. The rewrite is opt-in per element
  (`data-local`), because the console also renders relative ages inside
  `<time datetime>` — "4m ago" has no zone, and converting it would
  change what the cell says rather than how it is spelled.
- Every vendored browser asset is MIT-licensed and never patched:
  replacing one means taking a new upstream release whole.

## Naming — settled, use exactly these

| Context | Spelling |
|---|---|
| Product, in prose and headings | **pgConsole** |
| Kubernetes kind | **`PgConsole`**, group `pgtoolbox.fyannk.dev` |
| Repository and Go module | `pgConsole` |
| Container image and directory | `pgconsole` |

This mirrors the family: the product is `pgAdmin`, the kind is `PgAdmin`.

The kind is deliberately **not** `Console`. OpenShift already serves two
cluster-scoped `consoles` resources — `consoles.config.openshift.io` and
`consoles.operator.openshift.io` — so a third would make `kubectl get
console` ambiguous on exactly the platform pgtoolbox targets most, forcing
the fully-qualified name forever. `PgConsole` collides with nothing.

## Deployment path

The `PgConsole` CRD, its webhook, and the operator that reconciles it live
in the pgtoolbox repository, not this one. This repo builds the application
and image the operator deploys, plus example manifests mirroring what it
generates. As of v0.3.0 the operator is not published, so the standalone
manifests are the only install path.

One consequence binds this repo directly: the console's environment
contract has to stay derivable from that resource, so changing a variable's
name, default, or meaning changes the operator's contract even though the
operator lives elsewhere. Treat `internal/config` as a published interface.

The rest are design notes for the operator's API, kept here only because
this is where the family reasoning is written down. They constrain the
pgtoolbox repository, not this one — do not implement them here:

1. the spec must be derivable from a handful of string parameters — keep
   required fields to the cluster reference and default the rest in the
   mutating webhook;
2. reuse `pgtoolbox.fyannk.dev/registration-source: cnpg-i` rather than
   inventing a second convention;
3. assume same-namespace; do not design references enrollment could never
   express;
4. a `DesiredPgConsole()` builder must reproduce **every** webhook default,
   because the enrollment lock compares it against the stored spec — a
   default added in one place and forgotten in the other rejects resources
   nobody touched. Share one defaulting function between the webhook and the
   builder, with a test asserting their equivalence.
