---
sidebar_position: 1
title: Installation
---

# Installation

pgConsole is one static binary serving HTTP on port 3000. It is normally
deployed by the **pgToolBox operator**, which owns the proxy, exposure,
NetworkPolicy, and RBAC. You can also deploy it standalone from the example
manifests, provided you put a trusted, confining proxy in front of it.

## Prerequisites

- Kubernetes ≥ 1.28 (or OpenShift ≥ 4.14).
- [CloudNativePG](https://cloudnative-pg.io) ≥ 1.30 running the target
  `Cluster`.
- A versioned pgConsole image from GitHub Container Registry:

```bash
docker pull ghcr.io/fyannk/pgconsole:<version>
```

  Or build a development image for the local Docker platform:

```bash
make docker-build IMAGE=<registry>/pgconsole:<tag>
```

Release images support both `linux/amd64` and `linux/arm64`; `make
docker-build` builds only the current platform unless the caller configures
Buildx explicitly. The image is distroless and needs no shell. At runtime
the container starts under an arbitrary non-root UID, keeps its root
filesystem read-only with only `/tmp` writable, drops all Linux
capabilities, forbids privilege escalation, and listens on the
non-privileged port 3000. It reads its Kubernetes configuration only from
the in-cluster ServiceAccount and never loads a kubeconfig file. The example
manifest's `securityContext` encodes this, and its resource budget requests
25m CPU / 64Mi and limits 500m CPU / 256Mi.

- **A trusted proxy** in front of the console under a confining
  NetworkPolicy. Without it, the forwarded headers are spoofable and the
  authorization model does not hold.

pgConsole is validated against specific tested CloudNativePG + Kubernetes
tuples rather than open-ended version floors — the end-to-end test pins
CloudNativePG 1.30.0 on Kubernetes 1.34.1. Status fields that an older
supported CloudNativePG version does not report render as `unknown`, never
as errors.

## With the pgToolBox operator (recommended)

Declare one `PgConsole` object; the operator deploys the console, the
`pgtoolbox-proxy`, the exposure, the default-deny NetworkPolicy with the
proxy ingress exception, and the exact Role. See the pgToolBox
documentation for the `PgConsole` API.

## Standalone (example manifests)

`deploy/kubernetes-example.yaml` mirrors what the operator generates for a
read-only console observing the cluster `orders` in namespace `payments`:

```bash
kubectl apply -f deploy/kubernetes-example.yaml
```

It creates a ServiceAccount (token projected only into the container, no
pod-wide automount), the read-only Role and binding, a Service, a
**default-deny** NetworkPolicy, and the Deployment. Two exceptions are
intentionally absent because their selectors are deployment-specific — you
must add them:

1. an **ingress** exception admitting only your proxy to port 3000;
2. an **egress** exception to the Kubernetes API server.

Change the image and the `orders` / `payments` names to your own.

## Opt-in capabilities

The defaults are read-only. Each additional capability has its own flag and
its own Role — apply the Role only when you set the flag:

| Capability | Flag | Extra Role |
|---|---|---|
| Day-2 operations (backup, reload, restart, promote) | `ALLOW_OPERATIONS=true` | `deploy/operations-role.yaml` |
| The dba access-request review panel | `ALLOW_ACCESS_REVIEW=true` | `deploy/access-review-role.yaml` |

With a flag off, RBAC alone denies the writes; with a Role absent, the
capability cannot act even if the flag is set by mistake. The instance log
tail is on by default (`ALLOW_LOGS=true`) but requires the `poweruser`
level or above.

Object-definition history is enabled in memory by default and uses the same
existing watches, so it requires no extra Role. Durability is a separate
deployment choice: set `HISTORY_PATH`, mount the example PVC, and keep the
Deployment at one replica.

## First console

```bash
kubectl -n payments rollout status deploy/pgconsole-orders
kubectl -n payments get pod -l app.kubernetes.io/instance=orders
```

Browse to the proxy's external URL. A `view` user sees the read-only
baseline; `poweruser` additionally reaches operations and logs; `dba`
additionally reaches the review panel.

For every configurable value, see the
[configuration reference](../reference/configuration.md).
