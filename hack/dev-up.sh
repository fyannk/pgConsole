#!/bin/sh
# Stand up a local, browsable pgConsole against a real kind + CloudNativePG
# cluster, with every dba capability enabled and the review panel seeded
# with sample requests. It leaves the cluster running and (unless
# NO_FORWARD=true) port-forwards the console to localhost:3000.
#
# pgConsole reads Kubernetes credentials only from an in-cluster
# ServiceAccount, so it cannot run against a kubeconfig from your laptop —
# it must run inside a cluster. This is the lightest way to get there.
#
# The console has no login: it trusts a proxy's forwarded headers. In dev
# you are the proxy, so add these two headers to every request with a
# browser extension (ModHeader, Requestly, ...):
#
#     X-Forwarded-User: fanch          (any name — display/audit only)
#     X-PgToolBox-Level: dba           (top of none<view<poweruser<dba)
#
# Environment overrides: CLUSTER, KIND_NODE_IMAGE, CNPG_MANIFEST, IMAGE,
# SKIP_BUILD=true, NO_FORWARD=true.
#
# Tear down with:  kind delete cluster --name "$CLUSTER"
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

CLUSTER="${CLUSTER:-pgc-dev}"
KIND_NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v1.34.0}"
CNPG_MANIFEST="${CNPG_MANIFEST:-https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.30/releases/cnpg-1.30.0.yaml}"
IMAGE="${IMAGE:-pgconsole:dev}"

log() { echo "[dev-up] $*"; }
need() { command -v "$1" > /dev/null 2>&1 || { echo "dev-up needs '$1' on PATH" >&2; exit 1; }; }
need kind
need kubectl
need docker

if [ "${SKIP_BUILD:-false}" != "true" ]; then
  log "building the image ($IMAGE)"
  make docker-build IMAGE="$IMAGE"
fi

log "creating kind cluster '$CLUSTER' ($KIND_NODE_IMAGE)"
kind delete cluster --name "$CLUSTER" > /dev/null 2>&1 || true
kind create cluster --name "$CLUSTER" --image "$KIND_NODE_IMAGE" --wait 120s

log "installing CloudNativePG"
kubectl apply --server-side -f "$CNPG_MANIFEST" > /dev/null
kubectl -n cnpg-system rollout status deployment/cnpg-controller-manager --timeout=300s > /dev/null

log "creating the target cluster orders/payments"
kubectl create namespace payments > /dev/null
cat <<'YAML' | kubectl apply -f - > /dev/null
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: orders
  namespace: payments
spec:
  instances: 1
  storage:
    size: 1Gi
YAML

log "waiting for the cluster to become healthy (this takes a minute)"
phase=""
i=0
while [ "$phase" != "Cluster in healthy state" ]; do
  i=$((i + 1))
  [ "$i" -le 120 ] || { log "cluster never became healthy (last phase: $phase)"; exit 1; }
  sleep 5
  phase=$(kubectl -n payments get cluster orders -o jsonpath='{.status.phase}' 2>/dev/null || true)
done
log "cluster healthy"

log "loading the image and deploying the console"
kind load docker-image "$IMAGE" --name "$CLUSTER"
kubectl apply -f deploy/kubernetes-example.yaml > /dev/null
kubectl apply -f deploy/operations-role.yaml > /dev/null
kubectl apply -f deploy/access-review-role.yaml > /dev/null

# The example ships a default-deny NetworkPolicy whose proxy/API exceptions
# the operator would supply. kind's CNI enforces it, which would block the
# port-forward — drop it for local dev (a throwaway cluster, no real proxy).
kubectl -n payments delete networkpolicy pgconsole-orders-default-deny > /dev/null 2>&1 || true

log "installing the sample pgToolBox review CRDs and objects"
kubectl apply -f hack/testdata/pgtoolbox-crds.yaml > /dev/null
kubectl wait --for=condition=established --timeout=60s \
  crd/pgtoolboxroles.pgtoolbox.fyannk.dev \
  crd/pgtoolboxaccessrequests.pgtoolbox.fyannk.dev > /dev/null
kubectl apply -f hack/testdata/pgtoolbox-samples.yaml > /dev/null

log "enabling the dba capabilities (operations, review; logs are on by default)"
kubectl -n payments set env deployment/pgconsole-orders \
  ALLOW_OPERATIONS=true ALLOW_ACCESS_REVIEW=true > /dev/null
kubectl -n payments rollout status deployment/pgconsole-orders --timeout=180s > /dev/null

cat <<EOF

  pgConsole is up. Add these request headers with a browser extension
  (they are what a trusted proxy would inject), scoped to localhost:3000:

      X-Forwarded-User: fanch
      X-PgToolBox-Level: dba

  Then open  http://localhost:3000

  dba reaches everything: the read-only baseline, the log tail, the four
  day-2 operations, and the access-request review panel (seeded with one
  pending request from 'alice' and a role picker of reader/operator).

  Tear down when done:  kind delete cluster --name $CLUSTER

EOF

if [ "${NO_FORWARD:-false}" = "true" ]; then
  log "NO_FORWARD set — start the tunnel yourself:"
  log "  kubectl -n payments port-forward deploy/pgconsole-orders 3000:3000"
  exit 0
fi

log "port-forwarding localhost:3000 -> the console (Ctrl-C to stop; the cluster keeps running)"
exec kubectl -n payments port-forward deploy/pgconsole-orders 3000:3000
