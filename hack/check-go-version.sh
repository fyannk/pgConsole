#!/bin/sh
# Go toolchain agreement. The version lives in go.mod; the builder image
# in the Dockerfile has to carry it too, because a container image tag
# cannot be read from a module file. Dependabot moves the Dockerfile on
# its own schedule and nothing moves go.mod with it, so the two drift
# silently — and a release whose binaries and container are built by
# different toolchains is one the supply-chain attestations cannot
# honestly describe as the same build.
set -eu

modfile=go.mod
dockerfile=Dockerfile

# "toolchain go1.27.0" -> "1.27.0"
mod_version=$(sed -n 's/^toolchain go\([0-9][0-9.]*\)$/\1/p' "$modfile")
if [ -z "$mod_version" ]; then
  echo "no toolchain directive in $modfile" >&2
  exit 1
fi

# "FROM ... golang:1.27.0-alpine@sha256:..." -> "1.27.0"
img_version=$(sed -n 's/.*golang:\([0-9][0-9.]*\)-alpine.*/\1/p' "$dockerfile")
if [ -z "$img_version" ]; then
  echo "no golang builder image in $dockerfile" >&2
  exit 1
fi

if [ "$mod_version" != "$img_version" ]; then
  echo "Go toolchain drift: $modfile says $mod_version, $dockerfile says $img_version" >&2
  echo "bump whichever is behind so the binaries and the container share a toolchain" >&2
  exit 1
fi

echo "Go toolchain agrees at $mod_version"
