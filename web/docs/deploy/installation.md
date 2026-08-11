---
sidebar_position: 1
title: Installation
---

# Installation

pgConsole is one static binary serving HTTP on port 3000. It is designed to
be deployed by the **pgToolBox operator**, which owns the proxy, exposure,
NetworkPolicy, and RBAC.

:::info The operator is not published yet
As of 0.6.0 the pgToolBox operator and its `PgConsole` API are not publicly
available, so the standalone path below is the only way to install
pgConsole. Deploying standalone means **you** own the trust boundary the
console assumes — read
[Running behind a proxy](../guides/running-behind-a-proxy.md) first.
:::

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
CloudNativePG 1.30.0 on Kubernetes 1.34.0. Status fields that an older
supported CloudNativePG version does not report render as `unknown`, never
as errors.

## Standalone (example manifests)

`deploy/kubernetes-example.yaml` mirrors what the operator generates for a
read-only console observing the cluster `orders` in namespace `payments`:

:::caution Edit the manifest before applying it
The example pins `image: pgconsole:dev`, which exists only after a local
`make docker-build`. Replace it with a released image — see
[Prerequisites](#prerequisites) — and change the `orders` / `payments`
names to your own. Applied unedited, the Deployment stays in
`ImagePullBackOff`.
:::

```bash
kubectl apply -f deploy/kubernetes-example.yaml
```

It creates a ServiceAccount (token projected only into the container, no
pod-wide automount), the read-only Role and binding, a Service, a
**default-deny** NetworkPolicy, and the Deployment. Three exceptions are
intentionally absent because their selectors are deployment-specific — you
must add them:

1. an **ingress** exception admitting only your proxy to port 3000;
2. an **egress** exception to the Kubernetes API server;
3. with `METRICS_ENABLED` left at its default of `true`, an **egress**
   exception to the instance and pooler pods on ports `9187` and `9127`.
   The console scrapes those exporters directly; without this the metrics
   screens stay empty while every other screen looks healthy.

Change the image and the `orders` / `payments` names to your own.

## Opt-in capabilities

The defaults are read-only. Each additional capability has its own flag and
its own Role — apply the Role only when you set the flag:

| Capability | Flag | Extra Role |
|---|---|---|
| Day-2 operations (backup, reload, restart, promote) | `ALLOW_OPERATIONS=true` | `deploy/operations-role.yaml` |
| The dba access-request review panel | `ALLOW_ACCESS_REVIEW=true` | `deploy/access-review-role.yaml` |
| Cluster-wide image catalogs | `ALLOW_CLUSTER_CATALOGS=true` | `deploy/cluster-catalog-role.yaml` |

The third is the only one that grants anything cluster-scoped: a `get`
on `clusterimagecatalogs`, and nothing else. Declining it costs one
panel, which reads `unknown` rather than failing.

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

Browse to the proxy's external URL. Every screen is admitted by the
forwarded level, and no level reaches nothing: `view` reaches the read
screens; `poweruser` additionally reaches the log tails; `dba`
additionally reaches the day-2 operations and the review panel. A
request carrying no level at all reaches no screen, not a reduced one.

For every configurable value, see the
[configuration reference](../reference/configuration.md).
