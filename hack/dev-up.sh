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
# The dev cluster is deliberately not the minimum one: it runs two
# connection poolers and archives to a throwaway MinIO through the
# barman-cloud plugin, because the console's endpoint, storage and backup
# panels have nothing to say against a bare three-instance cluster.
# SKIP_BACKUP=true drops the object store half of that, and
# SKIP_EVIDENCE=true leaves out the ObjectStoreViewer sidecar that
# gives the Evidence screens something real to read.
#
# Environment overrides: CLUSTER, KIND_NODE_IMAGE, CNPG_MANIFEST,
# CERT_MANAGER_MANIFEST, BARMAN_MANIFEST, IMAGE, VIEWER_IMAGE,
# MINIO_ENDPOINT, EVIDENCE_REGION, SKIP_BUILD=true, SKIP_BACKUP=true,
# SKIP_EVIDENCE=true, RECREATE=true, NO_FORWARD=true.
#
# Behind a mandatory HTTP proxy, export HTTP_PROXY/HTTPS_PROXY/NO_PROXY
# before running: kubectl reads them to fetch the manifests above, kind
# copies them into the node so containerd can pull images, and
# `make docker-build` forwards them into the builder. Nothing here needs
# the proxy on loopback, and nothing injects it into the pods.
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
# The barman-cloud plugin is what gives the dev cluster a real object
# store, and it needs cert-manager for the mTLS between operator and
# plugin. Set SKIP_BACKUP=true to stand up the cluster without either;
# the console then honestly reports no object store referenced.
CERT_MANAGER_MANIFEST="${CERT_MANAGER_MANIFEST:-https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.yaml}"
BARMAN_MANIFEST="${BARMAN_MANIFEST:-https://github.com/cloudnative-pg/plugin-barman-cloud/releases/download/v0.14.0/manifest.yaml}"
IMAGE="${IMAGE:-pgconsole:dev}"
# The sibling that reads object storage. pgConsole consumes its
# evidence API and never gains store credentials of its own.
VIEWER_IMAGE="${VIEWER_IMAGE:-ghcr.io/fyannk/pgobjectstoreviewer:latest}"
# The MinIO the dev cluster archives to, and the region both the
# viewer and the fingerprint are computed with. Both sides must agree
# on the region exactly: it is one of the fingerprint inputs.
MINIO_ENDPOINT="${MINIO_ENDPOINT:-http://minio.minio.svc:9000}"
EVIDENCE_REGION="${EVIDENCE_REGION:-us-east-1}"

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

# enable_persistence gives the console the durable volume the shipped
# example documents but leaves commented out, so the metrics window and
# the object history survive a rollout instead of restarting empty.
#
# Without it every redeploy throws the charts away, which is the wrong
# default for a dev loop that redeploys on every change: the console is
# a stateless process by design, and "stateless" here means the window
# lives and dies with the pod unless a volume says otherwise.
#
# Two things are load-bearing and neither is optional:
#
#   - strategy Recreate. The history journal takes an exclusive lock on
#     the bbolt file and a second replica fails fast at startup. Under
#     the default RollingUpdate the new pod cannot start until the old
#     one releases the lock, and the old one is not removed until the
#     new one is ready — a rollout that deadlocks until it times out.
#   - replicas 1, for the same reason.
#
# Idempotent, and applied on the reuse path too: a cluster created
# before this existed should gain the volume on the next run rather than
# needing RECREATE=true.
enable_persistence() {
  log "giving the console a durable volume (metrics window + object history)"
  cat <<'YAML' | kubectl apply -f - > /dev/null
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pgconsole-orders-state
  namespace: payments
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 512Mi
YAML
  # Strategic, not merge: a JSON merge patch replaces whole arrays, so
  # naming the container to add a mount would drop its image, env,
  # ports and probes. Strategic merges volumes, containers and mounts by
  # their name key, which is exactly what is wanted here, and honours
  # the null that removes rollingUpdate (Recreate rejects it).
  kubectl -n payments patch deployment pgconsole-orders --type strategic -p '{
    "spec": {
      "replicas": 1,
      "strategy": {"type": "Recreate", "rollingUpdate": null},
      "template": {"spec": {
        "volumes": [
          {"name": "tmp", "emptyDir": {}},
          {"name": "state", "persistentVolumeClaim": {"claimName": "pgconsole-orders-state"}}
        ],
        "containers": [{
          "name": "pgconsole",
          "volumeMounts": [
            {"name": "tmp", "mountPath": "/tmp"},
            {"name": "state", "mountPath": "/var/lib/pgconsole"}
          ]
        }]
      }}
    }
  }' > /dev/null
  kubectl -n payments set env deployment/pgconsole-orders \
    HISTORY_PATH=/var/lib/pgconsole/history.db \
    METRICS_PATH=/var/lib/pgconsole/metrics.snapshot > /dev/null
  kubectl -n payments rollout status deployment/pgconsole-orders --timeout=180s > /dev/null
}

# enable_evidence runs the real ObjectStoreViewer as the console's
# evidence sidecar, so the Evidence and cross-check screens show what the
# repository actually holds instead of saying the sidecar is not wired.
#
# pgConsole never reads object storage; it consumes a versioned evidence
# API over a pod-private Unix socket, and the viewer is the only thing
# here holding store credentials. That separation is the point, so the
# mounts are asymmetric on purpose:
#
#   viewer     socket (rw), evidence token (ro), MinIO credentials (ro)
#   pgconsole  socket (rw), evidence token (ro) — and never the credentials
#
# Both containers reach the socket through the pod's fsGroup rather than
# by sharing a user. Nothing is published: no port, no Service, no
# ingress. SKIP_EVIDENCE=true leaves the console in its unwired state,
# which is the one the shipped example deploys.
enable_evidence() {
  if [ "${SKIP_EVIDENCE:-false}" = "true" ]; then
    log "SKIP_EVIDENCE set — the evidence screens stay honestly unwired"
    return 0
  fi
  log "pulling and loading ObjectStoreViewer ($VIEWER_IMAGE)"
  docker image inspect "$VIEWER_IMAGE" > /dev/null 2>&1 || docker pull "$VIEWER_IMAGE" > /dev/null
  kind load docker-image "$VIEWER_IMAGE" --name "$CLUSTER" > /dev/null

  # The token is the sidecar's whole authentication. It must be a
  # canonical 32-byte unpadded-base64url value; the viewer refuses
  # anything else at startup rather than accepting a weak one.
  if ! kubectl -n payments get secret pgconsole-evidence-token > /dev/null 2>&1; then
    token=$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n')
    kubectl -n payments create secret generic pgconsole-evidence-token \
      --from-literal=evidence-token="$token" > /dev/null
  fi
  # Separate files, not the plugin's two-key Secret: the viewer's
  # static-files mode wants one path per credential.
  kubectl -n payments create secret generic pgconsole-evidence-store \
    --from-literal=aws-access-key-id=pgconsoledev \
    --from-literal=aws-secret-access-key=pgconsoledev123 \
    --dry-run=client -o yaml | kubectl apply -f - > /dev/null

  # The fingerprint pgtoolbox would compute, from the same shared api
  # module the viewer stamps its responses with. Its inputs are the
  # ObjectStore in hack/testdata/dev-backup.yaml: bucket pgbackups,
  # prefix orders, and the Barman server the plugin writes under, which
  # for CloudNativePG is the cluster name.
  log "computing the destination fingerprint"
  fingerprint=$(go run ./hack/evidence-fingerprint \
    -endpoint "$MINIO_ENDPOINT" -region "$EVIDENCE_REGION" \
    -bucket pgbackups -prefix orders -server orders)
  cluster_uid=$(kubectl -n payments get cluster orders -o jsonpath='{.metadata.uid}')

  log "wiring the evidence sidecar into the console pod"
  kubectl -n payments patch deployment pgconsole-orders --type strategic -p '{
    "spec": {"template": {"spec": {
      "securityContext": {"runAsNonRoot": true, "fsGroup": 65532, "seccompProfile": {"type": "RuntimeDefault"}},
      "volumes": [
        {"name": "evidence-socket", "emptyDir": {}},
        {"name": "evidence-token", "secret": {"secretName": "pgconsole-evidence-token", "defaultMode": 288}},
        {"name": "evidence-store", "secret": {"secretName": "pgconsole-evidence-store", "defaultMode": 288}},
        {"name": "viewer-tmp", "emptyDir": {}}
      ],
      "containers": [
        {
          "name": "pgconsole",
          "volumeMounts": [
            {"name": "evidence-socket", "mountPath": "/var/run/objectstoreviewer"},
            {"name": "evidence-token", "mountPath": "/var/run/secrets/objectstoreviewer/evidence-token", "subPath": "evidence-token", "readOnly": true}
          ]
        },
        {
          "name": "objectstoreviewer",
          "image": "'"$VIEWER_IMAGE"'",
          "imagePullPolicy": "IfNotPresent",
          "securityContext": {
            "allowPrivilegeEscalation": false,
            "readOnlyRootFilesystem": true,
            "capabilities": {"drop": ["ALL"]}
          },
          "env": [
            {"name": "RUNTIME_MODE", "value": "pgconsole-sidecar"},
            {"name": "REPOSITORY_FORMAT", "value": "barman-cloud"},
            {"name": "PROVIDER", "value": "s3"},
            {"name": "DESTINATION_PATH", "value": "s3://pgbackups/orders"},
            {"name": "ENDPOINT_URL", "value": "'"$MINIO_ENDPOINT"'"},
            {"name": "BARMAN_SERVER_NAMES", "value": "orders"},
            {"name": "EVIDENCE_TOKEN_FILE", "value": "/var/run/secrets/objectstoreviewer/evidence-token"},
            {"name": "STORE_CREDENTIAL_MODE", "value": "static-files"},
            {"name": "AWS_ACCESS_KEY_ID_FILE", "value": "/var/run/secrets/objectstoreviewer-store/aws-access-key-id"},
            {"name": "AWS_SECRET_ACCESS_KEY_FILE", "value": "/var/run/secrets/objectstoreviewer-store/aws-secret-access-key"},
            {"name": "AWS_REGION", "value": "'"$EVIDENCE_REGION"'"},
            {"name": "CNPG_CLUSTER_NAMESPACE", "value": "payments"},
            {"name": "CNPG_CLUSTER_NAME", "value": "orders"},
            {"name": "CNPG_CLUSTER_UID", "value": "'"$cluster_uid"'"}
          ],
          "volumeMounts": [
            {"name": "evidence-socket", "mountPath": "/var/run/objectstoreviewer"},
            {"name": "evidence-token", "mountPath": "/var/run/secrets/objectstoreviewer/evidence-token", "subPath": "evidence-token", "readOnly": true},
            {"name": "evidence-store", "mountPath": "/var/run/secrets/objectstoreviewer-store", "readOnly": true},
            {"name": "viewer-tmp", "mountPath": "/tmp"}
          ]
        }
      ]
    }}}
  }' > /dev/null

  kubectl -n payments set env deployment/pgconsole-orders \
    REPOSITORY_EVIDENCE_URL=unix:///var/run/objectstoreviewer/evidence.sock \
    REPOSITORY_EVIDENCE_TOKEN_FILE=/var/run/secrets/objectstoreviewer/evidence-token \
    REPOSITORY_EXPECTED_FINGERPRINT="$fingerprint" \
    REPOSITORY_BARMAN_SERVER=orders > /dev/null
  kubectl -n payments rollout status deployment/pgconsole-orders --timeout=240s > /dev/null
}

# setup_backup gives the dev cluster a real object store: cert-manager,
# the barman-cloud plugin, a throwaway MinIO, and the ObjectStore the
# Cluster then names. The order matters and the waits are not padding —
# the plugin's CRD must exist before the ObjectStore applies, and the
# Cluster only gains its plugin sidecar on the rolling restart that
# adding spec.plugins triggers. A Backup taken before that sidecar exists
# fails with "requested plugin is not available", so this waits for the
# sidecar rather than assuming the patch was enough.
setup_backup() {
  log "installing cert-manager (the barman-cloud plugin needs it for mTLS)"
  kubectl apply -f "$CERT_MANAGER_MANIFEST" > /dev/null
  kubectl -n cert-manager rollout status deployment/cert-manager-webhook --timeout=300s > /dev/null

  log "installing the barman-cloud plugin"
  kubectl apply -f "$BARMAN_MANIFEST" > /dev/null
  kubectl wait --for=condition=established --timeout=120s \
    crd/objectstores.barmancloud.cnpg.io > /dev/null
  kubectl -n cnpg-system rollout status deployment/barman-cloud --timeout=300s > /dev/null

  log "deploying MinIO and creating the backup bucket"
  kubectl apply -f hack/testdata/dev-minio.yaml > /dev/null
  kubectl -n minio rollout status deployment/minio --timeout=180s > /dev/null
  kubectl -n minio delete job mc-mkbucket --ignore-not-found > /dev/null 2>&1 || true
  cat <<'YAML' | kubectl apply -f - > /dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: mc-mkbucket
  namespace: minio
spec:
  backoffLimit: 4
  template:
    spec:
      restartPolicy: OnFailure
      containers:
        - name: mc
          image: minio/mc:latest
          command: ["sh", "-c"]
          args:
            - mc alias set m http://minio.minio.svc:9000 pgconsoledev pgconsoledev123 &&
              mc mb --ignore-existing m/pgbackups
YAML
  kubectl -n minio wait --for=condition=complete --timeout=180s job/mc-mkbucket > /dev/null

  log "declaring the object store and the nightly schedule"
  kubectl apply -f hack/testdata/dev-backup.yaml > /dev/null

  log "pointing the cluster at the object store (this rolls the instances)"
  kubectl -n payments patch cluster orders --type merge -p '{"spec":{"plugins":[{"name":"barman-cloud.cloudnative-pg.io","enabled":true,"isWALArchiver":true,"parameters":{"barmanObjectName":"orders-store"}}]}}' > /dev/null

  # The sidecar is a native (restartable init) container, so it shows up
  # in .spec.initContainers, not .spec.containers.
  #
  # Select on podRole=instance, not on cnpg.io/cluster: the latter also
  # matches the two poolers added just above, and a PgBouncer pod never
  # gains a barman sidecar — waiting on it can only time out. Counting
  # Running instances against spec.instances rather than looking for a
  # missing one also keeps the rolling restart honest: mid-roll the list
  # is short, and "none of the pods I can see lacks it" would pass on an
  # empty list.
  log "waiting for every instance to gain the plugin sidecar"
  want=$(kubectl -n payments get cluster orders -o jsonpath='{.spec.instances}')
  i=0
  while :; do
    have=$(kubectl -n payments get pods -l cnpg.io/podRole=instance \
      --field-selector=status.phase=Running \
      -o jsonpath='{range .items[*]}{.spec.initContainers[*].name}{"\n"}{end}' 2>/dev/null |
      grep -c plugin-barman-cloud || true)
    [ "${have:-0}" -eq "$want" ] && break
    i=$((i + 1))
    [ "$i" -le 120 ] || { log "the plugin sidecar never appeared on the instances ($have/$want)"; exit 1; }
    sleep 5
  done
  log "backup wiring ready — WAL archives to s3://pgbackups/orders"
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
  instances: 3
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

  log "adding the connection poolers"
  kubectl apply -f hack/testdata/dev-poolers.yaml > /dev/null

  if [ "${SKIP_BACKUP:-false}" != "true" ]; then
    setup_backup
  else
    log "SKIP_BACKUP set — no object store; the backup panels stay empty"
  fi

  log "loading the image and deploying the console"
  kind load docker-image "$IMAGE" --name "$CLUSTER"
  kubectl apply -f deploy/kubernetes-example.yaml > /dev/null
  kubectl apply -f deploy/operations-role.yaml > /dev/null
  kubectl apply -f deploy/access-review-role.yaml > /dev/null

  # The shipped example grants nothing on secrets — that is the
  # console's read-only guarantee. Dev opts in, exactly as it opts into
  # the dba capabilities, so the children drawing shows the cluster's
  # Secrets as metadata (name, type, key count; never a value).
  log "granting the dev-only secrets-metadata read"
  cat <<'YAML' | kubectl apply -f - > /dev/null
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: pgconsole-orders-secrets-meta
  namespace: payments
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: pgconsole-orders-secrets-meta
  namespace: payments
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: pgconsole-orders-secrets-meta
subjects:
  - kind: ServiceAccount
    name: pgconsole-orders
    namespace: payments
YAML

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

  # The sibling-tool link-outs are what put pgAdmin and its neighbours in
  # the sidebar; buildLinks drops every unconfigured one, so a dev console
  # without them renders a sidebar the design does not describe. These are
  # the same placeholder hosts the UI harness uses (uiLinks in
  # uiharness_test.go), so dev, the browser tests and the design project
  # all show the same three entries. They are deliberately unreachable —
  # this is about the console's own chrome, not about the sibling tools.
  log "configuring the sibling-tool link-outs (ObjectStoreViewer, pgAdmin, monitoring)"
  kubectl -n payments set env deployment/pgconsole-orders \
    OBJECTSTOREVIEWER_URL=https://viewer.example.com/orders \
    PGADMIN_URL=https://pgadmin.example.com \
    MONITORING_URL=https://grafana.example.com/d/pg > /dev/null
  kubectl -n payments rollout status deployment/pgconsole-orders --timeout=180s > /dev/null
fi

# Outside the branch on purpose: a cluster stood up before this existed
# should gain the volume on the next run, not on the next RECREATE. The
# patch is a no-op once applied, so the reuse path stays fast.
enable_persistence
enable_evidence

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
# --noproxy keeps this off any http_proxy the machine mandates: the console
# is on loopback, and a proxy asked to reach it would answer for itself.
if ! curl --noproxy '*' --retry 30 --retry-connrefused --retry-delay 1 -fsS -o /dev/null http://127.0.0.1:3000/healthz; then
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
