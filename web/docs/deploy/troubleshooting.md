---
sidebar_position: 3
title: Troubleshooting
---

# Troubleshooting

pgConsole fails honestly: a panel it cannot observe renders `unknown`, a
section that lost contact renders **stale** with its last-good data, and a
route a level does not admit renders a constant "not authorized" page. Read
the panel state first; the fix follows from it.

```bash
kubectl -n <ns> logs deploy/pgconsole-<name>
kubectl -n <ns> describe role pgconsole-<name>-read
kubectl -n <ns> auth can-i --as=system:serviceaccount:<ns>:pgconsole-<name> \
  get clusters.postgresql.cnpg.io
```

Logs are structured and redacted — they carry an error **category**, never
a request URL, header value, or injected token. Because the console's entire
authority is namespaced Roles on its own ServiceAccount — with no
impersonation, and the single opt-in `ClusterRole` in
`deploy/cluster-catalog-role.yaml` the only cluster-scoped grant —
`kubectl auth can-i --as` that ServiceAccount reproduces exactly what the
console can and cannot do.

## Panels and sections

| Symptom | Meaning | Fix |
|---|---|---|
| A panel reads `unknown` | No observation — a missing optional permission, an absent CRD, or no snapshot yet. | Grant the optional rule (for example `objectstores`), or accept it: `unknown` is honest. |
| A section reads **stale** | The collector lost API contact and retains the last-good snapshot. | Check API reachability and the Role's `watch` verbs; it reconnects with bounded backoff. |
| `/readyz` returns 503 forever | The readiness probe cannot reach the API (or a required evidence sidecar). | Check egress to the API server and the evidence socket. `/healthz` stays green — the process is alive, not ready. |
| Backups panel empty | Nothing selects the target cluster, or the list/watch grant is missing. | Confirm the resources exist and the Role grants them. |
| Events missing for a pod | Pod-kind events render only for membership-verified pods; without a pods snapshot they are withheld, never guessed. | Ensure the `pods` `list`/`watch` grant is present so membership can be established. |

## Authorization and headers

| Symptom | Meaning | Fix |
|---|---|---|
| Every page: "identity required" | No usable `X-Forwarded-User` reached the console. | Fix the proxy path; confirm the NetworkPolicy admits only the proxy. |
| A real user gets 403 on operations / review | The asserted `X-PgToolBox-Level` is below the required level, or unset. | Have the proxy assert `poweruser` (logs) or `dba` (operations, review); confirm `TRUSTED_LEVEL_HEADER`. |
| `/operations` or `/access-requests` is 404 | The capability is disabled — the route does not exist. | Set the flag **and** apply the matching Role. |
| Log tail is 403 for a `view` user | The tail requires `poweruser`; its affordance is hidden below. | Expected; grant a higher level. |
| A POST fails "confirmation expired or invalid" | The CSRF token aged out (10 min) or the request was cross-origin. | Reload the confirmation page for a fresh token. |

## Operations and review

| Symptom | Meaning | Fix |
|---|---|---|
| An operation returns 502 | The pinned CNPG interaction was rejected by the API — usually RBAC. | Apply the operations Role, pinned to the cluster by `resourceNames`. |
| An operation reports success but the cluster looks unchanged | Operations are fire-and-observe: the write is recorded, and progress appears on the status page as the operator reports it — not on the result page. | Return to the console and watch the cluster section. |
| The approval picker lists no roles | No `PgToolBoxRole` is visible, or the review Role lacks `get`/`list`/`watch` on `pgtoolboxroles`. | Apply `deploy/access-review-role.yaml` and confirm roles exist. |
| An approval returns 400 "not one of the offered options" | The submitted role was not an observed `PgToolBoxRole`. | Reload the panel and pick a listed role. |
| A recorded decision does not show in the list | The status write is fire-and-observe; the panel reflects it once the informer catches up. | Wait and reload. |

## Repository evidence

| Symptom | Meaning | Fix |
|---|---|---|
| The process refuses to start naming a `REPOSITORY_*` variable | The four evidence variables validate all-or-nothing — socket, token file, expected fingerprint, Barman server. | Set all four, or unset all four; the evidence consumer is off with none set. |
| The evidence panel is **stale** or absent | The sidecar is unreachable over its Unix socket, or the mounted token or expected fingerprint no longer matches what the sidecar presents. | Check the sidecar container and the mounted token file; a mismatch is refused, not rendered, and the last-good report is retained stale. |

## Nothing reaches the console

The default-deny NetworkPolicy admits only the proxy port; bypassing the
proxy is blocked **by design** — that block is the trust boundary. Confirm
your ingress exception selects the proxy and your egress reaches the API
server.
