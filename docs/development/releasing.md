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
   and `./scripts/test-check.sh`. The named-file release tests check bare asset
   names, checksums, inventory completeness, and version validation before builds.
2. Run `./scripts/release.sh v0.5.0`. Versions must be `vMAJOR.MINOR.PATCH`
   or `vMAJOR.MINOR.PATCH-rc.N`, with no leading zeros in any number.
   Other forms exit 2 before building. This produces six static CLI executables,
   two CGO-free Windows UI executables (`-H windowsgui`), and their partial
   `SHA256SUMS` in `dist/`.
3. Check `sha256sum --check SHA256SUMS` from `dist/`. Save the CLI downloads,
   run the script again with the same version and source revision, and compare
   every CLI executable with `cmp`. The byte-for-byte promise applies to CLI
   downloads only. Build mechanics remain `CGO_ENABLED=0`, `-trimpath`, disabled
   VCS stamping, fixed CPU baselines, and no build timestamp. Reproduction needs
   identical source, version, revision and Go toolchain. Stripping with `-s -w`
   retains Go module metadata for `licenses`.
4. Rename the CLI download for your host to `seestar-wormhole-cli` (plus `.exe`
   on Windows), make it executable on Unix, and run `seestar-wormhole-cli --version`
   and `seestar-wormhole-cli licenses`. Confirm version, commit and platform,
   and that licenses include `Module: github.com/pion/dtls/v3`.
5. Rehearse the full workflow from a branch with **Run workflow** and a valid
   `version` input. `workflow_dispatch` builds all downloads, writes full
   `SHA256SUMS`, attests them, and uploads a `release-assets-<version>` workflow
   artifact. It never creates a GitHub Release. Artifact download may reset Unix
   executable permissions; users of rehearsal artifacts must restore those.
6. When ready, create an annotated version tag at the intended commit and push
   that tag. The workflow verifies the code, builds on the platforms above,
   gathers eleven downloads, checks their exact inventory, writes `SHA256SUMS`,
   and records build provenance attestations for every download. It creates a
   draft GitHub Release with twelve assets including the checksum file. RC tags
   create pre-release drafts; stable tags create ordinary drafts. Add notes with
   the compatibility record and breaking-change recovery steps before publishing.

Downloads are bare files, not archives:

| Program | Asset names |
|---|---|
| CLI, all six OS/architecture pairs | `seestar-wormhole-cli_<version>_<os>_<arch>` (plus `.exe` on Windows) |
| Linux UI | `seestar-wormhole_<version>_linux_<arch>` |
| Windows UI | `seestar-wormhole_<version>_windows_<arch>.exe` |
| macOS UI | `seestar-wormhole_<version>_darwin_universal.dmg` |

The DMG contains `seestar-wormhole.app` (arm64 and amd64 slices), a symlink to
`/Applications`, LICENSE and NOTICE. CLI downloads cover linux, darwin, windows
x amd64, arm64. Documentation and notices are available in the source repository;
every executable also includes `licenses`. `dist/` is ignored by Git.

UI downloads are verified by SHA256SUMS and attestation only. They have no
byte-for-byte reproducibility promise: native SDKs, packaging, signatures and
notarization can change their bytes. After gathering native UI downloads into
`dist/`, run `./scripts/release.sh --checksums v0.5.0` to require the exact eleven
filenames and write the full checksum file. Do this after all signing and
stapling, since those change the downloads.

## Mac signing and notarization

Run `./scripts/build-gui-mac.sh /absolute/output v0.5.0 <12-character-revision>`
on a Mac to build both slices, check deployment target 13.0 and bundle metadata,
combine them with `lipo`, and package the universal DMG. The script keeps its
Go/SDK/compiler checks and prints tool versions. To package an already built
universal app, run `./scripts/package-gui-mac.sh /absolute/seestar-wormhole.app
/absolute/output.dmg` as a single command.

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
keychain, signs the app with hardened runtime and the minimal
`macos-entitlements.plist`, builds and signs the DMG, submits with
`notarytool --wait`, and staples and validates the DMG ticket. No extra app
entitlements are enabled; WebKit uses system helper processes. The script
never enables command tracing and deletes its temporary files and keychain on
exit. CI also deletes the keychain in an `always()` step. `MACOS_KEYCHAIN_PATH`
is an optional non-secret temporary path; CI sets it under the runner's temp
directory. The script refuses to replace an existing keychain.

With all seven credentials absent, both app and DMG are ad-hoc signed with
`codesign -s -`, and the log explicitly says they are not notarized. Partial
credentials fail instead of silently producing an ad-hoc release. Until
Developer ID releases are available, document Gatekeeper's right-click Open
step. Test signing and notarization through artifact-only dispatches before
using credentials on a release tag.

Windows UI executables attach to the parent console when given arguments and
preserve valid stdout/stderr redirections. Windows shells do not wait for GUI
executables and their exit codes do not reach scripts: scripts and services
must use `seestar-wormhole-cli.exe`.
