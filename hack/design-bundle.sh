#!/bin/sh
# Regenerate the Claude Design bundle from the console this tree builds.
#
# The design project is meant to be the authority on the console's UI,
# which only holds if it describes the console that actually exists. Kept
# by hand it drifts: pages captured before a layout change keep the old
# layout, and nothing says which ones. So it is not kept by hand. Every
# page here is the real rendered output of the real templates, fetched
# over HTTP from the fixture harness in internal/web/uiharness_test.go —
# the same harness `make test-ui` drives, with the same fixed clock and
# the same fixture data, so a bundle is reproducible and hermetic.
#
# What the script adds on top of the served bytes is only what a static
# bundle needs: the asset URLs and the inter-page links are rewritten
# from routes to file names, and each page gets the @dsCard marker the
# Design System pane builds its index from. The markup itself is
# untouched — if a page looks wrong in the bundle, it looks wrong in the
# console.
#
# The harness serves each fixture state twice: a baseline build with
# operations and access review switched off, and an authorized build
# with every capability wired. The bundle is captured from the
# authorized ports, because a design system should describe the whole
# console; the baseline sidebar, with its inert entries, is captured
# once as its own card.
#
# Output lands in artifacts/design/ ready to upload. This script does not
# upload: pushing to the design project is a separate, deliberate step.
#
# Usage:  hack/design-bundle.sh
# Environment: GO, PGCONSOLE_UI_PORT_BASE, PGCONSOLE_DESIGN_OUT.
set -eu

GO="${GO:-go}"
PORT_BASE="${PGCONSOLE_UI_PORT_BASE:-18090}"
OUT="${PGCONSOLE_DESIGN_OUT:-artifacts/design}"
HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$HERE/.." && pwd)

# The harness serves five data states, each twice. The baseline copies
# occupy the first five ports (hack/uitest/drive.js addresses those by
# offset); the authorized copies follow, which is what AUTH shifts to.
STATES=8
AUTH=$STATES
HEALTHY=$((AUTH + 0))
STALE=$((AUTH + 1))
DEGRADED=$((AUTH + 2))
EMPTY=$((AUTH + 3))
ABSENT=$((AUTH + 4))
QUIET=$((AUTH + 5))
CLUSTERCAT=$((AUTH + 6))
MISSINGCAT=$((AUTH + 7))
PORTS=$((STATES * 2))

log() { echo "[design] $*"; }

cd "$ROOT"

need() { command -v "$1" > /dev/null 2>&1 || { echo "design-bundle needs '$1' on PATH" >&2; exit 1; }; }
need curl
need sed
need python3

STAGE=$(mktemp -d)
harness_pid=""
cleanup() {
  if [ -n "$harness_pid" ] && kill -0 "$harness_pid" 2>/dev/null; then
    kill -TERM "$harness_pid" 2>/dev/null || true
    i=0
    while [ "$i" -lt 50 ] && kill -0 "$harness_pid" 2>/dev/null; do
      i=$((i + 1))
      sleep 0.1
    done
    kill -KILL "$harness_pid" 2>/dev/null || true
  fi
  rm -rf "$STAGE"
}
trap cleanup EXIT INT TERM

rm -rf "$OUT"
mkdir -p "$OUT/pages"

# Compiled first and run directly, rather than backgrounding `go test`:
# `go test` runs the binary as a child, so $! would be the wrapper and
# the SIGTERM in cleanup would leave the server holding its ports. A
# survivor then answers the next run from a stale build, which looks
# exactly like a bundle that ignored your edits.
# Refuse to start on top of somebody else's harness. The readiness check
# below only asks whether a port answers, which a survivor from an
# earlier run does — and then the capture silently mixes two harnesses
# with different state lists, so a page named "empty" holds populated
# data. That is a wrong bundle that looks right, which is worse than a
# failed run.
busy=""
p=0
while [ "$p" -lt "$PORTS" ]; do
  if curl -fsS --max-time 1 "http://127.0.0.1:$((PORT_BASE + p))/healthz" > /dev/null 2>&1; then
    busy="$busy $((PORT_BASE + p))"
  fi
  p=$((p + 1))
done
if [ -n "$busy" ]; then
  log "ports already answering:$busy"
  log "another fixture harness is running; stop it before capturing a bundle"
  exit 1
fi

log "building the fixture harness"
$GO test -c -tags=uiharness -o "$STAGE/uiharness.test" ./internal/web/

log "starting the fixture harness on ports $PORT_BASE-$((PORT_BASE + PORTS - 1))"
PGCONSOLE_UI_PORT_BASE="$PORT_BASE" \
  "$STAGE/uiharness.test" -test.run TestUIHarness -test.count=1 -test.v -test.timeout 12m \
  > "$STAGE/harness.log" 2>&1 &
harness_pid=$!

log "waiting for the harness to accept connections"
ready=false
i=0
while [ "$i" -lt 120 ]; do
  i=$((i + 1))
  if ! kill -0 "$harness_pid" 2>/dev/null; then
    log "harness exited before becoming ready:"
    cat "$STAGE/harness.log"
    exit 1
  fi
  all=true
  p=0
  while [ "$p" -lt "$PORTS" ]; do
    if ! curl -fsS --max-time 2 "http://127.0.0.1:$((PORT_BASE + p))/healthz" > /dev/null 2>&1; then
      all=false
      break
    fi
    p=$((p + 1))
  done
  if [ "$all" = true ]; then
    ready=true
    break
  fi
  sleep 0.5
done
if [ "$ready" != true ]; then
  log "harness never became ready:"
  cat "$STAGE/harness.log"
  exit 1
fi

# get <offset> <route> <level> <user> — fetch one page to stdout. An
# empty level or user omits that header, which is how the un-proxied
# case is captured.
get() {
  _off=$1; _route=$2; _level=$3; _user=$4
  set -- -fsS --max-time 15
  if [ -n "$_level" ]; then set -- "$@" -H "X-PgToolBox-Level: $_level"; fi
  if [ -n "$_user" ]; then set -- "$@" -H "X-Forwarded-User: $_user"; fi
  curl "$@" "http://127.0.0.1:$((PORT_BASE + _off))$_route"
}

# refused <offset> <route> <level> <user> <want> — fetch a page whose
# whole point is that it is not a 200. curl -f would discard the body,
# and the refusal body is the screen being captured, so this checks the
# status itself and fails on anything but the expected one.
refused() {
  _off=$1; _route=$2; _level=$3; _user=$4; _want=$5
  _code=$(curl -sS --max-time 15 -o "$STAGE/refused.html" -w '%{http_code}' \
    -H "X-PgToolBox-Level: $_level" -H "X-Forwarded-User: $_user" \
    "http://127.0.0.1:$((PORT_BASE + _off))$_route")
  if [ "$_code" != "$_want" ]; then
    log "$_route returned $_code, expected $_want"
    exit 1
  fi
  cat "$STAGE/refused.html"
}

# rewrite maps the served page onto the bundle: routes become file
# names, /static/ becomes the sibling asset. Ordered most specific
# first, so /backups/objects is not eaten by /backups. Quotes are part
# of every pattern, so a rewrite only ever fires on a whole attribute
# value and never inside prose.
rewrite() {
  sed \
    -e 's|/static/app\.css|console.css|g' \
    -e 's|/static/console\.js|console.js|g' \
    -e 's|/static/topology-force\.js|topology-force.js|g' \
    -e 's|/static/alpine\.csp\.js|alpine.csp.js|g' \
    -e 's|/static/htmx-2\.0\.10\.min\.js|htmx-2.0.10.min.js|g' \
    -e 's|/static/history-timeline\.js|history-timeline.js|g' \
    -e 's|/static/logo\.png|logo.png|g' \
    -e 's|/static/uplot-1\.6\.32\.min\.js|uplot-1.6.32.min.js|g' \
    -e 's|/static/uplot-1\.6\.32\.min\.css|uplot-1.6.32.min.css|g' \
    -e 's|/static/metrics-charts\.js|metrics-charts.js|g' \
    -e 's|/static/favicon\.svg|favicon.svg|g' \
    -e 's|action="/operations/[^"]*"|action="operations-result.html"|g' \
    -e 's|action="/access-requests/[^"]*/approve"|action="access-result.html"|g' \
    -e 's|action="/access-requests/[^"]*/deny"|action="access-result.html"|g' \
    -e 's|"/cluster/overview"|"cluster-overview.html"|g' \
    -e 's|"/cluster/metrics"|"cluster-metrics.html"|g' \
    -e 's|"/cluster/status"|"cluster-status.html"|g' \
    -e 's|"/cluster/pods/[^"]*"|"pod-details.html"|g' \
    -e 's|"/cluster/pods"|"cluster-pods.html"|g' \
    -e 's|"/cluster/events"|"cluster-events.html"|g' \
    -e 's|"/backups/objects"|"backups-objects.html"|g' \
    -e 's|"/backups/evidence"|"backups-evidence.html"|g' \
    -e 's|"/backups"|"backups-overview.html"|g' \
    -e 's|"/databases/roles"|"databases-roles.html"|g' \
    -e 's|"/databases/publications"|"databases-publications.html"|g' \
    -e 's|"/databases/subscriptions"|"databases-subscriptions.html"|g' \
    -e 's|"/databases"|"databases-overview.html"|g' \
    -e 's|"/poolers/logs/[^"]*"|"poolers-logs.html"|g' \
    -e 's|"/poolers/pods"|"poolers-pods.html"|g' \
    -e 's|"/poolers/logs"|"poolers-logs.html"|g' \
    -e 's|"/poolers"|"poolers-overview.html"|g' \
    -e 's|"/history/revisions/[^"]*"|"history-revision.html"|g' \
    -e 's|"/history?[^"]*"|"history-timeline.html"|g' \
    -e 's|"/history"|"history-timeline.html"|g' \
    -e 's|"/operations/[^"]*"|"operations-confirm.html"|g' \
    -e 's|"/operations"|"operations-index.html"|g' \
    -e 's|"/access-requests"|"access-requests.html"|g' \
    -e 's|href="/"|href="index-healthy.html"|g'
}

# emit <file> <group> <name> <subtitle> — card marker, then the
# rewritten page, from stdin.
emit() {
  _file=$1; _group=$2; _name=$3; _sub=$4
  {
    printf '<!-- @dsCard group="%s" name="%s" subtitle="%s" -->\n' "$_group" "$_name" "$_sub"
    rewrite
  } > "$OUT/pages/$_file"
  log "  pages/$_file"
}

# page <offset> <route> <level> <user> <file> <group> <name> <subtitle>
page() {
  get "$1" "$2" "$3" "$4" | emit "$5" "$6" "$7" "$8"
}

log "capturing the shared assets"
cp internal/web/static/app.css "$OUT/pages/console.css"
cp internal/web/static/console.js "$OUT/pages/console.js"
cp internal/web/static/topology-force.js "$OUT/pages/topology-force.js"
cp internal/web/static/alpine.csp.js "$OUT/pages/alpine.csp.js"
cp internal/web/static/htmx-2.0.10.min.js "$OUT/pages/htmx-2.0.10.min.js"
cp internal/web/static/history-timeline.js "$OUT/pages/history-timeline.js"
cp internal/web/static/favicon.svg "$OUT/pages/favicon.svg"
cp internal/web/static/logo.png "$OUT/pages/logo.png"
cp internal/web/static/uplot-1.6.32.min.js "$OUT/pages/uplot-1.6.32.min.js"
cp internal/web/static/uplot-1.6.32.min.css "$OUT/pages/uplot-1.6.32.min.css"
cp internal/web/static/metrics-charts.js "$OUT/pages/metrics-charts.js"

log "capturing the pages"

# The Overview, once per data state. These are the states whose
# presentation genuinely differs, which is what makes each worth a card.
page $HEALTHY  / "" "" index-healthy.html Pages \
  "Overview — healthy" "Plain-language summary of a healthy cluster"
page $STALE    / "" "" index-stale.html Pages \
  "Overview — stale" "Broken watch: last-good content retained, staleness visible"
page $DEGRADED / "" "" index-degraded.html Pages \
  "Overview — degraded" "Sections stale and the repository evidence unavailable"
page $EMPTY    / "" "" index-no-snapshot.html Pages \
  "Overview — no snapshot" "Cold start: unknown shell, never presented as healthy"
page $ABSENT   / "" "" index-absent.html Pages \
  "Overview — cluster absent" "Deleted cluster renders explicit absence, not an error"
page $HEALTHY  / dba fanch index-identity.html Pages \
  "Overview — with identity" "Proxy-asserted user and authorization level in the target list"

# The section screens, all from the healthy state.
page $HEALTHY /cluster/overview "" "" cluster-overview.html Pages \
  "Cluster — overview" "Power-user wiring: placement, replication and the backup path"
page $HEALTHY /cluster/status   "" "" cluster-status.html Pages \
  "Cluster — status" "Verdict, topology and conditions"
page $HEALTHY /cluster/pods     "" "" cluster-pods.html Pages \
  "Cluster — pods" "Instance pods as observed"
page $HEALTHY /cluster/events   "" "" cluster-events.html Pages \
  "Cluster — events" "Recent Kubernetes events"
page $HEALTHY /cluster/metrics  "" "" cluster-metrics.html Pages \
  "Cluster — metrics" "Instance metrics: text summaries with chart enhancement"
page $HEALTHY /backups          "" "" backups-overview.html Pages \
  "Backups — overview" "Recency, schedule and retention"
page $HEALTHY /backups/objects  "" "" backups-objects.html Pages \
  "Backups — objects" "Backup and ScheduledBackup resources"
page $HEALTHY /backups/evidence "" "" backups-evidence.html Pages \
  "Backups — evidence" "Repository evidence and cross-check"
page $HEALTHY /databases               "" "" databases-overview.html Pages \
  "Databases — declared" "Declared databases and the operator's reconciliation verdict"
page $HEALTHY /databases/roles         "" "" databases-roles.html Pages \
  "Databases — roles" "Declared roles, their attributes and how they authenticate"
page $HEALTHY /databases/publications  "" "" databases-publications.html Pages \
  "Databases — publications" "Declared logical-replication publications"
page $HEALTHY /databases/subscriptions "" "" databases-subscriptions.html Pages \
  "Databases — subscriptions" "Declared logical-replication subscriptions"
page $HEALTHY /poolers          "" "" poolers-overview.html Pages \
  "Poolers — overview" "Connection poolers the operator reports for this cluster"
page $HEALTHY /poolers/pods poweruser operator poolers-pods.html Pages \
  "Poolers — pods" "Pods run by the cluster's poolers, membership proven by ownership"
page $HEALTHY /poolers/logs poweruser operator poolers-logs.html Pages \
  "Poolers — logs" "Choosing a pooler pod to tail its pgbouncer container"

# The level-gated screens. The tail needs poweruser; the review panel
# needs dba; the refusal is the same route asked for below its level.
page $HEALTHY /cluster/pods/orders-1 poweruser operator pod-details.html Pages \
  "Pod detail" "Status, history and logs for one instance pod, with the raw definition in a modal"
page $HEALTHY /operations poweruser operator operations-index.html Pages \
  "Operations — catalog" "Available operations for the authorized level"
# Promote is the operation that targets a named instance, so its
# confirmation carries the fuller form. The instance comes in on the
# query the catalog link builds, and the minted token is bound to it.
page $HEALTHY "/operations/promote?instance=orders-2" poweruser operator \
  operations-confirm.html Pages \
  "Operations — confirm" "Explicit confirmation with CSRF token and typed target"
page $HEALTHY /access-requests dba dba@corp access-requests.html Pages \
  "Access requests — queue" "Pending and already-decided requests with approve/deny forms"

# The object-definition history. The timeline is baseline metadata, but
# it is captured at poweruser so the revision links render; the revision
# detail requires that level, and its refusal is a designed screen.
page $HEALTHY /history poweruser operator history-timeline.html Pages \
  "Object history — timeline" "Observed revisions of watched object definitions, newest first"
page $HEALTHY /history/revisions/2 poweruser operator history-revision.html Pages \
  "Object history — revision" "One scrubbed retained definition with its bounded structural diff"

# A refusal is a designed screen, not an error page: the route exists,
# the level does not clear it, and the reason is stated.
refused $HEALTHY /operations view viewer 403 \
  | emit denied.html Pages \
      "Denied" "Level gate refusing a route, with the reason stated"
refused $HEALTHY /history/revisions/2 view viewer 403 \
  | emit history-revision-gated.html Pages \
      "Object history — revision denied" "A retained definition refused below the poweruser level"

# Observed and empty. Every "there are none" branch the console can take
# is a distinct claim from "nothing observed yet", worded differently on
# purpose, and a design that never sees it cannot check the wording or
# the layout of a screen with no rows in it.
page $QUIET /cluster/pods    "" "" cluster-pods-empty.html Pages \
  "Cluster — pods, none" "Observed and empty: no instance pods reported"
page $QUIET /cluster/events  "" "" cluster-events-empty.html Pages \
  "Cluster — events, none" "Observed and empty: no events in the window"
page $QUIET /cluster/status  "" "" cluster-status-empty.html Pages \
  "Cluster — status, no quorum or catalog" "Failover quorum not configured; image named directly rather than drawn from a catalog"
page $QUIET /backups         "" "" backups-overview-empty.html Pages \
  "Backups — none" "Observed and empty: no backups reported"
page $QUIET /backups/evidence "" "" backups-evidence-silent.html Pages \
  "Backups — evidence, sidecar silent" "The consumer is configured and the sidecar has not answered, which is not the same as unconfigured"
page $QUIET /databases       "" "" databases-empty.html Pages \
  "Databases — none declared" "Observed and empty: the cluster declares no Database resources"
page $QUIET /databases/roles "" "" databases-roles-empty.html Pages \
  "Databases — no roles declared" "Observed and empty: the cluster declares no DatabaseRole resources"
page $QUIET /databases/publications "" "" databases-publications-empty.html Pages \
  "Databases — no publications declared" "Observed and empty: no Publication resources for logical replication"
page $QUIET /databases/subscriptions "" "" databases-subscriptions-empty.html Pages \
  "Databases — no subscriptions declared" "Observed and empty: no Subscription resources for logical replication"
page $QUIET /poolers         "" "" poolers-empty.html Pages \
  "Poolers — none" "Observed and empty: the operator reports no Pooler resources"
page $QUIET /poolers/pods poweruser operator poolers-pods-empty.html Pages \
  "Poolers — no pods" "Observed and empty: no pod owned by one of this cluster's poolers"

# Nothing observed at all: the sections say the build makes no claim,
# which is a third wording distinct from both "none" and a populated
# list, and until now only the Overview had a card for it.
page $EMPTY /databases    "" "" databases-no-snapshot.html Pages \
  "Databases — nothing observed" "No declaration snapshot: the build makes no claim either way"
page $EMPTY /poolers      "" "" poolers-no-snapshot.html Pages \
  "Poolers — nothing observed" "No pooler snapshot: the build makes no claim either way"
page $EMPTY /poolers/pods poweruser operator poolers-pods-no-snapshot.html Pages \
  "Poolers — pods, nothing observed" "No pooler pod snapshot: the build makes no claim either way"

page $MISSINGCAT /cluster/status "" "" cluster-status-missing-catalog.html Pages \
  "Cluster — catalog not found" "The cluster names a catalog the API server reports does not exist"

page $CLUSTERCAT /cluster/status "" "" cluster-status-cluster-catalog.html Pages \
  "Cluster — cluster-scoped catalog" "The image is drawn from a ClusterImageCatalog this deployment does not read; the reference is shown and its content is not claimed"

# The pooler tail's own output, which is a different route to a
# different container from the instance tail beside it.
page $HEALTHY /poolers/logs/orders-rw-pooler-abc-1 poweruser operator poolers-logs-tail.html Pages \
  "Pooler log tail" "Bounded on-demand fetch of a pooler pod's pgbouncer container"

# The baseline build, where operations and access review are switched
# off. Its sidebar carries them as inert entries rather than dropping
# them, which is a deliberate design decision worth its own card.
page 0 / "" "" index-baseline.html Pages \
  "Overview — baseline build" "Capabilities switched off stay visible as inert entries"

# The two outcome screens are POST results, so they have to be driven
# rather than fetched: read the form, submit exactly what it carries.
log "driving the confirm/approve flows"

confirm=$(get $HEALTHY "/operations/promote?instance=orders-2" poweruser operator)
csrf=$(printf '%s\n' "$confirm" | sed -n 's|.*name="csrf" value="\([^"]*\)".*|\1|p' | head -1)
instance=$(printf '%s\n' "$confirm" | sed -n 's|.*name="instance" value="\([^"]*\)".*|\1|p' | head -1)
if [ -z "$csrf" ]; then
  log "could not read the operation CSRF token; is the confirm page still a form?"
  exit 1
fi
curl -fsS --max-time 15 -X POST \
  -H "X-PgToolBox-Level: poweruser" -H "X-Forwarded-User: operator" \
  --data-urlencode "csrf=$csrf" --data-urlencode "instance=$instance" \
  "http://127.0.0.1:$((PORT_BASE + HEALTHY))/operations/promote" \
  | emit operations-result.html Pages \
      "Operations — result" "Outcome of an accepted operation"

queue=$(get $HEALTHY /access-requests dba dba@corp)
req=$(printf '%s\n' "$queue" | sed -n 's|.*action="/access-requests/\([^/]*\)/approve".*|\1|p' | head -1)
rcsrf=$(printf '%s\n' "$queue" | sed -n 's|.*name="csrf" value="\([^"]*\)".*|\1|p' | head -1)
# The first option is the empty "choose a role" placeholder, so the
# pattern requires at least one character inside the quotes.
role=$(printf '%s\n' "$queue" | sed -n 's|.*<option value="\([^"][^"]*\)"[^>]*>.*|\1|p' | head -1)
if [ -z "$req" ] || [ -z "$rcsrf" ] || [ -z "$role" ]; then
  log "could not read the approve form (request=$req role=$role); has the queue markup changed?"
  exit 1
fi
curl -fsS --max-time 15 -X POST \
  -H "X-PgToolBox-Level: dba" -H "X-Forwarded-User: dba@corp" \
  --data-urlencode "csrf=$rcsrf" --data-urlencode "role=$role" \
  "http://127.0.0.1:$((PORT_BASE + HEALTHY))/access-requests/$req/approve" \
  | emit access-result.html Pages \
      "Access requests — decision" "Recorded decision and its attribution"

# The foundations card is generated from the same run: its tokens come
# from the real stylesheet and its component samples are lifted from the
# pages just captured, so it cannot drift from the console the way a
# hand-authored copy did.
log "generating the foundations card"
mkdir -p "$OUT/foundations"
python3 "$HERE/design-foundations.py" internal/web/static/app.css "$OUT/pages" "$OUT/foundations/foundations.html"

log "bundle written to $OUT/pages ($(find "$OUT/pages" -name '*.html' | wc -l | tr -d ' ') pages)"
log "review it, then upload pages/** to the design project"
