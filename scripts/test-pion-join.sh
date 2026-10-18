#!/bin/sh
# Run inside the Go 1.27 container. Keep upstream vendor files free of test additions.
set -eu
root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
probe=$(mktemp -d /tmp/pion-join.XXXXXX)
trap 'rm -rf "$probe"' EXIT
cp -a "$root/vendor" "$probe/"
cp "$root/go.mod" "$root/go.sum" "$probe/"
cp "$root/internal/edge/testdata/pion_context_join_test.go" "$probe/vendor/github.com/pion/dtls/v3/"
cd "$probe"
go test -race -count=10 -timeout=2m github.com/pion/dtls/v3 -run TestAdversarialWriteContextWatcherJoin
