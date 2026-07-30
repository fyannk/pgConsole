#!/bin/sh
# Restricted-runtime test: the image serves health endpoints under an
# arbitrary UID with a read-only root filesystem, dropped capabilities,
# and no privilege escalation; readiness stays honest; SIGTERM drains
# gracefully.
set -eu

image="${1:-pgconsole:dev}"
name="pgconsole-test-$$"

cleanup() { docker rm -f "$name" > /dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

docker run -d --name "$name" \
  --user 12345:12345 \
  --read-only \
  --tmpfs /tmp \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  -e CLUSTER_NAME=orders \
  -e NAMESPACE=payments \
  -p 127.0.0.1:0:3000 \
  "$image" > /dev/null

port=$(docker port "$name" 3000/tcp | head -n1 | sed 's/.*://')

for i in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:${port}/healthz" > /dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

health=$(curl -fsS "http://127.0.0.1:${port}/healthz")
[ "$health" = "ok" ] || { echo "healthz body unexpected" >&2; exit 1; }

ready_status=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/readyz")
[ "$ready_status" = "503" ] || { echo "readyz must report 503 without an API probe, got $ready_status" >&2; exit 1; }

ready_body=$(curl -s "http://127.0.0.1:${port}/readyz")
[ "$ready_body" = "not ready" ] || { echo "readyz body must be constant" >&2; exit 1; }

docker stop -t 20 "$name" > /dev/null
exit_code=$(docker inspect -f '{{.State.ExitCode}}' "$name")
[ "$exit_code" = "0" ] || { echo "SIGTERM shutdown was not graceful (exit $exit_code)" >&2; exit 1; }

echo "container test passed (arbitrary uid, read-only root, honest readiness, graceful SIGTERM)"
