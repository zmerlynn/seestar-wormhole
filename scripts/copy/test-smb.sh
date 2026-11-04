#!/usr/bin/env bash
# Runs the smbintegration tests against the Samba fixture in containers on a
# private podman network. Building the fixture image downloads Samba; the
# tests themselves use no network but that private one.
set -euo pipefail
task_src=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
task_cache=${SEESTAR_COPY_BUILD_CACHE:-"${XDG_CACHE_HOME:-$HOME/.cache}/seestar-copy/go-build"}
task_mod=${SEESTAR_COPY_MODULE_CACHE:-"${XDG_CACHE_HOME:-$HOME/.cache}/seestar-copy/go-mod"}
task_port=${SEESTAR_COPY_SMB_PORT:-14450}
task_image=localhost/seestar-copy-samba:test
task_name=seestar-copy-samba-$$
mkdir -p "$task_cache" "$task_mod"
podman build -t "$task_image" "$task_src/internal/remote/testdata/samba"
trap 'podman rm -f "$task_name" >/dev/null 2>&1 || true; podman network rm -f "$task_name" >/dev/null 2>&1 || true' EXIT
# An internal network has no route out; only the two containers share it.
podman network create --internal "$task_name" >/dev/null
podman run -d --name "$task_name" --network "$task_name" -e SMB_PORT="$task_port" "$task_image" >/dev/null
for task_try in {1..50}; do
 if podman exec "$task_name" bash -c "exec 3<>/dev/tcp/127.0.0.1/$task_port" 2>/dev/null; then break; fi
 if [[ $task_try == 50 ]]; then podman logs "$task_name"; exit 1; fi
 sleep 0.2
done
task_ip=$(podman inspect -f "{{(index .NetworkSettings.Networks \"$task_name\").IPAddress}}" "$task_name")
task_git=()
task_common=$(git -C "$task_src" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)
case "$task_common" in
 ""|"$task_src"/*) ;;
 *) task_git=(-v "$task_common:$task_common:ro") ;;
esac
podman run --rm --pull=never --network "$task_name" --memory=2g --cpus=2 \
 -e GOMAXPROCS=2 -e 'GOFLAGS=-mod=vendor -p=2' -e GOTOOLCHAIN=local -e GOPROXY=off -e GOSUMDB=off \
 -e SEESTAR_COPY_SMB_ENDPOINT="$task_ip:$task_port" \
 -v "$task_src:/src:ro,Z" "${task_git[@]}" -v "$task_cache:/root/.cache/go-build:Z" -v "$task_mod:/go/pkg/mod:Z" \
 -w /src docker.io/library/golang:1.27 \
 go test -tags smbintegration -race -count=1 -timeout 10m -v ./internal/remote ./internal/app || { podman logs "$task_name" | tail -50; exit 1; }
