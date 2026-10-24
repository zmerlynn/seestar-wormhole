#!/usr/bin/env bash
# Exercise the container command contract without executing Go on the host.
set -euo pipefail
task_src=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
task_tmp=$(mktemp -d "${TMPDIR:-/tmp}/seestar-check-test.XXXXXX")
trap 'rm -rf "$task_tmp"' EXIT
mkdir -p "$task_tmp/bin"
cat > "$task_tmp/bin/podman" <<'MOCK'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$CHECK_ARGS"
MOCK
chmod +x "$task_tmp/bin/podman"
export PATH="$task_tmp/bin:$PATH" CHECK_ARGS="$task_tmp/args"
export SEESTAR_WORMHOLE_BUILD_CACHE="$task_tmp/build" SEESTAR_WORMHOLE_MODULE_CACHE="$task_tmp/mod"
require_arg() {
 if ! grep -Fxq -- "$1" "$CHECK_ARGS"; then
  printf 'missing container argument: %s\n' "$1" >&2; exit 1
 fi
}
"$task_src/scripts/check.sh"
require_arg --network=none
require_arg "$SEESTAR_WORMHOLE_MODULE_CACHE:/go/pkg/mod:Z"
require_arg GOPROXY=off
require_arg GOTOOLCHAIN=local
require_arg "GOFLAGS=-mod=vendor -p=2"
if ! grep -Fq "scripts/check-darwin.sh" "$CHECK_ARGS"; then
 echo "missing Darwin cross-build check" >&2; exit 1
fi
if ! grep -Fq "go run ./scripts/checkjson" "$CHECK_ARGS"; then
 echo "missing JSON import check" >&2; exit 1
fi
"$task_src/scripts/check.sh" fuzz
require_arg --network=none
for target in iotc:FuzzLicenseJSON iotc:FuzzPacket iotc:FuzzStream iotc:FuzzProtocolParsers iotc:FuzzLicense \
 cloud:FuzzJSON cloud:FuzzAccountResponses cloud:FuzzDevices edge/iotcedge:FuzzIOTCLicenseVerification \
 proxy:FuzzSiteProtection landiscovery:FuzzDecodeRequest edge:FuzzDTLSAdmission dtls:FuzzControlBytes \
 rdt:FuzzDecode p2ptunnel:FuzzDecode p2ptunnel:FuzzStream; do
 if ! grep -Fq "go test -p 2 ./internal/${target%:*} -run=\"^$\" -fuzz=\"^${target#*:}$\" -fuzztime=60s -parallel=2" "$CHECK_ARGS"; then
  printf 'missing fuzz target: %s\n' "$target" >&2; exit 1
 fi
done
"$task_src/scripts/check.sh" prepare
if grep -Fxq -- --network=none "$CHECK_ARGS"; then
 echo 'dependency preparation has no network' >&2; exit 1
fi
require_arg "$task_src:/src:ro,Z"
require_arg "$SEESTAR_WORMHOLE_MODULE_CACHE:/go/pkg/mod:Z"
if ! grep -Fq 'go mod download' "$CHECK_ARGS" || ! grep -Fq 'go mod verify' "$CHECK_ARGS"; then
 echo 'dependency preparation must download and verify pinned modules' >&2; exit 1
fi
printf 'check.sh command contract passed\n'
