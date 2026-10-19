# Cutting a release

Use a clean checkout of the intended commit and the official `golang:1.27.2`
image (Go 1.27.2). Dependencies are vendored; builds need no network or C toolchain.

1. Run `go build ./...`, `go vet ./...`, `go test ./...` with
   `GOFLAGS=-mod=vendor`, then `go test scripts/release.go scripts/release_test.go`
   and `./scripts/test-check.sh`. The named-file release test builds both archive
   formats and checks their exact file inventories against the checkout.
2. Run `./scripts/release.sh v1.2.3`. Versions must be `vMAJOR.MINOR.PATCH`
   or `vMAJOR.MINOR.PATCH-rc.N`, with no leading zeros in any number.
   Other forms exit 2 before building.
3. Check `dist/SHA256SUMS` with `sha256sum --check SHA256SUMS` from `dist/`.
   Save the archives, run the script again with the same version, and compare
   every archive and the checksum file with `cmp`.
4. Extract the archive for your host and run `seestar-wormhole --version` and
   `seestar-wormhole licenses`. Confirm the version, commit and platform, and
   that the license output includes `Module: github.com/pion/dtls/v3`.
5. When ready to publish, create an annotated `v1.2.3` tag at that commit and
   push that tag. The release workflow verifies the code, runs the same script,
   uploads the six archives and `SHA256SUMS` to a draft GitHub Release. RC tags
   create a pre-release draft; stable tags create an ordinary draft. Add the
   compatibility record and breaking changes, review the notes, and publish
   by hand when ready.

The script builds Linux, macOS and Windows for amd64 and arm64. Each archive
contains `seestar-wormhole` (`seestar-wormhole.exe` on Windows), `README.md`,
`LICENSE`, `NOTICE`, and the user documentation under `docs/`, preserving
relative paths. All regular files under `docs/` are included recursively except
`docs/development/`. Links to development documentation use source-repository
URLs, so following them requires internet access.
`dist/` is ignored by Git. The CLI prints the supplied version and the first 12
characters of the source commit in separate fields.

Reproduction requires identical source contents (including docs, README and notices),
version, commit and Go toolchain. `-trimpath`, disabled VCS stamping, fixed CPU
baselines and `CGO_ENABLED=0` remove host path and C toolchain variation. Archive
entries are sorted by path; modes, ownership and timestamps are fixed. Gzip carries no original
filename or timestamp. No build timestamp is embedded. Stripping with `-s -w`
retains Go module metadata for the `licenses` command.
