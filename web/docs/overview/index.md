---
sidebar_position: 1
title: Introduction
slug: /
---

# Overview

pgConsole is a per-cluster **operational console** for exactly one
CloudNativePG `Cluster`. It lets platform teams and application owners see
what the operator and Kubernetes report about that cluster — health and
conditions, which instance is primary, pod phases and restarts, recent
events, backup resources and their phases, and a bounded log tail — without
handing anyone `kubectl` access or a database connection.

It is an **observer of reported state**. It renders what CloudNativePG and
the Kubernetes API claim; it does not independently verify replication
health, data integrity, or backup usability. A `Backup` in phase
`completed` is an operator claim, not repository evidence.

## What it is, and is not

- **Read-only by default.** Only `get`, `list`, `watch`, and bounded
  `pods/log` reads. Two capabilities are opt-in behind their own flags and
  Roles: day-2 operations and the access-request review panel.
- **No SQL.** pgConsole never connects to PostgreSQL, never reads database
  credential Secrets, and never renders database contents. That is
  pgAdmin's job.
- **No object storage.** pgConsole never holds store credentials or parses
  a backup repository. That is ObjectStoreViewer's job; pgConsole can
  *consume its evidence* over a pod-private socket, nothing more.
- **No authentication.** A trusted proxy in front of the console
  authenticates the user and asserts a coarse level; the console trusts
  those headers only because the deployment confines its ingress to the
  proxy.
- **One cluster, one namespace — permanently.** Multi-cluster and
  multi-namespace views are not a roadmap item; they are non-goals. A
  console watches exactly one `Cluster` in one namespace, and the
  configuration cannot express more.
- **Not a dashboard, installer, or fleet manager.** pgConsole does not
  install CloudNativePG, does not manage clusters across a fleet, and is not
  a general Kubernetes dashboard. It stores nothing — no logs, no metrics,
  no history; every page is a live render of the current snapshots.

## The pgtoolbox family

pgConsole is the third application of **pgToolBox**, a Kubernetes/OpenShift
operator that deploys a family of PostgreSQL tools, each in its own
repository and each doing only its own job — a security property, not a
layering preference.

| Application | Answers | Speaks |
|---|---|---|
| **pgAdmin** | SQL-level questions *inside* the database | SQL |
| **ObjectStoreViewer** | structural questions about the backup repository | object storage |
| **pgConsole** | operator-level questions about one cluster | the Kubernetes API only |

The pgToolBox operator owns everything outside the process: it deploys the
container, puts an authentication proxy in front of it, exposes only the
authenticated endpoint, applies a default-deny NetworkPolicy, and
provisions the ServiceAccount and Role that are the console's entire
authority. This repository builds the application and its image and ships
example manifests mirroring what the operator generates.

## Where to go next

- [Concepts](concepts.md) — the vocabulary: origins, staleness, levels,
  and the closed request-time exceptions.
- [Deploy → Installation](../deploy/installation.md) — get a console
  running.
- [Architecture → Authorization](../architecture/authorization.md) — how
  route admission works.
- [Reference → Configuration](../reference/configuration.md) — every
  environment variable.
