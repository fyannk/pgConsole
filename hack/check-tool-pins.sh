#!/bin/sh
# Tool pin freshness. Dependabot reads manifests — go.mod, package-lock.json,
# the Dockerfile, action refs — and nothing else. A tool version pinned in a
# Makefile variable is invisible to it and rots silently: GO_VERSION sat at
# 1.26.5 through six disclosed standard-library vulnerabilities, and the
# linter sat three months behind until a toolchain bump forced the issue.
#
# This reports the pins Dependabot cannot see. It is deliberately NOT part of
# `make check`: it needs the network, and it would turn CI red the day
# upstream tags a release, which has nothing to do with the change under
# test. It runs on a schedule instead, and opens a pull request.
#
#   ./hack/check-tool-pins.sh          report drift, exit 1 if any
#   ./hack/check-tool-pins.sh --bump   rewrite the Makefile to the latest
set -eu

bump=false
case "${1:-}" in
  --bump) bump=true ;;
  "") ;;
  *) echo "usage: $0 [--bump]" >&2; exit 2 ;;
esac

# Makefile variable, and the module whose tags define its versions. Both are
# resolved through the Go module proxy rather than a forge API: no token, and
# the same source the build itself would use.
#
# Not listed, and why: ENVTEST_K8S_VERSION and SETUP_ENVTEST_VERSION track
# what envtest publishes assets for rather than a module's tags, and the Node
# pins in ci.yml name an LTS line on purpose. Those stay hand-maintained;
# this file is the record that the choice was made, not an oversight.
tools='GOVULNCHECK_VERSION golang.org/x/vuln
GOLANGCI_LINT_VERSION github.com/golangci/golangci-lint/v2'

status=0

# Fed by redirect rather than a pipe: a pipe would run the loop in a
# subshell, where `status` dies with it and the first drift would end the
# report instead of continuing to the next pin.
while IFS=' ' read -r var module; do
  [ -n "$var" ] || continue

  current=$(sed -n "s/^$var ?= //p" Makefile)
  if [ -z "$current" ]; then
    echo "no $var in Makefile" >&2
    exit 2
  fi

  # Stable tags only: a release candidate is not something to bump into.
  latest=$(go list -m -versions "$module" 2>/dev/null \
    | tr ' ' '\n' \
    | grep -e '^v[0-9][0-9.]*$' \
    | sort -V \
    | tail -1)
  if [ -z "$latest" ]; then
    echo "could not resolve versions for $module" >&2
    exit 2
  fi

  if [ "$current" = "$latest" ]; then
    echo "$var $current (current)"
    continue
  fi

  echo "$var $current -> $latest"
  status=1
  if [ "$bump" = true ]; then
    sed -i "s|^$var ?= $current\$|$var ?= $latest|" Makefile
  fi
done <<EOF
$tools
EOF

exit "$status"
