#!/bin/sh
set -eu

image=${1:-pgconsole:dev}
artifact_dir=${2:-artifacts/release}
syft_image='anchore/syft@sha256:f94e5d9fce1f2278491a8e3a63bd5f6ddb81fdfdbb8bf7a1637565c1d5344357'
trivy_image='aquasec/trivy@sha256:a22415a38938a56c379387a8163fcb0ce38b10ace73e593475d3658d578b2436'
go_licenses_version='v1.6.0'
vulnerability_result=$(mktemp)
license_warnings=$(mktemp)

cleanup() {
    rm -f "$vulnerability_result"
    rm -f "$license_warnings"
}
trap cleanup EXIT INT TERM

mkdir -p "$artifact_dir"
docker_socket_group=$(stat -c '%g' /var/run/docker.sock)

docker run --rm --volume /var/run/docker.sock:/var/run/docker.sock \
    "$syft_image" "$image" --output spdx-json > "$artifact_dir/sbom.spdx.json"
if ! go run "github.com/google/go-licenses@$go_licenses_version" report ./... \
    > "$artifact_dir/licenses.csv" 2> "$license_warnings"; then
    cat "$license_warnings" >&2
    echo 'license scanner failed' >&2
    exit 1
fi
if grep -En ',(Unknown|Forbidden)$' "$artifact_dir/licenses.csv"; then
    echo 'license report contains an unresolved or forbidden license' >&2
    exit 1
fi

image_id=$(docker image inspect --format '{{.Id}}' "$image")
architecture=$(docker image inspect --format '{{.Architecture}}' "$image")
printf '{"image":"%s","digest":"%s","architecture":"%s"}\n' \
    "$image" "$image_id" "$architecture" > "$artifact_dir/image-digest.json"

docker run --rm \
    --user "$(id -u):$(id -g)" \
    --group-add "$docker_socket_group" \
    --env HOME=/tmp \
    --volume /var/run/docker.sock:/var/run/docker.sock \
    "$trivy_image" image --skip-version-check --scanners vuln --severity HIGH,CRITICAL \
        --ignore-unfixed --exit-code 1 --format json \
        "$image" > "$vulnerability_result"
mv -f "$vulnerability_result" "$artifact_dir/vulnerability.json"

test -s "$artifact_dir/sbom.spdx.json"
test -s "$artifact_dir/licenses.csv"
test -s "$artifact_dir/image-digest.json"
test -s "$artifact_dir/vulnerability.json"
printf 'supply-chain checks passed for %s: SPDX SBOM, image digest, license report, and vulnerability result retained in %s\n' "$image_id" "$artifact_dir"
