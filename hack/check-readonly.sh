#!/bin/sh
# Read-only boundary scan. Mutation-shaped call sites may exist only in
# internal/ops, and the example Role must grant no mutating verb and
# nothing on secrets.
set -eu

status=0

# Kubernetes mutation-shaped calls live only where the architecture
# permits: internal/ops originates mutations, and internal/kube/ops.go
# is the transport that executes them (the narrow writer). No other call
# site may create, update, patch, delete, or apply.
if grep -rEn --include='*.go' --exclude='*_test.go' \
    '\.(Create|Update|Patch|Delete|Apply|DeleteCollection)\(' \
    cmd internal | grep -v '^internal/ops/' | grep -v '^internal/kube/ops.go:'; then
  echo "mutation-shaped call site outside internal/ops and the kube transport" >&2
  status=1
fi

# The default (read-only) example Role: no mutating verb, and nothing on
# the secret resource.
if grep -En '"(create|update|patch|delete|deletecollection|impersonate|escalate|bind)"' deploy/kubernetes-example.yaml; then
  echo "mutating or privilege verb found in the read-only example" >&2
  status=1
fi

# The operations-mode Role may grant only the enumerated writes: create
# on backups and patch on clusters/clusters-status. It must never grant a
# delete, a spec-replace, a privilege escalation, or anything on secrets.
if [ -f deploy/operations-role.yaml ]; then
  if grep -En '"(delete|deletecollection|update|impersonate|escalate|bind)"' deploy/operations-role.yaml; then
    echo "forbidden verb in the operations Role" >&2
    status=1
  fi
  if grep -Eq '"clusters"' deploy/operations-role.yaml && ! grep -Eq 'resourceNames' deploy/operations-role.yaml; then
    echo "operations Role patches clusters without a resourceNames pin" >&2
    status=1
  fi
fi

# The access-review Role may grant only the enumerated review access:
# reads on the two pgToolBox CRDs and update on the request status
# subresource. It must never grant a create, delete, spec-write, or
# privilege escalation, and nothing on secrets.
if [ -f deploy/access-review-role.yaml ]; then
  if grep -En '"(create|delete|deletecollection|patch|impersonate|escalate|bind)"' deploy/access-review-role.yaml; then
    echo "forbidden verb in the access-review Role" >&2
    status=1
  fi
  if grep -Eq '"update"' deploy/access-review-role.yaml && ! grep -Eq 'pgtoolboxaccessrequests/status' deploy/access-review-role.yaml; then
    echo "access-review Role grants update outside the request status subresource" >&2
    status=1
  fi
fi

# Cluster-scoped authority is confined to one opt-in manifest. Every
# other manifest must stay namespaced: that is the difference between a
# console whose blast radius is one namespace and one that can read the
# whole cluster.
for manifest in deploy/*.yaml; do
  [ "$manifest" = "deploy/cluster-catalog-role.yaml" ] && continue
  if grep -En '^kind: Cluster(Role|RoleBinding)' "$manifest"; then
    echo "cluster-scoped grant outside deploy/cluster-catalog-role.yaml: $manifest" >&2
    status=1
  fi
done

# The one cluster-scoped Role may grant only a get, and only on catalogs.
# A list or watch there would mean enumerating every catalog in the
# cluster rather than reading the one the Cluster names.
if [ -f deploy/cluster-catalog-role.yaml ]; then
  if grep -En '"(list|watch|create|update|patch|delete|deletecollection|impersonate|escalate|bind)"' deploy/cluster-catalog-role.yaml; then
    echo "cluster-catalog Role grants more than a get" >&2
    status=1
  fi
  if grep -En 'resources:' deploy/cluster-catalog-role.yaml | grep -vq 'clusterimagecatalogs'; then
    echo "cluster-catalog Role names a resource other than clusterimagecatalogs" >&2
    status=1
  fi
fi

# No manifest may reference the secret resource.
if grep -En '"secrets"' deploy/*.yaml; then
  echo "secret resource referenced in example manifests" >&2
  status=1
fi

exit "$status"
