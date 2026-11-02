#!/usr/bin/env bash
set -euo pipefail
# All Go execution stays in the prescribed image; no host Go is needed.
task_src=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
task_cache=${SEESTAR_COPY_BUILD_CACHE:-"${XDG_CACHE_HOME:-$HOME/.cache}/seestar-copy/go-build"}
task_mod=${SEESTAR_COPY_MODULE_CACHE:-"${XDG_CACHE_HOME:-$HOME/.cache}/seestar-copy/go-mod"}
# Only Linux has /proc/meminfo; elsewhere the container's --memory limit stands alone.
if [ -r /proc/meminfo ]; then
 awk '/MemAvailable:/ { if ($2 < 2097152) { print "Need at least 2 GiB available" > "/dev/stderr"; exit 1 } }' /proc/meminfo
fi
mkdir -p "$task_cache" "$task_mod"
mode=${1:-check}
task_network=(--network=none -e GOPROXY=off -e GOSUMDB=off)
task_source="$task_src:/src:Z"
# A worktree's .git points at the main repository; VCS stamping needs git there.
task_git=()
task_common=$(git -C "$task_src" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)
case "$task_common" in
 ""|"$task_src"/*) ;;
 *) task_git=(-v "$task_common:$task_common:ro") ;;
esac
case "$mode" in
 prepare)
  # Only dependency preparation uses the network. The lockfiles cannot change.
  task_network=()
  task_source="$task_src:/src:ro,Z"
  task_command='go mod download && go mod verify' ;;
 check) task_command='sh scripts/check-go.sh' ;;
 *) printf 'Usage: %s [prepare|check]\n' "$0" >&2; exit 2 ;;
esac
exec podman run --rm --pull=never "${task_network[@]}" --memory=2g --cpus=2 \
 -e GOMAXPROCS=2 -e 'GOFLAGS=-mod=vendor -p=2' -e GOTOOLCHAIN=local \
 -v "$task_source" "${task_git[@]}" -v "$task_cache:/root/.cache/go-build:Z" -v "$task_mod:/go/pkg/mod:Z" \
 -w /src docker.io/library/golang:1.27 sh -c "$task_command"
