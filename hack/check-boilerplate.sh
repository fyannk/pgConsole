#!/bin/sh
# Verifies every Go source file starts with the Apache-2.0 boilerplate.
set -eu

boilerplate="hack/boilerplate.go.txt"
lines=$(wc -l < "$boilerplate")
status=0

for f in $(find cmd internal -name '*.go' -type f); do
  if ! head -n "$lines" "$f" | diff -q - "$boilerplate" > /dev/null 2>&1; then
    echo "missing or wrong boilerplate: $f" >&2
    status=1
  fi
done

exit "$status"
