---
sidebar_position: 4
title: Upgrade and uninstall
---

# Upgrade and uninstall

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
console and no cluster-scoped object. When the operator manages the
console, deleting the `PgConsole` tears down the pod, its RBAC, its
NetworkPolicy, and its exposure.
