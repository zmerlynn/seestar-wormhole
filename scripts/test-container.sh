#!/usr/bin/env bash
# Build final images before checking their effective user; Podman resolves stages/USER.
set -euo pipefail
task_src=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
task_cache=${SEESTAR_WORMHOLE_BUILD_CACHE:-"${XDG_CACHE_HOME:-$HOME/.cache}/seestar-wormhole/go-build"}
task_tmp=$(mktemp -d)
task_images=()
task_container=''
cleanup() {
 if [[ -n $task_container ]]; then podman rm -f "$task_container" >/dev/null; fi
 for task_image in "${task_images[@]}"; do podman rmi "$task_image" >/dev/null; done
 # :U maps state ownership into the container's user namespace.
 podman unshare rm -rf "$task_tmp"
}
trap cleanup EXIT
mkdir -p "$task_cache"
case $(uname -m) in
 aarch64) task_native=arm64 ;;
 x86_64) task_native=amd64 ;;
 *) echo 'Unsupported build host architecture' >&2; exit 1 ;;
esac
for task_arch in arm64 amd64; do
 awk '/MemAvailable:/ {if ($2 < 6291456) exit 1}' /proc/meminfo
 podman build --pull=never --layers=false --rm --force-rm --memory=4g \
  --cpu-period=100000 --cpu-quota=200000 --platform "linux/$task_arch" \
  --build-arg "BUILDPLATFORM=linux/$task_native" --build-arg "TARGETARCH=$task_arch" \
  --iidfile "$task_tmp/image.iid" -f "${CONTAINERFILE:-$task_src/Containerfile}" "$task_src"
 task_image=$(cat "$task_tmp/image.iid")
 task_images+=("$task_image")
 task_user=$(podman image inspect --format '{{.Config.User}}' "$task_image")
 printf 'linux/%s effective Config.User=%s\n' "$task_arch" "$task_user"
 podman run --rm --pull=never --network=none --memory=4g --cpus=2 \
  -e GOMAXPROCS=2 -e GOFLAGS=-p=2 -e GOTOOLCHAIN=go1.27.3 \
  -e GOPROXY=off -e GOSUMDB=off -e GOMODCACHE=/tmp/go-mod \
  -e "SEESTAR_CONTAINER_USER=$task_user" \
  -v "$task_src:/src:ro,Z" -v "$task_cache:/root/.cache/go-build:Z" \
  -w /src docker.io/library/golang:1.27 \
  go test -p 2 ./cmd/seestar-wormhole -run '^TestContainerRunsAsNonRoot$' -count=1 -v
 if [[ $task_arch == "$task_native" ]]; then
  podman run --rm --cap-drop=all --read-only --network=none "$task_image" --version
  # catalog lookup now runs even in lazy mode. Without a network fixture,
  # verify private identity creation before authorization and bounded signal exit.
  mkdir -p "$task_tmp/state"
  task_container=$(podman run -d --cap-drop=all --read-only --network=none \
   -v "$task_tmp/state:/state:U,Z" -e SEESTAR_EMAIL=fixture@example.invalid \
   -e SEESTAR_PASSWORD=fixture-password "$task_image" --serial fixture --idle 2m)
  for task_try in {1..100}; do
   podman logs "$task_container" > "$task_tmp/smoke.log" 2>&1
   if podman exec "$task_container" /seestar-wormhole-cli --version >/dev/null 2>&1 && podman unshare test -s "$task_tmp/state/InterProcessKV/device_uuid"; then break; fi
   sleep 0.1
  done
  podman unshare test -s "$task_tmp/state/InterProcessKV/device_uuid"
  podman stop -t 10 "$task_container" >/dev/null
  podman logs "$task_container" > "$task_tmp/smoke.log" 2>&1
  cat "$task_tmp/smoke.log"
  ! rg -q '"reason":"invalid_option"' "$task_tmp/smoke.log"
  podman rm "$task_container" >/dev/null
  task_container=''
 fi
done
