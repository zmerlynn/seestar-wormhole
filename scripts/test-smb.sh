#!/usr/bin/env bash
# Opt-in container test of everything that needs a real Samba server: the
# copy smbintegration tests, and TestComposedSMB through the proxy with the
# official rclone release. The fixture is internal/copy/remote/testdata/samba.
# Building its image and fetching rclone use the network; the tests run on a
# private podman network with no route out.
set -euo pipefail
task_src=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
task_cache=${SEESTAR_WORMHOLE_BUILD_CACHE:-"${XDG_CACHE_HOME:-$HOME/.cache}/seestar-wormhole/go-build"}
task_mod=${SEESTAR_WORMHOLE_MODULE_CACHE:-"${XDG_CACHE_HOME:-$HOME/.cache}/seestar-wormhole/go-mod"}
task_port=${SEESTAR_COPY_SMB_PORT:-14450}
task_image=localhost/seestar-wormhole-samba:test
task_name=seestar-wormhole-samba-$$
awk '/MemAvailable:/ { if ($2 < 6291456) { print "Need at least 6 GiB available" > "/dev/stderr"; exit 1 } }' /proc/meminfo
mkdir -p "$task_cache" "$task_mod"
task_tools=$(mktemp -d)
trap 'podman rm -f "$task_name" >/dev/null 2>&1 || true; podman network rm -f "$task_name" >/dev/null 2>&1 || true; podman unshare rm -rf "$task_tools" 2>/dev/null || rm -rf "$task_tools"' EXIT
podman build -t "$task_image" "$task_src/internal/copy/remote/testdata/samba"
# rclone v1.75.2, checked against its release's SHA256SUMS.
podman run --rm --pull=never -v "$task_tools:/tools:Z" -w /tools docker.io/library/golang:1.27 bash -euo pipefail -c '
 export DEBIAN_FRONTEND=noninteractive
 apt-get update -qq && apt-get install -y --no-install-recommends unzip >/dev/null
 arch=$(dpkg --print-architecture)
 case "$arch" in arm64|amd64) ;; *) echo "Unsupported fixture architecture: $arch" >&2; exit 1 ;; esac
 release=rclone-v1.75.2-linux-$arch
 curl --fail --location --retry 3 "https://downloads.rclone.org/v1.75.2/$release.zip" -o "$release.zip"
 curl --fail --location --retry 3 https://downloads.rclone.org/v1.75.2/SHA256SUMS -o SHA256SUMS
 awk -v file="$release.zip" '\''$2 == file {print; found=1} END {if (!found) exit 1}'\'' SHA256SUMS | sha256sum -c -
 unzip -q "$release.zip" && mv "$release/rclone" rclone && ./rclone version'
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
podman run --rm --pull=never --network "$task_name" --memory=4g --cpus=2 \
 -e GOMAXPROCS=2 -e 'GOFLAGS=-mod=vendor -p=2' -e GOTOOLCHAIN=local -e GOPROXY=off -e GOSUMDB=off \
 -e SEESTAR_COPY_SMB_ENDPOINT="$task_ip:$task_port" -e SEESTAR_TEST_SAMBA_ADDR="$task_ip:$task_port" -e SEESTAR_TEST_RCLONE=/tools/rclone \
 -v "$task_src:/src:ro,Z" "${task_git[@]}" -v "$task_tools:/tools:ro,Z" -v "$task_cache:/root/.cache/go-build:Z" -v "$task_mod:/go/pkg/mod:Z" \
 -w /src docker.io/library/golang:1.27 bash -euo pipefail -c '
 go test -tags smbintegration -race -count=1 -timeout 10m -v ./internal/copy/remote ./internal/copy/app
 go test -race -count=1 -timeout 2m -v ./internal/proxy -run "^TestComposedSMB$"' || { podman logs "$task_name" | tail -50; exit 1; }
