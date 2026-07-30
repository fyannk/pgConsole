---
sidebar_position: 1
title: Running behind a proxy
---

# Running behind a proxy

pgConsole authenticates nobody. It expects a trusted proxy to authenticate
the user and forward two headers, and it trusts them **only** because a
NetworkPolicy confines its ingress to that proxy. This guide covers the
non-pgToolBox case; with the operator, all of this is generated for you.

## The contract

Your proxy must, on every request that reaches the console:

1. Authenticate the user.
2. Set `X-Forwarded-User` to the verified identity (matches
   `TRUSTED_USER_HEADER`).
3. Set `X-PgToolBox-Level` to `view`, `poweruser`, or `dba` (matches
   `TRUSTED_LEVEL_HEADER`), based on your own authorization policy.
4. Strip any client-supplied copies of those headers so a browser cannot
   inject them.

The console treats a missing, empty, or unrecognized level as the
read-only baseline — a fail-safe default, never an elevation.

## The confinement invariant

The headers mean nothing without the network boundary. Deploy a
default-deny NetworkPolicy and add exactly two exceptions:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: pgconsole-orders
  namespace: payments
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: pgconsole
      app.kubernetes.io/instance: orders
  policyTypes: ["Ingress", "Egress"]
  ingress:
    - from:
        - podSelector:
            matchLabels: { app.kubernetes.io/name: my-proxy }
      ports:
        - { protocol: TCP, port: 3000 }
  egress:
    # Allow only the Kubernetes API server (adjust to your cluster).
    - to:
        - ipBlock: { cidr: 10.0.0.1/32 }
      ports:
        - { protocol: TCP, port: 6443 }
```

If a request can reach the console without passing the proxy, the level
header is spoofable and the model is broken. That is the single most
important property to get right.

## Choosing a proxy

Any authenticating reverse proxy works: `oauth2-proxy`, an
ingress-integrated `auth_request`/`forwardAuth`/`ext_authz`, or OpenShift's
`oauth-proxy`. The proxy owns TLS termination and the level policy;
pgConsole owns neither. Two audiences that need different *Role-level*
authority still means two console deployments with two Roles — the level
only splits what one deployment shows.
