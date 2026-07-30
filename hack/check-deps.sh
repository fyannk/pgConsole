#!/bin/sh
# Forbidden-dependency scan. No package that compiles into this module —
# and therefore into the binary — may come from a cloud provider SDK, a
# SQL driver, or repository-parsing code; those belong to the sibling
# applications. The scan inspects the actual package dependency closure,
# not the raw module graph, which carries pruned transitive entries of
# test-only tooling.
set -eu

forbidden='^(github\.com/aws/|github\.com/Azure/|cloud\.google\.com/|github\.com/googleapis/|github\.com/jackc/pgx|github\.com/lib/pq|github\.com/go-sql-driver/|github\.com/microsoft/go-mssqldb|github\.com/mattn/go-sqlite|github\.com/minio/)'

if go list -deps ./... | grep -E "$forbidden"; then
  echo "forbidden package in the build dependency closure" >&2
  exit 1
fi

if grep -En 'aws-sdk|azure-sdk|cloud\.google\.com|jackc/pgx|lib/pq|go-sql-driver' go.mod; then
  echo "forbidden module required directly in go.mod" >&2
  exit 1
fi

echo "build dependency closure clean"
