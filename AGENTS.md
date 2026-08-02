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

The console is feature-complete for its scope: an immutable,
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
| ObjectStoreViewer | structural questions about the backup repository in object storage | `ObjectStoreViewer` (reserved) |
| pgConsole (this repo) | operator-level questions about one CloudNativePG cluster | `PgConsole` (reserved) |

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
   ingress to that proxy. A missing, empty, or unrecognized level reaches
   only the baseline; `ALLOW_OPERATIONS=false` removes the write surface
   regardless of any asserted level. Two audiences needing different
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

## Hard rules (violating any of these is a bug)

1. **Read-only by default.** With `ALLOW_OPERATIONS=false` the application
   issues only `get`, `list`, `watch` and bounded `pods/log` reads. The
   deployed Role independently denies every mutating verb; that RBAC policy
   is the final enforcement boundary, not application logic.
2. **The mutation surface is enumerated, or it does not exist.** Every
   day-2 operation maps to an exact verb, resource and — where the verb
   supports it — a `resourceNames`-pinned rule. No generic "apply YAML", no
   `Cluster` spec editing, no free-form patch path. In read-only mode the
   assembly graph constructs no writer and registers no route at all.
3. **No Secret access at all.** The Role grants nothing on `secrets`, and
   nothing displayed may require secret material.
4. **One cluster, one namespace, one trust domain.** The target cluster name
   and namespace are fixed configuration. Every list/watch is
   namespace-scoped and filtered to that cluster's objects; the single
   exception is the opt-in `clusterimagecatalogs` **get** described above,
   which is a get precisely so that nothing cluster-wide is ever
   enumerated. Note that RBAC
   cannot pin `list`/`watch` by `resourceNames`, so the namespace boundary
   plus application-side selection is the honest scope for listing — and pod
   labels are a *selection* mechanism, never a security boundary.
5. **Redact at the client boundary.** ServiceAccount tokens, `Authorization`
   headers and raw client-go errors (which may embed request URLs) are
   redacted before any log, metric or response.
6. **Bounded log exposure.** Instance logs can contain query text. Tails are
   bounded in lines and bytes, fetched on demand, never persisted, and
   disabled entirely with `ALLOW_LOGS=false`.
7. **No third-party content.** No embedded iframes, external scripts, fonts,
   styles or telemetry. Monitoring depth is a link-out to an
   operator-configured URL; assets are embedded or served locally.
8. **Attribute every claim.** The UI distinguishes operator-reported,
   Kubernetes-reported and application-derived state, and never blends the
   vocabularies. In particular, an operator's backup claim is never
   presented as repository evidence — that is ObjectStoreViewer's word.

   **One carve-out: the overview summary.** The plain-language block that
   opens the console page may speak across the three vocabularies, because
   an operator arriving at an incident needs one sentence before they need
   three taxonomies. It earns that only under all four of these, and a
   reviewer should reject it if any is missing:

   1. **Derived, never sourced.** `buildSummary` takes the assembled
      `Page` and nothing else. It cannot reach a snapshot, so it has no
      way to state a fact the attributed sections below do not already
      carry. `TestSummaryRestatesOnlyWhatThePageCarries` holds this.
   2. **Paraphrase anchored to quotation.** The headline may paraphrase;
      the sub-line quotes the underlying claim verbatim beneath it.
   3. **Every card names one origin.** The blend exists at the level of
      the block, never inside a single card.
   4. **No claim the sources do not make.** The summary restates; it does
      not conclude. It never asserts recoverability — that a backup can
      be restored is not something either the operator or the repository
      scan reports, so the console does not say it.

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

The read side is `Snapshot` for the metadata timeline and `Revision`
for one manifest; the rendering screen arrives with the design work and
is not wired yet.

## Engineering conventions

Match the surrounding code. `CONTRIBUTING.md` is the human-readable guide;
the enforced rules live in `.golangci.yml`, the `hack/check-*.sh` boundary
scans, and the tests. In short:

- Go 1.26, standard-library `net/http`, server-rendered `html/template`,
  embedded local assets (`embed.FS`, nothing fetched at runtime), one static
  binary, Apache-2.0. Godoc on every exported identifier (lint-enforced;
  JSDoc for embedded JS), wrapped and categorized errors, injected clocks
  (no naked `time.Now()` outside the clock), owned goroutines, hermetic
  tests with negative security cases, `os.Getenv` confined to `internal/config`,
  and the closed dependency set enforced by `hack/check-deps.sh`.
- `k8s.io/client-go` with typed informers behind a narrow, consumer-owned
  interface; CloudNativePG types from the upstream `cloudnative-pg/api`
  module. Do not mirror whole client-go clients in interfaces or mocks.
- Package boundaries: snapshot-first rendering, with a closed set of
  request-time exceptions (log tail, readiness, day-2 operation write,
  access-review decision write); only `internal/kube` imports client-go;
  `internal/ops` is the sole origin of mutations with `internal/kube` as the
  transport; `internal/evidence` imports only the viewer's types module;
  attribution is a type.
- Hermetic unit tests (fake clients, injected clocks); integration tests via
  `envtest` or a pinned kind cluster with a pinned CloudNativePG version;
  e2e checks covering watch-break, forbidden-RBAC, operations-disabled and
  redaction negative cases.
- Makefile targets `build`, `test`, `test-race`, `test-integration`,
  `test-scale`, `test-container`, `test-multiarch`, `test-ui`, `test-e2e`,
  `lint`, `check`, `docker-build`, `supply-chain`, and `release-check`.
- The console UI is server-rendered `html/template` plus one embedded
  stylesheet and an embedded progressive-enhancement layer (the Alpine
  CSP build, vendored — never the standard build, which would require
  `script-src 'unsafe-eval'`). Content is never gated behind scripting:
  the document is complete before any script runs, enhancement-only
  controls carry `x-cloak`, and no state word is ever replaced by a
  colour or a mark. `make test-ui` enforces all of that in a browser.

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

Both, sequenced: a declared `PgConsole` resource (`pgtoolbox.fyannk.dev/v1alpha1`)
for the MVP, CNPG-I enrollment afterwards. Enrollment does not replace the
resource, it *generates* one, so the declared path is the foundation rather
than a throwaway step. The operator owns that CRD and its webhook; this repo
builds the application it deploys.

Four constraints this puts on the resource's API, all cheap now and
expensive later:

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
