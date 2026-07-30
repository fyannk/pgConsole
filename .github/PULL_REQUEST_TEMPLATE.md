<!--
Thanks for contributing. Keep one focused change per PR, with tests and docs
alongside their source change. The code and tests are the source of truth;
the docs site explains their behavior.
-->

## What and why

<!-- What does this change, and what problem does it solve? -->

## How it was verified

<!-- Commands you ran, and behavior you confirmed. -->

- [ ] `make check` passes (build, tests, race, lint, boundary scans, vuln)
- [ ] `make docs` passes if the site changed
- [ ] Docker-backed checks (`test-integration` / `test-container` / `test-e2e`)
      run if relevant to the change

## Invariants

<!-- pgConsole's boundaries are structural. Confirm this change keeps them. -->

- [ ] Read-only unless a capability flag is set; mutations stay in
      `internal/ops` + `internal/kube/ops.go` (`hack/check-readonly.sh`)
- [ ] No SQL, object storage, or Secret access; no forbidden module enters the
      dependency graph (`hack/check-deps.sh`)
- [ ] Uncertainty preserved — broken/forbidden/missing renders `unknown` or
      `stale`, never healthy-and-current
- [ ] Redaction intact — no URL, header value, or token in bodies, logs, or
      metrics
- [ ] User-facing behavior changes update `web/docs/`
