#!/usr/bin/env bash
set -euo pipefail
# Production Listen regression in disposable namespaces. Public packages are
# provisioned separately; the test has no network access beyond its own peers.
source_dir=$(cd "$(dirname "$0")/.." && pwd)
evidence_dir=${1:-/tmp/lan-address-changes}
build_cache=${SEESTAR_WORMHOLE_BUILD_CACHE:-/tmp/lan-discovery-go-build}
mkdir -p "$evidence_dir" "$build_cache"
evidence_dir=$(cd "$evidence_dir" && pwd)
available=$(awk '/MemAvailable:/ {print $2}' /proc/meminfo)
(( available >= 6 * 1024 * 1024 ))
# Package installation is the only network-enabled step. The empty build context
# keeps repository files out of the tools image.
tools_dir=$(mktemp -d)
cleanup() {
 if test -s "$tools_dir/image-id"; then
  podman image rm "$(cat "$tools_dir/image-id")" >/dev/null 2>&1 || true
 fi
 rm -rf "$tools_dir"
}
trap cleanup EXIT
podman build --cpu-period 100000 --cpu-quota 200000 --memory 1g --iidfile "$tools_dir/image-id" -f - "$tools_dir" <<'CONTAINERFILE'
FROM docker.io/library/alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8
RUN apk add --no-cache bash iproute2 util-linux
CONTAINERFILE
podman run --rm --pull=never --network none --cpus 2 --memory 4g \
 -e CGO_ENABLED=0 -e GOFLAGS='-mod=vendor -p=2' -e GOMAXPROCS=2 -e GOCACHE=/go-build \
 -v "$build_cache:/go-build" \
 -v "$source_dir:/src:ro" -v "$evidence_dir:/evidence" -w /src \
 docker.io/library/golang:1.27 \
 go test -p 2 -c -o /evidence/lan-address-changes.test ./internal/landiscovery
podman run --rm --pull=never --privileged --network none --cpus 2 --memory 1g \
 -v "$source_dir:/src:ro" -v "$evidence_dir:/evidence" \
 "$(cat "$tools_dir/image-id")" bash /src/scripts/testdata/lan-address-changes.sh
