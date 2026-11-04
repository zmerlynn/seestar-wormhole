#!/bin/sh
# The Go checks behind ./scripts/check.sh and CI. Run from the repository root
# with Go 1.27 and GOFLAGS=-mod=vendor; needs no network and no C toolchain
# except for the race detector.
set -eu
go version
unformatted=$(find . \( -type d -name '.*' ! -name . -o -path ./vendor \) -prune -o -type f -name '*.go' -print | xargs gofmt -l)
if [ -n "$unformatted" ]; then
 printf 'gofmt needed:\n%s\n' "$unformatted" >&2
 exit 1
fi
go build -p 2 ./...
for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
 # vet type-checks each target's tests too; foreign test binaries cannot run here.
 CGO_ENABLED=0 GOOS=${target%/*} GOARCH=${target#*/} go build -p 2 ./...
 CGO_ENABLED=0 GOOS=${target%/*} GOARCH=${target#*/} go vet -p 2 ./...
done
# The app's window needs cgo on macOS and Linux, so only Windows cross-builds
# it here; CI builds the others natively.
for target in windows/amd64 windows/arm64; do
 CGO_ENABLED=0 GOOS=${target%/*} GOARCH=${target#*/} go build -p 2 -tags gui,production ./...
 CGO_ENABLED=0 GOOS=${target%/*} GOARCH=${target#*/} go vet -p 2 -tags gui,production ./...
done
go run ./scripts/generate-licenses.go --check
# The Samba tests run separately (scripts/test-smb.sh); here they only build.
go vet -p 2 -tags smbintegration ./internal/remote
go test -p 2 -race -count=2 -timeout 20m ./...
# The release tooling is outside ./... behind the release build tag.
go vet scripts/release.go scripts/release_test.go
go test scripts/release.go scripts/release_test.go
