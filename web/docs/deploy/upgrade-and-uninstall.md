---
sidebar_position: 4
title: Upgrade and uninstall
---

# Upgrade and uninstall

## Compatibility policy

pgConsole is **pre-1.0**. Within 0.x there is no compatibility guarantee: a
minor release may rename or remove environment variables, change route paths,
or change the forwarded-header contract, without a deprecation period.

- Pin an **exact** image tag (`v0.6.0`), never a floating one.
- Read the [release notes](https://github.com/fyannk/pgConsole/releases) and
  the [changelog](https://github.com/fyannk/pgConsole/blob/main/CHANGELOG.md)
  before upgrading.
- Re-check the [configuration reference](../reference/configuration.md) after
  every upgrade: an environment variable the console no longer recognises is
  ignored silently, so a setting can stop taking effect without an error.
- Security fixes go to the most recent 0.x minor only; there are no backports.

## Upgrade

pgConsole is stateless by default — no database, session store, or persistent
volume. The optional `HISTORY_PATH` journal is the explicit exception. Without
it, upgrading is simply rolling the Deployment to a new image tag and the
bounded history honestly starts empty.

- **Operator-managed**: bump the image in the `PgConsole` object (or the
  operator's default image) and let it roll the pod once via its
  config-checksum annotation.
- **Standalone**: change the `image:` in the Deployment and
  `kubectl rollout restart` (or re-apply).

A release may also change the **Role**: a new read the console has
learned to make. Re-apply the Role manifests whenever the changelog
says the grants changed — the console degrades honestly without the
grant (the affected panel or check reports that it could not look,
never a wrong answer), but it stays degraded until the Role catches
up. The first example is the `resourcequotas` read: without it, the
`quota-exhausted` diagnostics check reports "could not run" on every
run.

In-flight CSRF confirmation tokens are minted from a random per-process key
and are invalidated by the restart. This is harmless: a reviewer or
operator simply reloads the confirmation page for a fresh token. A
journal-enabled deployment keeps one replica and carries its PVC across the
rollout. The journal is opened before listen; an unusable file fails the new
pod instead of silently discarding retained history.

Compatibility with CloudNativePG is pinned per release: the day-2
operations reproduce the exact `kubectl cnpg` plugin interactions for the
pinned CNPG version. A CNPG minor-version bump re-verifies those
interactions before the console claims support.

## Uninstall

```bash
kubectl delete -f deploy/kubernetes-example.yaml
```

Deleting the Deployment stops the process; deleting the Role removes its
only authority. A default in-memory deployment has nothing else to clean up.
If `HISTORY_PATH` was enabled, delete or retain its explicitly created PVC
according to the site's retention policy. There is no CRD owned by the
console. The only cluster-scoped objects are the `ClusterRole` and
`ClusterRoleBinding` from `deploy/cluster-catalog-role.yaml`, which exist
only if you opted into `ALLOW_CLUSTER_CATALOGS`; delete them explicitly,
since deleting the namespace will not. When the operator manages the
console, deleting the `PgConsole` tears down the pod, its RBAC, its
NetworkPolicy, and its exposure.
