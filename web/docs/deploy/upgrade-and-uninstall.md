---
sidebar_position: 4
title: Upgrade and uninstall
---

# Upgrade and uninstall

## Upgrade

pgConsole is **stateless** — no database, no session store, no persistent
volume, no external state. Upgrading is rolling the Deployment to a new
image tag.

- **Operator-managed**: bump the image in the `PgConsole` object (or the
  operator's default image) and let it roll the pod once via its
  config-checksum annotation.
- **Standalone**: change the `image:` in the Deployment and
  `kubectl rollout restart` (or re-apply).

In-flight CSRF confirmation tokens are minted from a random per-process key
and are invalidated by the restart. This is harmless: a reviewer or
operator simply reloads the confirmation page for a fresh token. There is
no migration and no data to carry across versions.

Compatibility with CloudNativePG is pinned per release: the day-2
operations reproduce the exact `kubectl cnpg` plugin interactions for the
pinned CNPG version. A CNPG minor-version bump re-verifies those
interactions before the console claims support.

## Uninstall

```bash
kubectl delete -f deploy/kubernetes-example.yaml
```

Deleting the Deployment stops the process; deleting the Role removes its
only authority. There is nothing else to clean up — no PVC, no CRD owned by
the console, no cluster-scoped object. When the operator manages the
console, deleting the `PgConsole` tears down the pod, its RBAC, its
NetworkPolicy, and its exposure.
