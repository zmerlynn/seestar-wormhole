# Cutting a release

[`.claude/skills/wormhole-release`](../../.claude/skills/wormhole-release/SKILL.md)
is the checklist for a whole release, ending in a single owner check of the
draft. This page has the build mechanics.

Use a clean checkout of the intended commit. CLI and Windows UI builds use the
official `golang:1.27.3` image (Go 1.27.3); dependencies are vendored. Linux UI
builds use Ubuntu 24.04 native runners with `libgtk-4-dev` and
`libwebkitgtk-6.0-dev`. Mac UI builds use macos-26, Xcode Command Line Tools,
and Go 1.27.3. GUI releases use `gui,production` tags.

1. Run `go build ./...`, `go vet ./...`, `go test ./...` with
   `GOFLAGS=-mod=vendor`, then `go test -race scripts/release.go scripts/release_test.go`
   and `./scripts/test-check.sh`. The named-file release tests check archive
   names, contents, modes, determinism, checksums, inventory completeness, and
   version validation before builds.
2. Run `./scripts/release.sh v0.5.0`. Versions must be `vMAJOR.MINOR.PATCH`
   or `vMAJOR.MINOR.PATCH-rc.N`, with no leading zeros in any number.
   Other forms exit 2 before building. This produces six static CLI executables
   and two CGO-free Windows UI executables (`-H windowsgui`) in `dist/parts/`,
   plus the two Windows ZIP archives in `dist/`. These are intermediate build
   outputs; no partial checksum file is emitted.
3. Run the script twice with the same source revision and compare portable
   outputs byte for byte. Linux and Windows CLI reproducibility is verified by
   rebuilding and comparing against the executable unpacked from the archive.
   Build mechanics remain `CGO_ENABLED=0`, `-trimpath`, disabled VCS stamping,
   fixed CPU baselines, and no build timestamp. Reproduction needs identical
   source, version, revision and Go toolchain. Stripping with `-s -w` retains
   Go module metadata for `licenses`.
4. Unpack the archive for your host and run `seestar-wormhole-cli --version`
   and `seestar-wormhole-cli licenses` (with `.exe` on Windows). Confirm version,
   commit and platform, and that licenses include `Module: github.com/pion/dtls/v3`.
5. Rehearse the full workflow from a branch with **Run workflow** and a valid
   `version` input. `workflow_dispatch` builds all downloads, writes full
   `SHA256SUMS`, attests them, and uploads a `release-assets-<version>` workflow
   artifact. It never creates a GitHub Release. Executable permissions are
   preserved inside the archives.
6. When ready, create an annotated version tag at the intended commit and push
   that tag. The workflow verifies the code, builds on the platforms above,
   assembles five downloads, checks their exact inventory, writes `SHA256SUMS`,
   and records build provenance attestations for every download. It creates a
   draft GitHub Release with six assets including the checksum file. RC tags
   create pre-release drafts; stable tags create ordinary drafts. Add notes with
   the compatibility record and breaking-change recovery steps before publishing.

Each download includes both the UI and CLI:

| Platform | Asset names | Root contents |
|---|---|---|
| Linux (amd64, arm64) | `seestar-wormhole_<version>_linux_<arch>.tar.gz` | `seestar-wormhole`, `seestar-wormhole-cli`, LICENSE, NOTICE |
| Windows (amd64, arm64) | `seestar-wormhole_<version>_windows_<arch>.zip` | `seestar-wormhole.exe`, `seestar-wormhole-cli.exe`, LICENSE, NOTICE |
| macOS (universal) | `seestar-wormhole_<version>_darwin_universal.dmg` | `seestar-wormhole.app`, `Applications` symlink to `/Applications`, universal `seestar-wormhole-cli`, LICENSE, NOTICE |

Archives have no enclosing directory. Go's standard tar/gzip/zip libraries write
sorted members with the commit time, executable mode 0755 and notice mode 0644;
tar ownership is zero with empty user/group names, gzip has no name or timestamp,
and compression levels are fixed. The Linux UI requires GTK 4 and WebKitGTK 6.0;
the CLI in the same archive does not. Every executable includes `licenses`.
`dist/` is ignored by Git.

The final job gathers the eight portable executables, two native Linux UI
executables and finished DMG into `dist/parts/`. Run
`./scripts/release.sh --checksums v0.5.0` to validate those parts, assemble the four
archives, remove the parts, enforce exactly five downloads plus `SHA256SUMS` in
`dist/`, and write the single checksum file. Then run
`sha256sum --check SHA256SUMS` from `dist/`. Checksums and attestations cover only
the five downloads, after signing and stapling.

The Mac CLI is combined with lipo and signed, so it is not byte-reproducible.
UI executables and the DMG also have no byte-for-byte reproducibility promise:
native SDKs, packaging, signatures and notarization can change their bytes.

## Mac signing and notarization

Before building or packaging locally, activate a Python venv and run
`python3 -m pip install --require-hashes --only-binary :all: -r scripts/dmg-requirements.txt`.

Run `./scripts/build-gui-mac.sh /absolute/output v0.5.0 <12-character-revision>`
on a Mac to build both slices, check deployment target 13.0 and bundle metadata,
combine them with `lipo`, build and combine both CLI slices, and package the
universal DMG. CI depends on the portable job and downloads its Darwin CLI
parts into the output directory's `cli/` subdirectory, so those builds are reused.
The script keeps its Go/SDK/compiler checks and prints tool versions. To package
an already built universal app with a universal `seestar-wormhole-cli` beside it,
run `./scripts/package-gui-mac.sh /absolute/seestar-wormhole.app /absolute/output.dmg`
as a single command.

Provide all seven environment variables below locally, or configure GitHub
Actions secrets with these exact names. Do not put their values in source or
shell history:

| Secret | Value |
|---|---|
| `MACOS_CERT_P12_BASE64` | Base64 Developer ID Application certificate and private key, exported as PKCS#12 |
| `MACOS_CERT_PASSWORD` | Password for that PKCS#12 export |
| `MACOS_SIGN_IDENTITY` | Developer ID Application signing identity (certificate SHA-1 fingerprint recommended) |
| `APPLE_TEAM_ID` | Expected certificate team identifier; checked after app signing |
| `APPLE_NOTARY_KEY_ID` | App Store Connect notarization API key ID |
| `APPLE_NOTARY_ISSUER_ID` | API key issuer UUID |
| `APPLE_NOTARY_KEY_P8_BASE64` | Base64 contents of the API key's `.p8` private key |

With all credentials present, the packaging script imports into a temporary
keychain, signs the app and universal CLI with the same identity, hardened
runtime, secure timestamps and the minimal
`macos-entitlements.plist`, builds and signs the DMG, submits with
`notarytool --wait`, and staples and validates the DMG ticket. No extra app
entitlements are enabled; WebKit uses system helper processes. The script
never enables command tracing and deletes its temporary files and keychain on
exit. CI also deletes the keychain in an `always()` step. `MACOS_KEYCHAIN_PATH`
is an optional non-secret temporary path; CI sets it under the runner's temp
directory. The script refuses to replace an existing keychain.

With all seven credentials absent, the app, CLI and DMG are ad-hoc signed with
`codesign -s -`, and the log explicitly says they are not notarized. Partial
credentials fail instead of silently producing an ad-hoc release. Until
Developer ID releases are available, document Gatekeeper's right-click Open
step. Test signing and notarization through artifact-only dispatches before
using credentials on a release tag.

Windows UI executables attach to the parent console when given arguments and
preserve valid stdout/stderr redirections. Windows shells do not wait for GUI
executables and their exit codes do not reach scripts: scripts and services
must use `seestar-wormhole-cli.exe`.
