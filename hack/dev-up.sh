#!/bin/sh
# Stand up a local, browsable pgConsole against a real kind + CloudNativePG
# cluster, with every dba capability enabled and the review panel seeded
# with sample requests, then expose it on four local ports — one per
# proxy-asserted authorization level — so the whole level ladder is
# reviewable in a plain browser with no header extension:
#
#     http://localhost:3000   no proxy    baseline renders; gated actions refuse
#     http://localhost:3001   view        read-only baseline
#     http://localhost:3002   poweruser   + log tail + day-2 operations
#     http://localhost:3003   dba         + access-request review (everything)
#
# pgConsole reads Kubernetes credentials only from an in-cluster
# ServiceAccount, so it cannot run against a laptop kubeconfig — it must
# run inside a cluster. The console itself has no login: it trusts a
# proxy's forwarded X-Forwarded-User / X-PgToolBox-Level headers. Ports
# 3001-3003 are tiny local proxies that stand in for that trusted proxy,
# each injecting a fixed level; port 3000 is the raw console with no
# headers, so it demonstrates the un-proxied case (gated routes refuse
# with "identity required"; the read-only baseline still renders).
#
# First run does the full setup (~5 min). While the kind cluster is still
# up, re-running relaunches the four ports fast. RECREATE=true forces a
# clean rebuild.
#
# Environment overrides: CLUSTER, KIND_NODE_IMAGE, CNPG_MANIFEST, IMAGE,
# SKIP_BUILD=true, RECREATE=true, NO_FORWARD=true.
#
# Ctrl-C stops the forward and proxies; the cluster stays up.
# Tear down with:  kind delete cluster --name "$CLUSTER"
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

CLUSTER="${CLUSTER:-pgc-dev}"
CONTEXT="kind-$CLUSTER"
KIND_NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v1.34.0}"
CNPG_MANIFEST="${CNPG_MANIFEST:-https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.30/releases/cnpg-1.30.0.yaml}"
IMAGE="${IMAGE:-pgconsole:dev}"

log() { echo "[dev-up] $*"; }
need() { command -v "$1" > /dev/null 2>&1 || { echo "dev-up needs '$1' on PATH" >&2; exit 1; }; }
need kind
need kubectl
need docker
need go
need curl

# already_up reports whether this cluster already runs a ready console, so
# a re-run can skip the expensive build/create/deploy and just relaunch the
# four ports.
already_up() {
  kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || return 1
  kubectl --context "$CONTEXT" -n payments get deploy/pgconsole-orders > /dev/null 2>&1 || return 1
  ready=$(kubectl --context "$CONTEXT" -n payments get deploy/pgconsole-orders \
    -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo 0)
  [ "${ready:-0}" -ge 1 ] 2>/dev/null
}

if [ "${RECREATE:-false}" != "true" ] && already_up; then
  log "cluster + console already up — reusing it (RECREATE=true to rebuild)"
  kubectl config use-context "$CONTEXT" > /dev/null 2>&1 || true
else
  if [ "${SKIP_BUILD:-false}" != "true" ]; then
    log "building the image ($IMAGE)"
    make docker-build IMAGE="$IMAGE"
  fi

  log "creating kind cluster '$CLUSTER' ($KIND_NODE_IMAGE)"
  kind delete cluster --name "$CLUSTER" > /dev/null 2>&1 || true
  kind create cluster --name "$CLUSTER" --image "$KIND_NODE_IMAGE" --wait 120s
  kubectl config use-context "$CONTEXT" > /dev/null 2>&1 || true

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

  # The example ships a default-deny NetworkPolicy whose proxy/API
  # exceptions the operator would supply. kind's CNI enforces it, which
  # would block the port-forward — drop it for local dev (a throwaway
  # cluster, no real proxy).
  kubectl -n payments delete networkpolicy pgconsole-orders-default-deny > /dev/null 2>&1 || true

  log "installing the sample pgToolBox review CRDs and objects"
  kubectl apply -f hack/testdata/pgtoolbox-crds.yaml > /dev/null
  kubectl wait --for=condition=established --timeout=60s \
    crd/pgtoolboxroles.pgtoolbox.fyannk.dev \
    crd/pgtoolboxaccessrequests.pgtoolbox.fyannk.dev > /dev/null
  kubectl apply -f hack/testdata/pgtoolbox-samples.yaml > /dev/null

  log "enabling the dba capabilities (operations, review; logs on by default)"
  kubectl -n payments set env deployment/pgconsole-orders \
    ALLOW_OPERATIONS=true ALLOW_ACCESS_REVIEW=true > /dev/null
  kubectl -n payments rollout status deployment/pgconsole-orders --timeout=180s > /dev/null
fi

if [ "${NO_FORWARD:-false}" = "true" ]; then
  log "NO_FORWARD set — start the tunnel yourself:"
  log "  kubectl -n payments port-forward deploy/pgconsole-orders 3000:3000"
  exit 0
fi

# The level proxies stand in for the trusted pgToolBox proxy: each injects
# a fixed X-Forwarded-User / X-PgToolBox-Level onto every request and
# forwards to the raw console on :3000. Built once from an embedded source
# (stdlib only), run three times.
WORK=$(mktemp -d)
cat > "$WORK/dev-proxy.go" <<'GO'
// dev-proxy stands in for the trusted pgToolBox proxy: it overwrites the
// two forwarded headers the console reads on every request, so a plain
// browser sees exactly one authorization level's view. Overwrite, never
// trust a client-supplied value.
package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:3001", "address to listen on")
	target := flag.String("target", "http://127.0.0.1:3000", "console address")
	user := flag.String("user", "dev", "forwarded X-Forwarded-User")
	level := flag.String("level", "view", "forwarded X-PgToolBox-Level")
	flag.Parse()
	u, err := url.Parse(*target)
	if err != nil {
		log.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Header.Set("X-Forwarded-User", *user)
		r.Header.Set("X-PgToolBox-Level", *level)
	}
	log.Printf("dev-proxy %s -> %s (user=%s level=%s)", *listen, *target, *user, *level)
	log.Fatal(http.ListenAndServe(*listen, proxy))
}
GO
log "building the level proxies"
go build -o "$WORK/dev-proxy" "$WORK/dev-proxy.go"

PIDS=""
cleanup() {
  log "stopping the forward and proxies (the kind cluster stays up)"
  # shellcheck disable=SC2086
  [ -n "$PIDS" ] && kill $PIDS 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

log "port-forwarding the console to 127.0.0.1:3000"
kubectl -n payments port-forward deploy/pgconsole-orders 3000:3000 > /dev/null 2>&1 &
PIDS="$PIDS $!"

# Wait until the raw console answers before the proxies start pointing at it.
if ! curl --retry 30 --retry-connrefused --retry-delay 1 -fsS -o /dev/null http://127.0.0.1:3000/healthz; then
  log "console never became reachable on 127.0.0.1:3000"
  exit 1
fi

"$WORK/dev-proxy" -listen 127.0.0.1:3001 -user viewer   -level view      > /dev/null 2>&1 &
PIDS="$PIDS $!"
"$WORK/dev-proxy" -listen 127.0.0.1:3002 -user operator -level poweruser > /dev/null 2>&1 &
PIDS="$PIDS $!"
"$WORK/dev-proxy" -listen 127.0.0.1:3003 -user dba      -level dba       > /dev/null 2>&1 &
PIDS="$PIDS $!"

cat <<EOF

  pgConsole dev is up. Four ports, each a different proxy-asserted level:

    http://localhost:3000   no proxy     baseline renders; every gated action
                                         refuses ("identity required")
    http://localhost:3001   view         the read-only baseline
    http://localhost:3002   poweruser    + the log tail + the four day-2 operations
    http://localhost:3003   dba          + the access-request review panel (everything)

  The review panel (:3003 -> Access requests) is seeded with one pending
  request from 'alice', role picker reader / operator.

  Ctrl-C stops the forward and proxies; the kind cluster stays up, so
  re-running this script relaunches the four ports fast.
  Full teardown:  kind delete cluster --name $CLUSTER

EOF

log "serving — Ctrl-C to stop"
wait
