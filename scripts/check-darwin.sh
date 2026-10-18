#!/usr/bin/env bash
set -euo pipefail
# Run inside a Go 1.27 container; builds need no C toolchain.
for task_arch in arm64 amd64; do
 CGO_ENABLED=0 GOOS=darwin GOARCH="$task_arch" go build -p 2 ./...
 # Compile the platform-specific tests too; they cannot execute on Linux.
 CGO_ENABLED=0 GOOS=darwin GOARCH="$task_arch" go test -p 2 -c -o "/tmp/lan-proxy-$task_arch.test" ./cmd/seestar-wormhole
 CGO_ENABLED=0 GOOS=darwin GOARCH="$task_arch" go test -p 2 -c -o "/tmp/lan-discovery-$task_arch.test" ./internal/landiscovery
done
