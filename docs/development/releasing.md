# Cutting a release

This page has the build mechanics of a release: what the workflow builds,
how to rehearse it, and how to check that it is reproducible. The
[versioning contract](../versioning.md) says what a release promises. The
[release checklist](../../.claude/skills/seestar-copy-release/SKILL.md) puts
these steps in order with the checks before publishing.

Use a clean checkout of the intended commit. The CLI and Windows app builds
use the official `golang:1.27.3` image (Go 1.27.3), with the vendored
dependencies. The Linux apps need cgo, so they build on Ubuntu 24.04 native
runners (`ubuntu-24.04` and `ubuntu-24.04-arm`) with `libgtk-4-dev` and
`libwebkitgtk-6.0-dev`. The Mac app and DMG are built on macos-26 with Xcode
Command Line Tools and Go 1.27.3. App builds use the `gui,production` tags.

1. Run `./scripts/check.sh`, or `GOFLAGS=-mod=vendor sh scripts/check-go.sh`
   with Go installed. Besides the usual checks it runs
   `go test scripts/release.go scripts/release_test.go`, the release tooling
   tests: archive names, members, modes, determinism, checksums, the exact
   inventory, and version validation before any build.
2. Choose the version by the [bump rules](../versioning.md#bumps), then
   run `./scripts/release.sh v0.1.0`. Versions must be `vMAJOR.MINOR.PATCH`
   or `vMAJOR.MINOR.PATCH-rc.N`, with no leading zeros in any number. Other
   forms exit 2 before building. This writes the six static CLI executables
   and the two Windows apps (`CGO_ENABLED=0`, `-H windowsgui`) to
   `dist/parts/`, and the two Windows archives to `dist/`. The Linux archives
   wait for the Linux apps, which only the workflow builds. These are
   intermediate outputs; no checksum file is written.
3. Run the script twice with the same source revision, from two checkouts if
   you like, and compare the CLI executables byte for byte. Linux and Windows
   CLI executables are reproducible: `CGO_ENABLED=0`, `-trimpath`,
   no VCS stamp, `GOAMD64=v1`, `GOARM64=v8.0`, `-s -w`, and no build
   timestamp. Reproduction needs the same source, version, revision and Go
   toolchain. Stripping keeps Go's module information, which `licenses`
   reads.
4. Run the CLI part for your host from `dist/parts/` with `--version` and
   with `licenses`, such as
   `dist/parts/seestar-copy-cli_v0.1.0_linux_amd64 --version`. Check the
   version, commit and platform, and that the licenses include
   `Module: github.com/hirochachacha/go-smb2/v2`.
5. Rehearse the whole workflow from a branch with **Run workflow** and a
   valid `version` input, such as
   `gh workflow run release.yml --ref <branch> -f version=v0.1.0-rc.0`. A
   manual run builds all five downloads, writes `SHA256SUMS`, attests them
   and uploads a `release-assets-<version>` workflow artifact. It never
   creates a GitHub Release. Download the artifact, unpack the Linux and
   Windows archives, and compare their CLIs byte for byte with the parts
   from a local run of the same version at the same commit.
6. When ready, create an annotated version tag at the intended commit and
   push it. The workflow runs CI, builds on the platforms above, assembles
   the five downloads, checks their exact inventory, writes `SHA256SUMS` and
   records a build provenance attestation for each download. It then creates
   a draft GitHub Release with the five downloads and the checksum file. RC
   tags create prerelease drafts; others create ordinary drafts. Add the
   notes before publishing.

Each download holds both programs, the desktop app `seestar-copy` and
`seestar-copy-cli`:

| Platform | Asset name | Root contents |
|---|---|---|
| Linux (amd64, arm64) | `seestar-copy_<version>_linux_<arch>.tar.gz` | `seestar-copy`, `seestar-copy-cli`, LICENSE, NOTICE |
| Windows (amd64, arm64) | `seestar-copy_<version>_windows_<arch>.zip` | `seestar-copy-cli.exe`, `seestar-copy.exe`, LICENSE, NOTICE |
| macOS (universal) | `seestar-copy_<version>_darwin_universal.dmg` | `seestar-copy.app`, `Applications` symlink to `/Applications`, universal `seestar-copy-cli`, LICENSE, NOTICE |

Archives have no enclosing directory. Go's standard tar, gzip and zip
packages write sorted members with the commit time, mode 0755 for the
executable and 0644 for the notices. Tar ownership is zero with empty user
and group names, gzip has no name or timestamp, and the compression level is
fixed. The Linux app needs GTK 4 and WebKitGTK 6.0; the CLI in the same
archive does not. `dist/` is ignored by Git.

The final job gathers the six CLI executables, the two Windows apps, the two
Linux apps and the finished DMG into `dist/parts/`.
`./scripts/release.sh --checksums v0.1.0` accepts exactly those parts,
assembles the four archives, removes the parts, requires exactly the
five downloads plus `SHA256SUMS` in `dist/`, and writes the checksum file.
The job then runs `sha256sum --check SHA256SUMS` in `dist/`. Checksums and
attestations cover the five downloads after signing and stapling.

The Mac CLI is combined with `lipo` and signed, so neither it nor the DMG is
byte-reproducible. The apps have no byte-for-byte reproducibility promise on
any platform: native SDKs and libraries, packaging, signatures and
notarization can change their bytes.

## Mac signing and notarization

Before packaging locally, activate a Python venv and run
`python3 -m pip install --require-hashes --only-binary :all: -r scripts/dmg-requirements.txt`.

On a Mac with Go 1.27.3, run `./scripts/release.sh v0.1.0`, then
`./scripts/build-gui-mac.sh "$PWD/dist/parts" v0.1.0 /absolute/output`. The
script builds `seestar-copy.app` for arm64 and amd64 with cgo, checks the
deployment target 13.0 and the bundle's Info.plist, makes its icns from
`cmd/seestar-copy/icon.png`, copies LICENSE and NOTICE into the bundle's
`Contents/Resources/`, and combines the two slices with `lipo`. It
combines the two Darwin CLIs from the parts the same way, then runs
`scripts/package-gui-mac.sh`, which signs the app and the CLI and writes
`/absolute/output/seestar-copy_v0.1.0_darwin_universal.dmg`. In CI, the Mac
job downloads the portable job's parts and runs the same script.

Provide all seven environment variables below locally, or configure GitHub
Actions secrets with these exact names. Don't put their values in source or
shell history:

| Secret | Value |
|---|---|
| `MACOS_CERT_P12_BASE64` | Base64 Developer ID Application certificate and private key, exported as PKCS#12 |
| `MACOS_CERT_PASSWORD` | Password for that PKCS#12 export |
| `MACOS_SIGN_IDENTITY` | Developer ID Application signing identity (certificate SHA-1 fingerprint recommended) |
| `APPLE_TEAM_ID` | Expected certificate team identifier; checked after signing the app and the CLI |
| `APPLE_NOTARY_KEY_ID` | App Store Connect notarization API key ID |
| `APPLE_NOTARY_ISSUER_ID` | API key issuer UUID |
| `APPLE_NOTARY_KEY_P8_BASE64` | Base64 contents of the API key's `.p8` private key |

With all credentials present, the script imports the certificate into a
temporary keychain and signs the app and the CLI with the same identity,
hardened runtime, a secure timestamp and the empty
`scripts/macos-entitlements.plist`: WebKit runs in system helper processes,
so the app needs no entitlements, and none are granted. It then builds and signs the DMG, submits it with `notarytool --wait`, and
staples and validates the ticket. The script never enables command tracing
and deletes its temporary files and keychain on exit; CI also deletes the
keychain in an `always()` step. `MACOS_KEYCHAIN_PATH` is an optional,
non-secret temporary path, which CI sets under the runner's temp directory.
The script refuses to replace an existing keychain.

With all seven credentials absent, the app, CLI and DMG are ad-hoc signed with
`codesign -s -`, and the log says the DMG is not notarized. Partial
credentials fail instead of silently producing an ad-hoc release. Test
signing and notarization with a manual run before using the credentials on
a release tag.

## Weekly checks

The Vulncheck workflow runs every Monday and on demand:

- `govulncheck` over the vendored build, including the Go standard library,
  which Dependabot does not cover.
- A repository activity alarm. GitHub disables scheduled workflows after 60
  days without a commit on the default branch, so the check fails after 45.
  Merge a maintenance commit; if the schedule was disabled, re-enable
  Vulncheck under Actions.
- `scripts/check-go-support.sh latest`, which fails once a newer Go release
  series exists, before the current one leaves upstream support. CI runs it
  without `latest`, failing only once the series is unsupported.
- The Mac signing certificate's expiry. When `MACOS_CERT_P12_BASE64` is set,
  the check reads it with `MACOS_CERT_PASSWORD` and fails from 28 days
  before the certificate expires; renew it and replace both secrets. When
  `MACOS_SIGN_IDENTITY` is a SHA-1 fingerprint (any case, with or without
  colons), it picks the matching leaf certificate; otherwise the export must
  hold exactly one. Without the secret, the check reports a notice and
  passes.
