#!/bin/sh
# Browser tests for the console UI. Starts the fixture harness in
# internal/web/uiharness_test.go, drives it with headless Chromium via
# hack/uitest/drive.js, then stops the harness.
#
# What this covers that the Go tests cannot: whether the embedded
# enhancement layer actually runs under the served Content-Security-
# Policy, whether colour contrast clears WCAG AA in both schemes, and
# whether the tables survive a 375px viewport. Those are properties of
# the served page in an engine, not of a rendered string.
#
# Hermetic: no cluster, no network beyond the loopback, fixture data and
# the same fixed clock the unit tests use.
#
# Artifacts land in artifacts/ui/.
set -eu

GO="${GO:-go}"
PORT_BASE="${PGCONSOLE_UI_PORT_BASE:-18090}"
OUT="${PGCONSOLE_UI_ARTIFACTS:-artifacts/ui}"
HERE=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH= cd -- "$HERE/.." && pwd)

mkdir -p "$OUT"
log() { echo "[ui] $*"; }

harness_pid=""
cleanup() {
  if [ -n "$harness_pid" ] && kill -0 "$harness_pid" 2>/dev/null; then
    log "stopping harness ($harness_pid)"
    kill -TERM "$harness_pid" 2>/dev/null || true
    # Give it a moment to close its listeners before forcing.
    i=0
    while [ "$i" -lt 50 ] && kill -0 "$harness_pid" 2>/dev/null; do
      i=$((i + 1))
      sleep 0.1
    done
    kill -KILL "$harness_pid" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

cd "$ROOT"

log "installing browser-side dependencies"
cd "$HERE/uitest"
if [ -f package-lock.json ]; then
  npm ci
else
  npm install
fi
# Reuses a cached download when the revision already matches.
# PGCONSOLE_UI_INSTALL_DEPS=1 additionally installs Chromium's system
# libraries via apt; that needs root, so CI opts in and a developer
# machine does not.
if [ "${PGCONSOLE_UI_INSTALL_DEPS:-0}" = "1" ]; then
  npx --no-install playwright install --with-deps chromium
else
  npx --no-install playwright install chromium
fi
cd "$ROOT"

log "starting fixture harness on ports $PORT_BASE-$((PORT_BASE + 3))"
PGCONSOLE_UI_PORT_BASE="$PORT_BASE" \
  $GO test -tags=uiharness ./internal/web/ -run TestUIHarness -count=1 -v -timeout 12m \
  > "$OUT/harness.log" 2>&1 &
harness_pid=$!

log "waiting for the harness to accept connections"
ready=false
i=0
while [ "$i" -lt 120 ]; do
  i=$((i + 1))
  if ! kill -0 "$harness_pid" 2>/dev/null; then
    log "harness exited before becoming ready:"
    cat "$OUT/harness.log"
    exit 1
  fi
  all=true
  p=0
  while [ "$p" -lt 4 ]; do
    if ! curl -fsS --max-time 2 "http://127.0.0.1:$((PORT_BASE + p))/healthz" >/dev/null 2>&1; then
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
  cat "$OUT/harness.log"
  exit 1
fi
log "harness ready"

status=0
PGCONSOLE_UI_PORT_BASE="$PORT_BASE" \
  PGCONSOLE_UI_ARTIFACTS="$ROOT/$OUT" \
  node "$HERE/uitest/drive.js" || status=$?

if [ "$status" -eq 0 ]; then
  log "all checks passed; artifacts in $OUT"
else
  log "checks failed (exit $status); see $OUT/summary.txt and the screenshots"
fi
exit "$status"
