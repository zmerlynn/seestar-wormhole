#!/usr/bin/env bash
# Opt-in network/container test. All downloaded tools disappear with --rm.
set -euo pipefail
task_src=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
task_cache=${SEESTAR_WORMHOLE_BUILD_CACHE:-"${XDG_CACHE_HOME:-$HOME/.cache}/seestar-wormhole/go-build"}
awk '/MemAvailable:/ { if ($2 < 6291456) { print "Need at least 6 GiB available" > "/dev/stderr"; exit 1 } }' /proc/meminfo
mkdir -p "$task_cache"
exec podman run --rm --pull=never --memory=4g --cpus=2 \
 -e GOMAXPROCS=2 -e GOFLAGS=-p=2 -e GOCACHE=/cache -e GOMODCACHE=/tmp/go-mod \
 -v "$task_src:/src:ro,Z" -v "$task_cache:/cache:Z" \
 -w /src docker.io/library/golang:1.27 bash scripts/test-smb-container.sh
