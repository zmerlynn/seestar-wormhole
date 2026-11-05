#!/bin/sh
# seestar-copy's checks that scripts/check.sh does not otherwise run: the
# cross-builds of its CLI, the license catalog check and vet of the Samba
# tests. Run from the repository root with Go 1.27 and GOFLAGS=-mod=vendor; needs no network and no C toolchain.
set -eu
task_pkgs='./cmd/seestar-copy-cli ./internal/copy/...'
for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
 # vet type-checks each target's tests too; foreign test binaries cannot run here.
 CGO_ENABLED=0 GOOS=${target%/*} GOARCH=${target#*/} go build -p 2 $task_pkgs
 CGO_ENABLED=0 GOOS=${target%/*} GOARCH=${target#*/} go vet -p 2 $task_pkgs
done
go run ./scripts/generate-licenses.go --check
# The Samba tests run separately (scripts/copy/test-smb.sh); here they only build.
go vet -p 2 -tags smbintegration ./internal/copy/remote ./internal/copy/app
