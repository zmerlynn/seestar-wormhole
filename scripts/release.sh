#!/usr/bin/env bash
# Run with the same Go toolchain for byte-identical CLI downloads.
set -euo pipefail
task_validate_only=false
task_mode=()
if [[ ${1:-} == --validate-only ]]; then
 task_validate_only=true
 shift
elif [[ ${1:-} == --checksums ]]; then
 task_mode=(checksums)
 shift
fi
if [[ $# != 1 || ! $1 =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.(0|[1-9][0-9]*))?$ ]]; then
 printf 'release version must be vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N, without leading zeros\n' >&2
 exit 2
fi
if "$task_validate_only"; then
 exit 0
fi
task_src=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$task_src"
task_sha=$(git rev-parse --short=12 HEAD)
# Ignore machine-specific defaults and never download a toolchain or modules.
export CGO_ENABLED=0 GOENV=off GOWORK=off GOTOOLCHAIN=local
export GOFLAGS=-mod=vendor GOPROXY=off GOSUMDB=off GOEXPERIMENT=
export GOAMD64=v1 GOARM64=v8.0
exec go run -mod=vendor -trimpath ./scripts/release.go "$1" "$task_sha" "${task_mode[@]}"
