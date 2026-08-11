#!/usr/bin/env bash
# dev-problems.sh — a rogues' gallery for the diagnostics catalog.
#
# Stands up six deliberately broken, deliberately tiny CloudNativePG
# clusters beside the healthy dev environment, each in its own
# namespace with its own console — the console watches exactly one
# cluster, so one broken cluster means one console:
#
#     http://localhost:3010   quota       replica PVCs refused by a ResourceQuota
#     http://localhost:3011   s3conflict  WAL archive destination already holds another cluster's data
#     http://localhost:3012   s3denied    object store refuses the credentials
#     http://localhost:3013   walfull     dedicated WAL volume genuinely full (128Mi tmpfs)
#     http://localhost:3014   datafull    single data volume genuinely full (256Mi tmpfs)
#     http://localhost:3015   oom         memory limit too small, kernel kills the instance
#
# Each port is a poweruser-level proxy in front of that namespace's
# console, so /diagnostics is reachable directly.
#
# kind's local-path provisioner enforces no PVC size, so the two
# disk-full scenarios back their volumes with fixed-size tmpfs mounts
# created inside the kind node. tmpfs is RAM and vanishes on node
# restart — these clusters hold nothing anyone can lose.
#
#     hack/dev-problems.sh          stand everything up
#     hack/dev-problems.sh down     tear everything down
#     PROBLEMS="quota oom" hack/dev-problems.sh    a subset
#
# Requires the pgc-dev kind cluster from hack/dev-up.sh, with the
# barman-cloud plugin and MinIO installed (the default dev-up run).

set -u

CONTEXT="${CONTEXT:-kind-pgc-dev}"
NODE="${NODE:-pgc-dev-control-plane}"
IMAGE="${IMAGE:-pgconsole:dev}"
ALL_PROBLEMS="quota s3conflict s3denied walfull datafull oom"
PROBLEMS="${PROBLEMS:-$ALL_PROBLEMS}"
PROXY_BASE=3010
RAW_BASE=4010
WORK="${TMPDIR:-/tmp}/pgc-dev-problems"
mkdir -p "$WORK"

log() { echo "[dev-problems] $*"; }
k() { kubectl --context "$CONTEXT" "$@"; }

port_of() {
  local i=0 p
  for p in $ALL_PROBLEMS; do
    [ "$p" = "$1" ] && { echo $((PROXY_BASE + i)); return; }
    i=$((i + 1))
  done
}
raw_port_of() {
  local i=0 p
  for p in $ALL_PROBLEMS; do
    [ "$p" = "$1" ] && { echo $((RAW_BASE + i)); return; }
    i=$((i + 1))
  done
}

# ---------------------------------------------------------------- down
if [ "${1:-}" = "down" ]; then
  for s in $ALL_PROBLEMS; do
    k delete namespace "pgc-$s" --ignore-not-found --wait=false > /dev/null 2>&1
  done
  k delete pv pgc-walfull-wal pgc-datafull-data --ignore-not-found > /dev/null 2>&1
  pkill -f "port-forward deploy/pgconsole-(quota|s3conflict|s3denied|walfull|datafull|oom)" 2> /dev/null
  pkill -f "dev-problems-proxy" 2> /dev/null
  docker exec "$NODE" sh -c 'umount /mnt/pgc-walfull /mnt/pgc-datafull 2>/dev/null; rm -rf /mnt/pgc-walfull /mnt/pgc-datafull' 2> /dev/null
  log "torn down (namespace deletion finishes in the background)"
  exit 0
fi

# ------------------------------------------------- shared ingredients

# The MinIO credentials the healthy environment already uses.
s3_secret() {
  local ns="$1" access="$2" secret="$3"
  k -n "$ns" create secret generic minio-s3-creds \
    --from-literal=ACCESS_KEY_ID="$access" \
    --from-literal=ACCESS_SECRET_KEY="$secret" \
    --dry-run=client -o yaml | k apply -f - > /dev/null
}

# object_store <ns> <name> <destination> <endpoint>
object_store() {
  local ns="$1" name="$2" dest="$3" endpoint="$4"
  cat <<YAML | k apply -f - > /dev/null
apiVersion: barmancloud.cnpg.io/v1
kind: ObjectStore
metadata:
  name: $name
  namespace: $ns
spec:
  configuration:
    destinationPath: $dest
    endpointURL: $endpoint
    s3Credentials:
      accessKeyId:
        name: minio-s3-creds
        key: ACCESS_KEY_ID
      secretAccessKey:
        name: minio-s3-creds
        key: ACCESS_SECRET_KEY
YAML
}

# barman_plugin <store-name> [serverName] — the plugin stanza for a
# cluster spec. serverName is a plugin parameter, not ObjectStore spec.
barman_plugin() {
  cat <<YAML
  plugins:
    - name: barman-cloud.cloudnative-pg.io
      enabled: true
      isWALArchiver: true
      parameters:
        barmanObjectName: $1${2:+
        serverName: $2}
YAML
}

# tmpfs_pv <mount> <size-mount> <pv-name> <class> <capacity>
tmpfs_pv() {
  local mount="$1" size="$2" pv="$3" class="$4" capacity="$5"
  docker exec "$NODE" sh -c \
    "mkdir -p $mount && { mountpoint -q $mount || mount -t tmpfs -o size=$size tmpfs $mount; }"
  k delete pv "$pv" --ignore-not-found > /dev/null 2>&1
  cat <<YAML | k apply -f - > /dev/null
apiVersion: v1
kind: PersistentVolume
metadata:
  name: $pv
spec:
  capacity:
    storage: $capacity
  accessModes: ["ReadWriteOnce"]
  persistentVolumeReclaimPolicy: Retain
  storageClassName: $class
  hostPath:
    path: $mount
YAML
}

# console <name> — the per-namespace console, templated from the
# shipped example: pgconsole-<name> watching cluster <name> in
# namespace pgc-<name>, with diagnostics and log following on.
console() {
  local name="$1" ns="pgc-$1"
  sed -e "s/payments/$ns/g" -e "s/orders/$name/g" deploy/kubernetes-example.yaml | k apply -f - > /dev/null
  k -n "$ns" delete networkpolicy "pgconsole-$name-default-deny" > /dev/null 2>&1 || true
  k -n "$ns" set env "deployment/pgconsole-$name" \
    ALLOW_DIAGNOSTICS=true ALLOW_LOGS=true LOG_STREAM_ENABLED=true > /dev/null
}

# cluster_apply <name> <spec-yaml> — a Cluster named <name> in pgc-<name>.
cluster_apply() {
  local name="$1" spec="$2"
  cat <<YAML | k apply -f - > /dev/null
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: $name
  namespace: pgc-$name
spec:
$spec
YAML
}

# wait_instance <ns> <pod> — bounded wait for one running instance.
wait_instance() {
  local ns="$1" pod="$2" i=0
  while ! k -n "$ns" exec "$pod" -c postgres -- psql -U postgres -c "select 1" > /dev/null 2>&1; do
    i=$((i + 1))
    [ "$i" -le 60 ] || return 1
    sleep 5
  done
}

# ------------------------------------------------------ the scenarios

setup_quota() {
  local ns="pgc-quota"
  # 1Gi of storage quota against three declared 1Gi instances: the
  # first PVC consumes the whole grant and the second is refused, so
  # the operator cannot create what the cluster needs.
  cat <<YAML | k apply -f - > /dev/null
apiVersion: v1
kind: ResourceQuota
metadata:
  name: tight
  namespace: $ns
spec:
  hard:
    requests.storage: 1Gi
YAML
  cluster_apply quota "  instances: 3
  storage:
    size: 1Gi"
}

setup_s3conflict() {
  local ns="pgc-s3conflict"
  s3_secret "$ns" pgconsoledev pgconsoledev123
  # serverName orders: this cluster tries to archive into the folder
  # the healthy orders cluster already fills, and the first-archive
  # safety check refuses a non-empty destination.
  object_store "$ns" conflict-store s3://pgbackups/orders http://minio.minio.svc:9000
  cluster_apply s3conflict "  instances: 1
  storage:
    size: 512Mi
$(barman_plugin conflict-store orders)"
}

setup_s3denied() {
  local ns="pgc-s3denied"
  s3_secret "$ns" wrong-access-key wrong-secret-key
  object_store "$ns" denied-store s3://pgbackups/s3denied http://minio.minio.svc:9000
  cluster_apply s3denied "  instances: 1
  storage:
    size: 512Mi
$(barman_plugin denied-store)"
}

setup_walfull() {
  local ns="pgc-walfull"
  tmpfs_pv /mnt/pgc-walfull 128m pgc-walfull-wal pgc-tiny-wal 128Mi
  s3_secret "$ns" pgconsoledev pgconsoledev123
  # An unreachable endpoint keeps every WAL segment on the 128Mi tmpfs
  # volume; the burst below then fills it for real.
  object_store "$ns" unreachable-store s3://pgbackups/walfull http://nowhere.invalid:9000
  cluster_apply walfull "  instances: 1
  storage:
    size: 512Mi
  walStorage:
    size: 128Mi
    storageClass: pgc-tiny-wal
$(barman_plugin unreachable-store)"
}

fill_walfull() {
  local ns="pgc-walfull" pod="walfull-1" i
  wait_instance "$ns" "$pod" || { log "walfull: instance never answered, skipping the fill"; return; }
  log "walfull: generating WAL against the dead archive (fills 128Mi)"
  for i in $(seq 1 12); do
    k -n "$ns" exec "$pod" -c postgres -- psql -U postgres -q -c \
      "create table if not exists junk_$i as select g, md5(g::text) t from generate_series(1,200000) g; select pg_switch_wal();" \
      > /dev/null 2>&1 || break
    sleep 2
  done
}

setup_datafull() {
  local ns="pgc-datafull"
  tmpfs_pv /mnt/pgc-datafull 256m pgc-datafull-data pgc-tiny-data 256Mi
  # No separate WAL volume on purpose: the common single-volume layout,
  # where filling the data directory takes the WAL down with it.
  cluster_apply datafull "  instances: 1
  storage:
    size: 256Mi
    storageClass: pgc-tiny-data"
}

fill_datafull() {
  local ns="pgc-datafull" pod="datafull-1" i
  wait_instance "$ns" "$pod" || { log "datafull: instance never answered, skipping the fill"; return; }
  log "datafull: inserting until the 256Mi volume runs dry"
  for i in $(seq 1 20); do
    k -n "$ns" exec "$pod" -c postgres -- psql -U postgres -q -c \
      "create table if not exists junk_$i as select g, md5(g::text) t from generate_series(1,150000) g;" \
      > /dev/null 2>&1 || break
    sleep 1
  done
}

setup_oom() {
  local ns="pgc-oom"
  # 48Mi is not enough to bootstrap PostgreSQL and its instance
  # manager: the kernel kills the container and the kubelet backs off
  # restarting it. (96Mi turned out to be survivable — an idle postgres
  # with CloudNativePG's defaults fits.)
  cluster_apply oom "  instances: 1
  storage:
    size: 512Mi
  resources:
    requests:
      memory: 48Mi
    limits:
      memory: 48Mi"
}

# ------------------------------------------------------------ the run

k cluster-info > /dev/null 2>&1 || { echo "kind cluster '$CONTEXT' unreachable" >&2; exit 1; }

# "ports" relaunches only the forwards and proxies — after a console
# rollout, without touching the broken clusters or their findings.
if [ "${1:-}" = "ports" ]; then
  PORTS_ONLY=true
else
  PORTS_ONLY=false
fi

[ "$PORTS_ONLY" = "true" ] || for s in $PROBLEMS; do
  ns="pgc-$s"
  log "scenario $s — resetting namespace $ns"
  k delete namespace "$ns" --ignore-not-found --wait=true > /dev/null 2>&1
  k create namespace "$ns" > /dev/null
  "setup_$s"
done

# The consoles ride alongside while the clusters break themselves.
[ "$PORTS_ONLY" = "true" ] || for s in $PROBLEMS; do
  console "$s"
done

# Disk fills need a running instance first, so they come last.
[ "$PORTS_ONLY" = "true" ] || case " $PROBLEMS " in *" walfull "*) fill_walfull ;; esac
[ "$PORTS_ONLY" = "true" ] || case " $PROBLEMS " in *" datafull "*) fill_datafull ;; esac

for s in $PROBLEMS; do
  k -n "pgc-$s" rollout status "deployment/pgconsole-$s" --timeout=180s > /dev/null 2>&1 ||
    log "console for $s is not ready yet — the port below will answer once it is"
done

# ----------------------------------------------------- ports and proxies

cat > "$WORK/dev-problems-proxy.go" <<'GO'
// dev-problems-proxy injects the trusted-proxy headers, exactly like
// dev-up's proxy, one instance per broken-cluster console.
package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
)

func main() {
	listen := flag.String("listen", "", "listen address")
	target := flag.String("target", "", "console base URL")
	flag.Parse()
	u, err := url.Parse(*target)
	if err != nil {
		log.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Header.Set("X-Forwarded-User", "operator")
		r.Header.Set("X-PgToolBox-Level", "poweruser")
	}
	log.Printf("dev-problems-proxy %s -> %s", *listen, *target)
	log.Fatal(http.ListenAndServe(*listen, proxy))
}
GO
go build -o "$WORK/dev-problems-proxy" "$WORK/dev-problems-proxy.go"

for s in $PROBLEMS; do
  raw=$(raw_port_of "$s")
  port=$(port_of "$s")
  pkill -f "port-forward deploy/pgconsole-$s" 2> /dev/null
  pkill -f "dev-problems-proxy -listen 127.0.0.1:$port" 2> /dev/null
  k -n "pgc-$s" port-forward "deploy/pgconsole-$s" "$raw:3000" > /dev/null 2>&1 &
  "$WORK/dev-problems-proxy" -listen "127.0.0.1:$port" -target "http://127.0.0.1:$raw" > /dev/null 2>&1 &
done

sleep 3
log "the rogues' gallery is up (poweruser level on every port):"
for s in $PROBLEMS; do
  log "  http://localhost:$(port_of "$s")/diagnostics   $s"
done
log "findings take a few minutes to develop as each cluster breaks itself"
log "tear down with: hack/dev-problems.sh down"
