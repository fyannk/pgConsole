#!/bin/sh
# End-to-end test against the pinned tuple: a real kind cluster runs
# the pinned CloudNativePG release, the console deploys from the example
# manifests, and the page renders genuine operator-reported state. Two
# live demonstrations follow: breaking every watch (killing the API
# server) yields visible staleness with recovery, and removing the
# console's RBAC yields retained last-good content that stays stale
# with the ObjectStore panel degraded — never an error page and never a
# silently healthy one.
#
# Artifacts land in artifacts/e2e/.
set -eu

KIND_NODE_IMAGE="${KIND_NODE_IMAGE:-kindest/node:v1.34.0}"
CNPG_MANIFEST="${CNPG_MANIFEST:-https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.30/releases/cnpg-1.30.0.yaml}"
CLUSTER_NAME="pgconsole-e2e"
OUT="artifacts/e2e"
IMAGE="${IMAGE:-pgconsole:dev}"

mkdir -p "$OUT"
log() { echo "[e2e] $*" | tee -a "$OUT/summary.log"; }

cleanup() {
  if [ "${KEEP_CLUSTER:-false}" != "true" ]; then
    kind delete cluster --name "$CLUSTER_NAME" > /dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

: > "$OUT/summary.log"
log "tuple: $KIND_NODE_IMAGE + $CNPG_MANIFEST"

log "creating kind cluster"
kind delete cluster --name "$CLUSTER_NAME" > /dev/null 2>&1 || true
kind create cluster --name "$CLUSTER_NAME" --image "$KIND_NODE_IMAGE" --wait 120s > /dev/null

log "installing CloudNativePG"
kubectl apply --server-side -f "$CNPG_MANIFEST" > /dev/null
kubectl -n cnpg-system rollout status deployment/cnpg-controller-manager --timeout=300s > /dev/null

log "creating the target cluster"
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

phase=""
i=0
while [ "$phase" != "Cluster in healthy state" ]; do
  i=$((i + 1))
  [ "$i" -le 120 ] || { log "cluster never became healthy (last phase: $phase)"; exit 1; }
  sleep 5
  phase=$(kubectl -n payments get cluster orders -o jsonpath='{.status.phase}' 2>/dev/null || true)
done
log "cluster healthy"

log "deploying the console from the example manifests"
kind load docker-image "$IMAGE" --name "$CLUSTER_NAME" > /dev/null
kubectl apply -f deploy/kubernetes-example.yaml > /dev/null
# One link-out is configured the way the operator would, so the journey
# demonstrates the evidence pointer against the pinned tuple.
kubectl -n payments set env deployment/pgconsole-orders \
  OBJECTSTOREVIEWER_URL=https://viewer.example.com/repo > /dev/null
kubectl -n payments rollout status deployment/pgconsole-orders --timeout=180s > /dev/null

# The example ships the default-deny NetworkPolicy with the
# operator-owned exceptions deliberately absent; kind enforces policies,
# so the harness supplies the exceptions the operator would generate —
# ingress standing in for the auth proxy, egress for the API path.
cat <<'YAML' | kubectl apply -f - > /dev/null
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: pgconsole-orders-e2e-exceptions
  namespace: payments
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: pgconsole
      app.kubernetes.io/instance: orders
  policyTypes: ["Ingress", "Egress"]
  ingress:
    - ports:
        - port: 3000
  egress:
    - {}
YAML

# The e2e harness reaches the console through a NodePort because
# kubectl port-forward tunnels through the API server, which one of the
# demonstrations deliberately kills.
kubectl -n payments expose deployment pgconsole-orders --name pgconsole-e2e-access --type=NodePort --port 3000 > /dev/null
node_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${CLUSTER_NAME}-control-plane")
node_port=$(kubectl -n payments get svc pgconsole-e2e-access -o jsonpath='{.spec.ports[0].nodePort}')
base="http://${node_ip}:${node_port}"

# Every screen is admitted by the forwarded level, so the journey states
# an identity on every fetch; the ungated case is asserted on its own in
# the admission section below. poweruser is the level that reaches every
# read screen and no more — the operations and review panels are dba and
# are exercised with their own headers.
ID_USER="X-Forwarded-User: fanch"
ID_LEVEL="X-PgToolBox-Level: poweruser"

# The demonstrations read the cluster overview, not the index: the
# overview is the screen carrying the cluster verdict together with the
# per-section freshness the demonstrations turn stale and recover.
page() { curl -fsS --max-time 5 -H "$ID_USER" -H "$ID_LEVEL" "$base/cluster/overview" 2>/dev/null || true; }
page_at() { curl -fsS --max-time 5 -H "$ID_USER" -H "$ID_LEVEL" "$base$1" 2>/dev/null || true; }

# rollout status returns once the new replica is available, but the old
# one is still terminating and the NodePort can keep routing to it for a
# few seconds after its listener has closed. A probe landing there is
# refused, and under set -e an unretried curl then kills the journey
# with curl's own exit code. Settled means the old pod is gone — giving
# kube-proxy time to drop its endpoint — and the console answers again;
# only then are the unretried probes that follow trustworthy.
settle_console_rollout() {
  kubectl -n payments rollout status deployment/pgconsole-orders --timeout=180s > /dev/null
  i=0
  until [ "$(kubectl -n payments get pods \
      -l app.kubernetes.io/name=pgconsole,app.kubernetes.io/instance=orders \
      --no-headers 2>/dev/null | wc -l)" -eq 1 ]; do
    i=$((i + 2)); [ "$i" -le 120 ] || { log "old console replica never terminated $1"; exit 1; }; sleep 2
  done
  i=0
  until curl -fsS --max-time 5 "$base/healthz" > /dev/null 2>&1; do
    i=$((i + 2)); [ "$i" -le 60 ] || { log "console unreachable $1"; exit 1; }; sleep 2
  done
}

i=0
until curl -fsS --max-time 5 "$base/healthz" > /dev/null 2>&1; do
  i=$((i + 2))
  if [ "$i" -gt 60 ]; then
    log "console unreachable at $base"
    kubectl -n payments get pods -o wide | tee -a "$OUT/summary.log"
    kubectl -n payments logs deployment/pgconsole-orders --tail=20 | tee -a "$OUT/summary.log" || true
    exit 1
  fi
  sleep 2
done
log "console reachable at $base"

wait_page_contains() {
  needle="$1"; timeout="$2"; artifact="$3"
  i=0
  while :; do
    body=$(page)
    if echo "$body" | grep -qF "$needle"; then
      echo "$body" > "$OUT/$artifact"
      return 0
    fi
    i=$((i + 2))
    [ "$i" -le "$timeout" ] || { echo "$body" > "$OUT/$artifact"; log "timed out waiting for: $needle"; return 1; }
    sleep 2
  done
}

wait_path_contains() {
  path="$1"; needle="$2"; timeout="$3"; artifact="$4"
  i=0
  while :; do
    body=$(page_at "$path")
    if echo "$body" | grep -qF "$needle"; then
      echo "$body" > "$OUT/$artifact"
      return 0
    fi
    i=$((i + 2))
    [ "$i" -le "$timeout" ] || { echo "$body" > "$OUT/$artifact"; log "timed out waiting for $path to contain: $needle"; return 1; }
    sleep 2
  done
}

log "creating a Backup resource (operator will report its phase)"
cat <<'YAML' | kubectl apply -f - > /dev/null
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata:
  name: manual-1
  namespace: payments
spec:
  cluster:
    name: orders
YAML

log "baseline: real operator-reported state renders"
wait_page_contains "Cluster in healthy state" 120 baseline.html
# The overview and the section screens together must render real rows. Wait
# independently for each snapshot: cluster health can already be present when
# the Backup is created, and the overview intentionally summarizes rather than
# repeating the section tables.
wait_path_contains "/cluster/pods" "orders-1" 120 baseline-pods.html
wait_path_contains "/backups/objects" "manual-1" 120 baseline-backups.html
# The overview absorbed the conditions table the separate status screen
# used to carry, and the Events screen was removed outright, so both are
# read here rather than on screens that no longer exist.
grep -qF "orders-1" "$OUT/baseline.html" || { log "overview misses: orders-1"; exit 1; }
grep -qF "Conditions" "$OUT/baseline.html" || { log "overview misses the conditions table"; exit 1; }
grep -qF "<td>Ready</td>" "$OUT/baseline.html" || { log "overview misses the Ready condition"; exit 1; }
grep -qF "source: operator-reported" "$OUT/baseline.html" || { log "overview misses operator attribution"; exit 1; }
grep -qF "current — age" "$OUT/baseline-pods.html" || { log "pods screen misses current snapshot age"; exit 1; }
grep -qF "source: kubernetes-observed" "$OUT/baseline-pods.html" || { log "pods screen misses Kubernetes attribution"; exit 1; }
grep -qF "manual-1" "$OUT/baseline-backups.html" || { log "backup objects screen misses: manual-1"; exit 1; }
grep -qF "source: operator-reported" "$OUT/baseline-backups.html" || { log "backup objects screen misses operator attribution"; exit 1; }
grep -qF 'href="https://viewer.example.com/repo"' "$OUT/baseline.html" || { log "configured evidence link-out not rendered"; exit 1; }
grep -qE 'verified|restorable' "$OUT/baseline.html" && { log "prohibited language rendered"; exit 1; }
log "baseline ok (verdict, conditions, pods, backups, link-out all render)"

log "log tail: poweruser-gated, bounded, live fetch for a verified member"
# The tail sits above the baseline: a view-level user is refused.
view_tail=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: view" "$base/logs/orders-1")
[ "$view_tail" = "403" ] || { log "view-level log tail = $view_tail, want 403"; exit 1; }
tail_status=$(curl -s -o "$OUT/logtail.html" -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: poweruser" "$base/logs/orders-1")
[ "$tail_status" = "200" ] || { log "log tail status = $tail_status"; exit 1; }
grep -qF 'pre class="logs"' "$OUT/logtail.html" || { log "log tail page misses the content block"; exit 1; }
grep -qiF "postgres" "$OUT/logtail.html" || { log "log tail carries no PostgreSQL output"; exit 1; }
grep -qF "fetched on demand, never stored" "$OUT/logtail.html" || { log "log bounds line missing"; exit 1; }
miss_status=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: poweruser" "$base/logs/orders-1-missing")
[ "$miss_status" = "404" ] || { log "nonexistent pod tail = $miss_status, want 404"; exit 1; }
log "log tail ok (view 403, poweruser 200 with real output, nonexistent 404)"

log "demonstration 1: killing the API server breaks every watch"
docker exec "${CLUSTER_NAME}-control-plane" pkill -f kube-apiserver
wait_page_contains "stale — age" 120 stale.html
grep -qF "Cluster in healthy state" "$OUT/stale.html" || { log "last-good content not retained while stale"; exit 1; }
log "staleness visible, last-good retained"

i=0
until kubectl get --raw /healthz > /dev/null 2>&1; do
  i=$((i + 2)); [ "$i" -le 120 ] || { log "api server did not return"; exit 1; }; sleep 2
done
wait_page_contains "current — age" 120 recovered.html
log "recovery restored freshness"

log "demonstration 2: removing RBAC leaves retained, stale, degraded panels"
kubectl -n payments delete rolebinding pgconsole-orders-read > /dev/null
docker exec "${CLUSTER_NAME}-control-plane" pkill -f kube-apiserver
i=0
until kubectl get --raw /healthz > /dev/null 2>&1; do
  i=$((i + 2)); [ "$i" -le 120 ] || { log "api server did not return"; exit 1; }; sleep 2
done
# Every section must cross into stale. They age past the staleness threshold
# at slightly different moments, so wait until stale is visible AND no section
# still reads "current" — asserting on the first stale section alone races.
i=0
while :; do
  body=$(page)
  if echo "$body" | grep -qF "stale — age" && ! echo "$body" | grep -qF "current — age"; then
    echo "$body" > "$OUT/forbidden.html"; break
  fi
  i=$((i + 2))
  [ "$i" -le 180 ] || { echo "$body" > "$OUT/forbidden.html"; log "sections did not all go stale under forbidden RBAC"; exit 1; }
  sleep 2
done
grep -qF "Cluster in healthy state" "$OUT/forbidden.html" || { log "last-good content lost under forbidden RBAC"; exit 1; }
log "forbidden RBAC: retained last-good, visibly stale, never current"

log "restoring RBAC recovers without restart"
# Demonstration 1 restarted the API server, and kubectl validates a manifest
# client-side by downloading the server's OpenAPI document. That document is
# served again only once the API server has rebuilt its aggregated schema, so
# this apply can fail with "failed to download openapi" for a while after the
# restart — observed twice in CI. Retry until discovery catches up rather than
# failing the run on that race, in the same shape as the custom-resource apply
# below. Output is not silenced, so a genuine failure is visible in the log.
k=0
until kubectl apply -f deploy/kubernetes-example.yaml; do
  k=$((k + 1)); [ "$k" -le 12 ] || { log "RBAC never restored (API server discovery lag)"; exit 1; }
  sleep 5
done
wait_page_contains "current — age" 180 restored.html
log "recovered after RBAC restoration"

readyz_status=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$base/readyz")
[ "$readyz_status" = "200" ] || { log "readyz = $readyz_status after recovery"; exit 1; }

log "route admission: the proxy-asserted level decides, the console probes nothing"
# There is no ungated baseline. Reaching the console is not authorization:
# with no level forwarded every screen refuses, including the index, and
# what is served is the denial page rather than a thinner console.
base_status=$(curl -s -o "$OUT/admission-baseline.html" -w '%{http_code}' --max-time 10 "$base/")
[ "$base_status" = "403" ] || { log "ungated index = $base_status, want 403"; exit 1; }
grep -qF "identity required" "$OUT/admission-baseline.html" || { log "ungated index denial misses its message"; exit 1; }
grep -qF "Cluster in healthy state" "$OUT/admission-baseline.html" && { log "ungated index leaked cluster state"; exit 1; }

# The lowest usable level reaches the overviews, and nothing above them.
view_index=$(curl -s -o "$OUT/admission-view.html" -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: view" "$base/")
[ "$view_index" = "200" ] || { log "view-level index = $view_index, want 200"; exit 1; }
view_pods=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: view" "$base/cluster/pods")
[ "$view_pods" = "403" ] || { log "view-level pod roster = $view_pods, want 403"; exit 1; }

# The identity line reflects the proxy-asserted user and level verbatim,
# never the user's own RBAC.
curl -s -o "$OUT/admission-identity.html" --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: dba" "$base/" > /dev/null
grep -qF "dba (proxy-asserted)" "$OUT/admission-identity.html" || { log "identity line missing the proxy-asserted level"; exit 1; }
grep -qF "fanch" "$OUT/admission-identity.html" || { log "identity line missing the user"; exit 1; }

# A gated route (the log tail) refuses a missing identity outright.
noid=$(curl -s -o "$OUT/admission-noid.html" -w '%{http_code}' --max-time 10 \
  -H "X-PgToolBox-Level: poweruser" "$base/logs/orders-1")
[ "$noid" = "403" ] || { log "no-identity gated route = $noid, want 403"; exit 1; }
grep -qF "identity required" "$OUT/admission-noid.html" || { log "no-identity denial misses its message"; exit 1; }

# ...and refuses an insufficient level when an identity is present.
low=$(curl -s -o "$OUT/admission-low.html" -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: view" "$base/logs/orders-1")
[ "$low" = "403" ] || { log "view-level gated route = $low, want 403"; exit 1; }
grep -qF "not authorized" "$OUT/admission-low.html" || { log "insufficient-level denial misses its message"; exit 1; }

# An unrecognized level parses to none, which now reaches nothing: it is
# refused on the index exactly as it is on a gated route, rather than
# falling back to a readable baseline.
junk=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: superuser" "$base/logs/orders-1")
[ "$junk" = "403" ] || { log "unknown-level gated route = $junk, want 403"; exit 1; }
junk_index=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: superuser" "$base/")
[ "$junk_index" = "403" ] || { log "unknown-level index = $junk_index, want 403"; exit 1; }
log "route admission ok (nothing ungated, identity proxy-asserted, view reaches overviews only, unknown level reaches nothing)"

log "day-2 operations: level-gated, confirmed, audited, RBAC-bounded"
# Enable operations; the operations Role is deliberately not applied yet,
# so RBAC must block the mutation even with the flag on and the level met.
kubectl -n payments set env deployment/pgconsole-orders ALLOW_OPERATIONS=true > /dev/null
settle_console_rollout "after enabling operations"

# Operations require the dba level: view is refused, and so is poweruser,
# which reaches every read screen and stops short of the day-2 surface.
ops_view=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: view" "$base/operations")
[ "$ops_view" = "403" ] || { log "view-level operations index = $ops_view, want 403"; exit 1; }
ops_power=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: poweruser" "$base/operations")
[ "$ops_power" = "403" ] || { log "poweruser operations index = $ops_power, want 403"; exit 1; }

# The operations index exists now for the dba.
ops_status=$(curl -s -o "$OUT/operations.html" -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: dba" "$base/operations")
[ "$ops_status" = "200" ] || { log "operations index = $ops_status, want 200"; exit 1; }
grep -qF "Restart cluster" "$OUT/operations.html" || { log "operations index misses an operation"; exit 1; }

# Fetch the confirmation form and its CSRF token as the dba.
curl -s -c "$OUT/cookies" -o "$OUT/confirm.html" --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: dba" "$base/operations/reload" > /dev/null
token=$(sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' "$OUT/confirm.html")
[ -n "$token" ] || { log "no CSRF token in the confirmation form"; exit 1; }

# A POST with the level met but no token is refused: CSRF is independent
# of the level gate.
noconf=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: dba" -X POST "$base/operations/reload")
[ "$noconf" = "403" ] || { log "tokenless POST = $noconf, want 403"; exit 1; }

# With the token, the operation is attempted — but RBAC has no write
# rule yet, so the operator rejects it: fire-and-observe surfaces the
# not-accepted outcome, and nothing was changed.
rbac_blocked=$(curl -s -o "$OUT/op-rbacblocked.html" -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: dba" \
  -X POST --data "csrf=${token}" "$base/operations/reload")
grep -qF "not accepted" "$OUT/op-rbacblocked.html" || { log "RBAC-blocked op did not surface refusal (status $rbac_blocked)"; exit 1; }
log "operations: level met and confirmed, but RBAC alone blocks the mutation"

# Apply the operations Role and retry: now the reload is accepted.
kubectl apply -f deploy/operations-role.yaml > /dev/null
i=0
while :; do
  curl -s -o "$OUT/confirm2.html" --max-time 10 \
    -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: dba" "$base/operations/reload" > /dev/null
  token=$(sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' "$OUT/confirm2.html")
  accepted=$(curl -s -o "$OUT/op-accepted.html" -w '%{http_code}' --max-time 10 \
    -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: dba" \
    -X POST --data "csrf=${token}" "$base/operations/reload")
  if [ "$accepted" = "200" ] && grep -qF "accepted" "$OUT/op-accepted.html"; then
    break
  fi
  i=$((i + 3)); [ "$i" -le 30 ] || { log "reload not accepted after granting RBAC (status $accepted)"; exit 1; }; sleep 3
done

# The operator actually recorded the reload annotation.
reload_at=$(kubectl -n payments get cluster orders -o jsonpath='{.metadata.annotations.cnpg\.io/reloadedAt}')
[ -n "$reload_at" ] || { log "operator did not record the reload annotation"; exit 1; }

# The audit line names the operation and outcome, without leaking.
kubectl -n payments logs deployment/pgconsole-orders --tail=200 | grep -F '"msg":"operation"' > "$OUT/op-audit.log" || true
grep -qF '"operation":"reload"' "$OUT/op-audit.log" || { log "operation audit line missing"; exit 1; }
grep -qF '"outcome":"accepted"' "$OUT/op-audit.log" || { log "operation audit outcome missing"; exit 1; }
log "operations ok (index 200, tokenless 403, RBAC-blocked refusal, granted+confirmed reload recorded and audited)"

log "access-request review: dba-gated panel over the pgToolBox CRDs"
# Stand in for the operator's CRD and seed one pending request, then
# enable the review panel and its Role. The approval picker needs no
# seeded objects: it offers the closed level set from the console itself.
kubectl apply -f hack/testdata/pgtoolbox-crds.yaml
kubectl wait --for=condition=established --timeout=60s \
  crd/pgtoolboxaccessrequests.pgtoolbox.fyannk.dev
# The Established condition can precede the API server actually serving the new
# kinds, so retry the custom-resource apply until discovery catches up rather
# than failing the run on that race. Output is not silenced, so a genuine
# failure is visible in the log.
j=0
until kubectl apply -f hack/testdata/pgtoolbox-samples.yaml; do
  j=$((j + 1)); [ "$j" -le 12 ] || { log "sample review objects never applied (CRD discovery lag)"; exit 1; }
  sleep 5
done
kubectl apply -f deploy/access-review-role.yaml
kubectl -n payments set env deployment/pgconsole-orders ALLOW_ACCESS_REVIEW=true > /dev/null
settle_console_rollout "after enabling review"

# The panel requires dba: a poweruser is refused.
rev_low=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: poweruser" "$base/access-requests")
[ "$rev_low" = "403" ] || { log "poweruser review = $rev_low, want 403"; exit 1; }

# dba reaches it; the observed pending request and the level picker
# render once the collector has seeded.
i=0
while :; do
  rev_dba=$(curl -s -o "$OUT/review.html" -w '%{http_code}' --max-time 10 \
    -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: dba" "$base/access-requests")
  if [ "$rev_dba" = "200" ] && grep -qF "alice" "$OUT/review.html"; then
    break
  fi
  i=$((i + 2)); [ "$i" -le 60 ] || { log "dba review = $rev_dba without the pending request"; exit 1; }; sleep 2
done
# The picker is the console's own closed ladder, so every level must be
# offered regardless of what the API server holds.
for level in view poweruser dba; do
  grep -qF "value=\"$level\"" "$OUT/review.html" ||
    { log "review panel misses the $level picker option"; exit 1; }
done
log "review ok (poweruser 403, dba 200 with the pending request and all three levels offered)"

# The decision write: the only mutation this panel performs, and the one
# the Role exists to authorize. Reading the panel proves nothing about
# it. The write is a merge patch, so RBAC requires the patch verb; a Role
# granting update instead is refused by the API server, the console
# reports 502, and every read asserted above still passes.
approve_token=$(sed -n '/action="\/access-requests\/alice-orders\/approve"/,/<\/form>/p' "$OUT/review.html" \
  | sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' | head -1)
[ -n "$approve_token" ] || { log "approve form carries no CSRF token"; exit 1; }
decide=$(curl -s -o "$OUT/decision.html" -w '%{http_code}' --max-time 15 \
  -H "X-Forwarded-User: fanch" -H "X-PgToolBox-Level: dba" \
  -X POST --data "csrf=${approve_token}" --data "level=poweruser" \
  "$base/access-requests/alice-orders/approve")
[ "$decide" = "200" ] || { log "approve = $decide, want 200"; exit 1; }

# The console reporting success is not the fact; the request status is.
i=0
until [ "$(kubectl -n payments get pgtoolboxaccessrequest alice-orders \
    -o jsonpath='{.status.state}' 2>/dev/null)" = "approved" ]; do
  i=$((i + 2)); [ "$i" -le 30 ] || { log "decision never reached the request status"; exit 1; }; sleep 2
done
log "decision ok (approve 200, request status recorded as approved)"

log "end-to-end test passed; artifacts in $OUT/"
