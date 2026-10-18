#!/usr/bin/env bash
set -euo pipefail
# All Go execution stays in the prescribed image; no host Go is needed.
task_src=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
task_cache=${SEESTAR_WORMHOLE_BUILD_CACHE:-"${XDG_CACHE_HOME:-$HOME/.cache}/seestar-wormhole/go-build"}
task_mod=${SEESTAR_WORMHOLE_MODULE_CACHE:-"${XDG_CACHE_HOME:-$HOME/.cache}/seestar-wormhole/go-mod"}
awk '/MemAvailable:/ { if ($2 < 6291456) { print "Need at least 6 GiB available" > "/dev/stderr"; exit 1 } }' /proc/meminfo
mkdir -p "$task_cache" "$task_mod"
mode=${1:-check}
task_network=(--network=none -e GOPROXY=off -e GOSUMDB=off)
task_source="$task_src:/src:Z"
# A worktree's .git points at the main repository; release tests need git there.
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
 check) task_command='go version && go run ./scripts/checkjson && test -z "$(find . \( -type d -name ".*" ! -name . -o -path ./vendor \) -prune -o -type f -name "*.go" -print0 | xargs -0 gofmt -l)" && go build -p 2 ./... && bash scripts/check-darwin.sh && go vet -p 2 ./... && go test -p 2 -race -count=2 -timeout 50m ./... && go test scripts/release.go scripts/release_test.go && sh scripts/test-pion-join.sh' ;;
 fuzz) task_command='go test -p 2 ./internal/iotc -run="^$" -fuzz="^FuzzLicenseJSON$" -fuzztime=60s -parallel=2 && go test -p 2 ./internal/proxy -run="^$" -fuzz="^FuzzSiteProtection$" -fuzztime=60s -parallel=2 && go test -p 2 ./internal/landiscovery -run="^$" -fuzz="^FuzzDecodeRequest$" -fuzztime=60s -parallel=2 && go test -p 2 ./internal/cloud -run="^$" -fuzz="^FuzzJSON$" -fuzztime=60s -parallel=2 && go test -p 2 ./internal/edge -run="^$" -fuzz="^FuzzDTLSAdmission$" -fuzztime=60s -parallel=2 && go test -p 2 ./internal/dtls -run="^$" -fuzz="^FuzzControlBytes$" -fuzztime=60s -parallel=2 && go test -p 2 ./internal/rdt -run="^$" -fuzz="^FuzzDecode$" -fuzztime=60s -parallel=2 && go test -p 2 ./internal/p2ptunnel -run="^$" -fuzz="^FuzzDecode$" -fuzztime=60s -parallel=2 && go test -p 2 ./internal/p2ptunnel -run="^$" -fuzz="^FuzzStream$" -fuzztime=60s -parallel=2' ;;
 *) printf 'Usage: %s [prepare|check|fuzz]\n' "$0" >&2; exit 2 ;;
esac
exec podman run --rm --pull=never "${task_network[@]}" --memory=4g --cpus=2 \
 -e GOMAXPROCS=2 -e 'GOFLAGS=-mod=vendor -p=2' -e GOTOOLCHAIN=local \
 -v "$task_source" "${task_git[@]}" -v "$task_cache:/root/.cache/go-build:Z" -v "$task_mod:/go/pkg/mod:Z" \
 -w /src docker.io/library/golang:1.27 sh -c "$task_command"
